package workflow

import (
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/barbarosydev/ai-factory/internal/activities"
	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/contracts"
)

const (
	SignalHumanReply       = "human-reply"
	QueryFactoryStatus     = "factory-status"
	maxRepairAttempts      = 3
	maxClarificationRounds = 2
	humanWaitTimeout       = 7 * 24 * time.Hour
)

// HumanReplySignal wakes a paused factory:waiting workflow when a human comments or re-labels.
type HumanReplySignal struct {
	CommentID        int64  `json:"comment_id"`
	Author           string `json:"author"`
	Body             string `json:"body"`
	CreatedAt        string `json:"created_at"`
	UpdatedIssueBody string `json:"updated_issue_body,omitempty"`
}

// WorkflowStatus is exposed via Temporal query handler "factory-status".
type WorkflowStatus struct {
	IssueNumber  int                 `json:"issue_number"`
	Stage        string              `json:"stage"`
	Attempt      int                 `json:"attempt"`
	SiteSlug     string              `json:"site_slug,omitempty"`
	AutonomyMode config.AutonomyMode `json:"autonomy_mode"`
}

// WorkflowInput starts IssueFactoryWorkflow for one eligible GitHub issue.
type WorkflowInput struct {
	Issue        contracts.CustomerIssueInput `json:"issue"`
	AutonomyMode config.AutonomyMode          `json:"autonomy_mode"`
	SkipRemoteCI bool                         `json:"skip_remote_ci"`
}

// WorkflowResult summarizes the terminal state of an IssueFactoryWorkflow execution.
type WorkflowResult struct {
	IssueNumber int    `json:"issue_number"`
	FinalState  string `json:"final_state"` // "review" | "done" | "rejected" | "failed"
	SiteSlug    string `json:"site_slug,omitempty"`
	Attempts    int    `json:"attempts"`
	PRURL       string `json:"pr_url,omitempty"`
	Merged      bool   `json:"merged"`
	Reason      string `json:"reason,omitempty"`
}

// WorkflowID returns the deterministic Temporal workflow ID for a GitHub issue.
func WorkflowID(issueNumber int) string {
	return "factory-issue-" + strconv.Itoa(issueNumber)
}

// IssueFactoryWorkflow orchestrates a single GitHub issue from intake through PM specification,
// optional human clarification wait, bounded 3-attempt Dev/Tester/Reviewer repair loop,
// PR publishing, and optional auto-merge.
func IssueFactoryWorkflow(ctx workflow.Context, in WorkflowInput) (WorkflowResult, error) {
	status := WorkflowStatus{
		IssueNumber:  in.Issue.IssueNumber,
		Stage:        "intake",
		AutonomyMode: in.AutonomyMode,
	}
	if err := workflow.SetQueryHandler(ctx, QueryFactoryStatus, func() (WorkflowStatus, error) {
		return status, nil
	}); err != nil {
		return WorkflowResult{}, err
	}

	var acts *activities.FactoryActivities

	ghOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    4,
		},
	}
	pmOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		HeartbeatTimeout:    3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    3 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	}
	devOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Minute,
		HeartbeatTimeout:    5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    3 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    2,
		},
	}
	testOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		HeartbeatTimeout:    3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    3 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    2,
		},
	}
	revOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		HeartbeatTimeout:    3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    3 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    2,
		},
	}
	mergeOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		HeartbeatTimeout:    2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	}

	ghCtx := workflow.WithActivityOptions(ctx, ghOpts)
	pmCtx := workflow.WithActivityOptions(ctx, pmOpts)
	devCtx := workflow.WithActivityOptions(ctx, devOpts)
	testCtx := workflow.WithActivityOptions(ctx, testOpts)
	revCtx := workflow.WithActivityOptions(ctx, revOpts)
	mergeCtx := workflow.WithActivityOptions(ctx, mergeOpts)

	// Transition factory:ready -> factory:running
	if err := workflow.ExecuteActivity(ghCtx, acts.TransitionIssueStateActivity, activities.IssueTransition{
		IssueNumber: in.Issue.IssueNumber,
		RemoveLabel: "factory:ready",
		AddLabel:    "factory:running",
		CommentBody: "AI Software Factory started workflow `" + WorkflowID(in.Issue.IssueNumber) + "` (mode: `" + string(in.AutonomyMode) + "`).",
	}).Get(ctx, nil); err != nil {
		return WorkflowResult{}, err
	}

	// PM analysis + bounded human clarification wait loop
	issue := in.Issue
	var pmOut contracts.PMDecisionOutput
	humanReplyCh := workflow.GetSignalChannel(ctx, SignalHumanReply)

	for round := 0; round <= maxClarificationRounds; round++ {
		status.Stage = "pm_analysis"
		if err := workflow.ExecuteActivity(pmCtx, acts.RunPMAgentActivity, issue).Get(ctx, &pmOut); err != nil {
			_ = markFailed(ghCtx, acts, in.Issue.IssueNumber, "PM Agent failed: "+err.Error())
			return WorkflowResult{IssueNumber: in.Issue.IssueNumber, FinalState: "failed", Reason: err.Error()}, err
		}

		if pmOut.Status == "rejected" {
			status.Stage = "rejected"
			_ = workflow.ExecuteActivity(ghCtx, acts.TransitionIssueStateActivity, activities.IssueTransition{
				IssueNumber: in.Issue.IssueNumber,
				RemoveLabel: "factory:running",
				AddLabel:    "factory:rejected",
				CommentBody: "Request rejected as outside AI Software Factory v1 scope:\n\n> " + pmOut.RejectionReason,
				CloseIssue:  true,
			}).Get(ctx, nil)
			return WorkflowResult{
				IssueNumber: in.Issue.IssueNumber,
				FinalState:  "rejected",
				Reason:      pmOut.RejectionReason,
			}, nil
		}

		if pmOut.Status == "ready" && pmOut.Spec != nil {
			break
		}

		if round == maxClarificationRounds {
			msg := "Clarification still incomplete after 2 human-in-the-loop rounds."
			_ = markFailed(ghCtx, acts, in.Issue.IssueNumber, msg)
			return WorkflowResult{IssueNumber: in.Issue.IssueNumber, FinalState: "failed", Reason: msg}, nil
		}

		status.Stage = "waiting_human"
		if err := workflow.ExecuteActivity(ghCtx, acts.TransitionIssueStateActivity, activities.IssueTransition{
			IssueNumber: in.Issue.IssueNumber,
			RemoveLabel: "factory:running",
			AddLabel:    "factory:waiting",
			Questions:   pmOut.ClarificationQuestions,
		}).Get(ctx, nil); err != nil {
			return WorkflowResult{}, err
		}

		var reply HumanReplySignal
		var timedOut bool
		timerCtx, cancelTimer := workflow.WithCancel(ctx)
		timerFuture := workflow.NewTimer(timerCtx, humanWaitTimeout)

		sel := workflow.NewSelector(ctx)
		sel.AddReceive(humanReplyCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, &reply)
			cancelTimer()
		})
		sel.AddFuture(timerFuture, func(f workflow.Future) {
			_ = f.Get(ctx, nil)
			timedOut = true
		})
		sel.Select(ctx)

		if timedOut {
			msg := "Timed out waiting 7 days for human clarification."
			_ = workflow.ExecuteActivity(ghCtx, acts.TransitionIssueStateActivity, activities.IssueTransition{
				IssueNumber: in.Issue.IssueNumber,
				RemoveLabel: "factory:waiting",
				AddLabel:    "factory:failed",
				CommentBody: msg,
			}).Get(ctx, nil)
			return WorkflowResult{IssueNumber: in.Issue.IssueNumber, FinalState: "failed", Reason: msg}, nil
		}

		if strings.TrimSpace(reply.UpdatedIssueBody) != "" {
			issue.IssueBody = reply.UpdatedIssueBody
		}
		if strings.TrimSpace(reply.Body) != "" {
			issue.Clarifications = append(issue.Clarifications, contracts.IssueComment{
				ID:        reply.CommentID,
				Author:    reply.Author,
				Body:      reply.Body,
				CreatedAt: reply.CreatedAt,
			})
		}

		_ = workflow.ExecuteActivity(ghCtx, acts.TransitionIssueStateActivity, activities.IssueTransition{
			IssueNumber: in.Issue.IssueNumber,
			RemoveLabel: "factory:waiting",
			AddLabel:    "factory:running",
			CommentBody: "Received clarification from @" + reply.Author + "; resuming build.",
		}).Get(ctx, nil)
	}

	spec := *pmOut.Spec
	status.SiteSlug = spec.SiteSlug
	status.Stage = "prepare_worktree"

	var worktreeDir string
	if err := workflow.ExecuteActivity(ghCtx, acts.PrepareWorktreeActivity, activities.PrepareWorktreeInput{
		IssueNumber: in.Issue.IssueNumber,
		SiteSlug:    spec.SiteSlug,
	}).Get(ctx, &worktreeDir); err != nil {
		_ = markFailed(ghCtx, acts, in.Issue.IssueNumber, "Failed to prepare git worktree: "+err.Error())
		return WorkflowResult{IssueNumber: in.Issue.IssueNumber, FinalState: "failed", SiteSlug: spec.SiteSlug, Reason: err.Error()}, err
	}

	worktreeFailed := true
	var reports []contracts.TestReport
	defer func() {
		if worktreeDir == "" {
			return
		}
		cleanupCtx, _ := workflow.NewDisconnectedContext(ctx)
		cleanupActCtx := workflow.WithActivityOptions(cleanupCtx, ghOpts)
		_ = workflow.ExecuteActivity(cleanupActCtx, acts.CleanupWorktreeActivity, activities.CleanupInput{
			WorktreeDir: worktreeDir,
			IssueNumber: in.Issue.IssueNumber,
			SiteSlug:    spec.SiteSlug,
			Failed:      worktreeFailed,
			Reports:     reports,
		}).Get(cleanupCtx, nil)
	}()

	// Bounded Semantic Repair Loop (up to 3 implementation attempts)
	var priorFailures []contracts.FailureEvidence
	var lastImpl contracts.ImplementationResult
	var lastReport contracts.TestReport
	var lastReview contracts.ReviewResult
	var verified bool

	for attempt := 1; attempt <= maxRepairAttempts; attempt++ {
		status.Attempt = attempt
		status.Stage = "dev_attempt_" + strconv.Itoa(attempt)

		if err := workflow.ExecuteActivity(devCtx, acts.RunDeveloperAgentActivity, activities.DeveloperActivityInput{
			WorktreeDir: worktreeDir,
			Task: contracts.DeveloperTaskInput{
				IssueNumber:   in.Issue.IssueNumber,
				Attempt:       attempt,
				Spec:          spec,
				PriorFailures: priorFailures,
			},
		}).Get(ctx, &lastImpl); err != nil {
			priorFailures = []contracts.FailureEvidence{{
				CheckName:   "developer-runtime",
				Expected:    "Valid static site files created",
				Actual:      err.Error(),
				Remediation: "Ensure index.html and styles.css are written inside sites/<slug>/.",
			}}
			continue
		}

		status.Stage = "test_attempt_" + strconv.Itoa(attempt)
		if err := workflow.ExecuteActivity(testCtx, acts.RunTesterAgentActivity, activities.TesterActivityInput{
			WorktreeDir: worktreeDir,
			Input: contracts.TestRunInput{
				IssueNumber: in.Issue.IssueNumber,
				Attempt:     attempt,
				Spec:        spec,
			},
		}).Get(ctx, &lastReport); err != nil {
			priorFailures = []contracts.FailureEvidence{{
				CheckName:   "tester-runtime",
				Expected:    "Playwright verification completes",
				Actual:      err.Error(),
				Remediation: "Ensure valid HTML5 and local assets.",
			}}
			continue
		}
		reports = append(reports, lastReport)

		if !lastReport.Passed {
			priorFailures = lastReport.Failures
			continue
		}

		status.Stage = "reviewing"
		if err := workflow.ExecuteActivity(revCtx, acts.RunReviewerActivity, activities.ReviewerActivityInput{
			WorktreeDir: worktreeDir,
			Input: contracts.ReviewInput{
				Issue:        issue,
				Spec:         spec,
				TestReport:   lastReport,
				ChangedFiles: lastImpl.FilesChanged,
			},
		}).Get(ctx, &lastReview); err != nil {
			priorFailures = []contracts.FailureEvidence{{
				CheckName:   "reviewer-runtime",
				Expected:    "Security & fidelity review completes",
				Actual:      err.Error(),
				Remediation: "Verify site files and retry.",
			}}
			continue
		}

		if lastReview.Approved {
			verified = true
			break
		}

		var revFailures []contracts.FailureEvidence
		for _, finding := range lastReview.Findings {
			revFailures = append(revFailures, contracts.FailureEvidence{
				CheckName:   "reviewer-finding",
				Expected:    "Clean path isolation, zero external assets, and spec fidelity",
				Actual:      finding,
				Remediation: finding,
			})
		}
		priorFailures = revFailures
	}

	if !verified {
		status.Stage = "failed"
		diag := formatFailureComment(spec.SiteSlug, reports, priorFailures)
		_ = markFailed(ghCtx, acts, in.Issue.IssueNumber, diag)
		return WorkflowResult{
			IssueNumber: in.Issue.IssueNumber,
			FinalState:  "failed",
			SiteSlug:    spec.SiteSlug,
			Attempts:    status.Attempt,
			Reason:      "Exhausted 3 bounded repair attempts",
		}, nil
	}

	// Publish PR
	status.Stage = "publishing_pr"
	var pr activities.PRResult
	if err := workflow.ExecuteActivity(ghCtx, acts.PublishPullRequestActivity, activities.PublishPRInput{
		WorktreeDir:  worktreeDir,
		IssueNumber:  in.Issue.IssueNumber,
		IssueTitle:   issue.IssueTitle,
		Spec:         spec,
		TestReport:   lastReport,
		AllReports:   reports,
		Review:       lastReview,
		Attempts:     status.Attempt,
		AutonomyMode: in.AutonomyMode,
	}).Get(ctx, &pr); err != nil {
		_ = markFailed(ghCtx, acts, in.Issue.IssueNumber, "Failed to publish Pull Request: "+err.Error())
		return WorkflowResult{IssueNumber: in.Issue.IssueNumber, FinalState: "failed", SiteSlug: spec.SiteSlug, Attempts: status.Attempt, Reason: err.Error()}, err
	}

	// Worktree succeeded; do not snapshot as failed during deferred cleanup.
	worktreeFailed = false

	if in.AutonomyMode == config.AutonomyAuto {
		status.Stage = "awaiting_ci_and_merge"
		var mergeRes activities.MergeResult
		if err := workflow.ExecuteActivity(mergeCtx, acts.AwaitChecksAndMergeActivity, activities.MergeRequest{
			IssueNumber:  in.Issue.IssueNumber,
			PRNumber:     pr.Number,
			HeadSHA:      pr.SHA,
			SiteSlug:     spec.SiteSlug,
			SkipRemoteCI: in.SkipRemoteCI,
		}).Get(ctx, &mergeRes); err != nil {
			_ = workflow.ExecuteActivity(ghCtx, acts.TransitionIssueStateActivity, activities.IssueTransition{
				IssueNumber: in.Issue.IssueNumber,
				RemoveLabel: "factory:running",
				AddLabel:    "factory:review",
				CommentBody: "PR #" + strconv.Itoa(pr.Number) + " (" + pr.URL + ") passed local verification, but autonomous merge paused/failed (" + err.Error() + "). Switched to `factory:review` for human attention.",
			}).Get(ctx, nil)
			return WorkflowResult{
				IssueNumber: in.Issue.IssueNumber,
				FinalState:  "review",
				SiteSlug:    spec.SiteSlug,
				Attempts:    status.Attempt,
				PRURL:       pr.URL,
				Merged:      false,
				Reason:      err.Error(),
			}, nil
		}

		status.Stage = "done"
		return WorkflowResult{
			IssueNumber: in.Issue.IssueNumber,
			FinalState:  "done",
			SiteSlug:    spec.SiteSlug,
			Attempts:    status.Attempt,
			PRURL:       pr.URL,
			Merged:      mergeRes.Merged,
		}, nil
	}

	status.Stage = "review"
	if err := workflow.ExecuteActivity(ghCtx, acts.TransitionIssueStateActivity, activities.IssueTransition{
		IssueNumber: in.Issue.IssueNumber,
		RemoveLabel: "factory:running",
		AddLabel:    "factory:review",
		CommentBody: "Website build verified in " + strconv.Itoa(status.Attempt) + " attempt(s). Pull request ready for review: " + pr.URL,
	}).Get(ctx, nil); err != nil {
		return WorkflowResult{}, err
	}

	return WorkflowResult{
		IssueNumber: in.Issue.IssueNumber,
		FinalState:  "review",
		SiteSlug:    spec.SiteSlug,
		Attempts:    status.Attempt,
		PRURL:       pr.URL,
		Merged:      false,
	}, nil
}

func markFailed(ghCtx workflow.Context, acts *activities.FactoryActivities, issueNumber int, body string) error {
	return workflow.ExecuteActivity(ghCtx, acts.TransitionIssueStateActivity, activities.IssueTransition{
		IssueNumber: issueNumber,
		RemoveLabel: "factory:running",
		AddLabel:    "factory:failed",
		CommentBody: body,
	}).Get(ghCtx, nil)
}

func formatFailureComment(slug string, reports []contracts.TestReport, remaining []contracts.FailureEvidence) string {
	var b strings.Builder
	b.WriteString("AI Software Factory could not verify `sites/" + slug + "/` after " + strconv.Itoa(maxRepairAttempts) + " bounded repair attempt(s).\n\n")
	if len(reports) > 0 {
		b.WriteString("### Attempt Summary\n")
		for _, r := range reports {
			b.WriteString("- Attempt " + strconv.Itoa(r.Attempt) + ": passed=" + strconv.FormatBool(r.Passed) + " (" + strconv.Itoa(len(r.Failures)) + " failure(s)) — " + r.TesterSummary + "\n")
		}
		b.WriteString("\n")
	}
	if len(remaining) > 0 {
		b.WriteString("### Final Failure Evidence\n")
		for _, f := range remaining {
			b.WriteString("- **" + f.CheckName + "**: expected `" + f.Expected + "`, got `" + f.Actual + "` (" + f.Remediation + ")\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("A diagnostic snapshot was archived locally under `.worktrees/`. Re-add `factory:ready` after updating the issue to retry.")
	return b.String()
}
