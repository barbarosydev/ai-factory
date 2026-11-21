package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/barbarosydev/ai-factory/internal/activities"
	"github.com/barbarosydev/ai-factory/internal/agents/dev"
	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/agents/pm"
	"github.com/barbarosydev/ai-factory/internal/agents/reviewer"
	"github.com/barbarosydev/ai-factory/internal/agents/tester"
	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/internal/evaluator"
	"github.com/barbarosydev/ai-factory/internal/githubapp"
	"github.com/barbarosydev/ai-factory/internal/gitworktree"
	"github.com/barbarosydev/ai-factory/internal/workflow"
)

// RunOptions controls how factory run/once executes.
type RunOptions struct {
	AutonomyMode config.AutonomyMode
	SkipRemoteCI bool
	Once         bool
	IssueNumber  int
}

type inspectorAdapter struct {
	git *gitworktree.Manager
	ev  *evaluator.PlaywrightEvaluator
}

func (a inspectorAdapter) InspectGitDiff(ctx context.Context, worktreeDir, slug string) (reviewer.DiffAudit, error) {
	return a.git.InspectGitDiff(ctx, worktreeDir, slug)
}

func (a inspectorAdapter) ScanExternalURLs(worktreeDir, slug string) ([]string, error) {
	return a.ev.ScanExternalURLs(worktreeDir, slug)
}

func (a inspectorAdapter) ReadSiteFile(worktreeDir, slug, relPath string) (string, error) {
	return a.ev.ReadSiteFile(worktreeDir, slug, relPath)
}

// Run starts the Temporal worker and single-issue GitHub App poller until signaled to stop (or once completes).
func Run(ctx context.Context, cfg config.Config, opts RunOptions, out io.Writer) error {
	if opts.AutonomyMode == "" {
		opts.AutonomyMode = cfg.AutonomyMode
	}
	if opts.AutonomyMode != config.AutonomyReview && opts.AutonomyMode != config.AutonomyAuto {
		return fmt.Errorf("invalid mode %q: must be review or auto", opts.AutonomyMode)
	}
	if err := cfg.ValidateRuntimeSecrets(true); err != nil {
		return err
	}

	var gitMgr *gitworktree.Manager
	ghClient, auth, err := newGitHubClient(cfg, func() ([]string, error) {
		return gitMgr.ListExistingSites()
	})
	if err != nil {
		return err
	}
	gitMgr = gitworktree.NewManager(cfg.WebsitesRepoPath, cfg.GitHubAppID, auth.Token)
	ev := evaluator.NewPlaywrightEvaluator(cfg.WebsitesRepoPath)

	pmAgent := pm.NewAgent(llm.NewClient(cfg.LiteLLMBaseURL, cfg.PMKey, 30*time.Minute), cfg.NumCtx, gitMgr)
	devAgent := dev.NewAgent(llm.NewClient(cfg.LiteLLMBaseURL, cfg.DevKey, 30*time.Minute), cfg.NumCtx)
	testAgent := tester.NewAgent(llm.NewClient(cfg.LiteLLMBaseURL, cfg.TestKey, 30*time.Minute), cfg.NumCtx, ev)
	revAgent := reviewer.NewAgent(llm.NewClient(cfg.LiteLLMBaseURL, cfg.SecKey, 30*time.Minute), cfg.NumCtx, inspectorAdapter{git: gitMgr, ev: ev})

	acts := &activities.FactoryActivities{
		PM:       pmAgent,
		Dev:      devAgent,
		Tester:   testAgent,
		Reviewer: revAgent,
		Git:      gitMgr,
		GitHub:   ghClient,
	}

	tc, err := client.Dial(client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
	})
	if err != nil {
		return fmt.Errorf("dial temporal %s: %w", cfg.TemporalAddress, err)
	}
	defer tc.Close()

	// Note: Temporal Go SDK panics if MaxConcurrentWorkflowTaskExecutionSize == 1 because it reserves
	// a slot for sticky workflow cache tasks. MaxConcurrentActivityExecutionSize = 1 enforces that
	// at most one LLM / Playwright / Git activity executes at a time on the Mac.
	w := worker.New(tc, cfg.TaskQueue, worker.Options{
		MaxConcurrentActivityExecutionSize: 1,
	})
	w.RegisterWorkflow(workflow.IssueFactoryWorkflow)
	w.RegisterActivity(acts)

	if err := w.Start(); err != nil {
		return fmt.Errorf("start temporal worker: %w", err)
	}
	defer w.Stop()

	_, _ = fmt.Fprintf(out, "ai-factory worker started (temporal=%s queue=%s repo=%s/%s mode=%s)\n",
		cfg.TemporalAddress, cfg.TaskQueue, cfg.GitHubOwner, cfg.GitHubRepo, opts.AutonomyMode)

	if opts.Once {
		return runOnce(ctx, cfg, tc, ghClient, opts, out)
	}
	return runPoller(ctx, cfg, tc, ghClient, opts, out)
}

func runOnce(
	ctx context.Context,
	cfg config.Config,
	tc client.Client,
	ghClient *githubapp.Client,
	opts RunOptions,
	out io.Writer,
) error {
	var issue contracts.CustomerIssueInput
	if opts.IssueNumber > 0 {
		fetched, _, err := ghClient.GetIssue(ctx, opts.IssueNumber)
		if err != nil {
			return err
		}
		issue = fetched
	} else {
		polled, err := ghClient.PollOpenFactoryIssues(ctx)
		if err != nil {
			return err
		}
		if len(polled.Ready) == 0 {
			_, _ = fmt.Fprintln(out, "no eligible factory:ready issues found")
			return nil
		}
		issue = polled.Ready[0]
	}

	wfID := workflow.WorkflowID(issue.IssueNumber)
	runID, err := ensureStartedOrSignaled(ctx, tc, cfg, issue, opts, out)
	if err != nil {
		return err
	}

	type doneMsg struct {
		res workflow.WorkflowResult
		err error
	}
	doneCh := make(chan doneMsg, 1)
	go func() {
		var res workflow.WorkflowResult
		getErr := tc.GetWorkflow(ctx, wfID, runID).Get(ctx, &res)
		doneCh <- doneMsg{res: res, err: getErr}
	}()

	signaledComments := make(map[int]int64)
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case done := <-doneCh:
			if done.err != nil {
				return fmt.Errorf("workflow %s failed: %w", wfID, done.err)
			}
			_, _ = fmt.Fprintf(out, "workflow %s finished: state=%s site=%q attempts=%d merged=%t pr=%s reason=%s\n",
				wfID, done.res.FinalState, done.res.SiteSlug, done.res.Attempts, done.res.Merged, done.res.PRURL, done.res.Reason)
			return nil
		case <-ticker.C:
			polled, err := ghClient.PollOpenFactoryIssues(ctx)
			if err != nil {
				_, _ = fmt.Fprintf(out, "poll warning: %v\n", err)
				continue
			}
			forwardWaitingSignals(ctx, tc, polled.WaitingUpdates, signaledComments, opts.IssueNumber, out)
		}
	}
}

func runPoller(
	ctx context.Context,
	cfg config.Config,
	tc client.Client,
	ghClient *githubapp.Client,
	opts RunOptions,
	out io.Writer,
) error {
	signaledComments := make(map[int]int64)
	pollTick := func() {
		polled, err := ghClient.PollOpenFactoryIssues(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(out, "github poll error: %v\n", err)
			return
		}

		forwardWaitingSignals(ctx, tc, polled.WaitingUpdates, signaledComments, 0, out)

		busy, activeID, err := hasActiveFactoryWorkflow(ctx, tc, cfg.TemporalNamespace)
		if err != nil {
			_, _ = fmt.Fprintf(out, "temporal visibility error: %v\n", err)
			return
		}
		if busy {
			return
		}
		_ = activeID

		if len(polled.Ready) == 0 {
			return
		}
		nextIssue := polled.Ready[0]
		if _, err := ensureStartedOrSignaled(ctx, tc, cfg, nextIssue, opts, out); err != nil {
			_, _ = fmt.Fprintf(out, "start workflow for issue #%d error: %v\n", nextIssue.IssueNumber, err)
		}
	}

	pollTick()
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_, _ = fmt.Fprintln(out, "shutdown signal received; stopping poller")
			return nil
		case <-ticker.C:
			pollTick()
		}
	}
}

func forwardWaitingSignals(
	ctx context.Context,
	tc client.Client,
	updates []githubapp.WaitingIssueUpdate,
	signaled map[int]int64,
	onlyIssue int,
	out io.Writer,
) {
	for _, upd := range updates {
		num := upd.Issue.IssueNumber
		if onlyIssue > 0 && num != onlyIssue {
			continue
		}
		if signaled[num] == upd.LatestHumanComment.ID {
			continue
		}
		wfID := workflow.WorkflowID(num)
		running, st, _, err := describeFactoryStage(ctx, tc, wfID)
		if err != nil || !running || st.Stage != "waiting_human" {
			continue
		}

		sig := workflow.HumanReplySignal{
			CommentID:        upd.LatestHumanComment.ID,
			Author:           upd.LatestHumanComment.Author,
			Body:             upd.LatestHumanComment.Body,
			CreatedAt:        upd.LatestHumanComment.CreatedAt,
			UpdatedIssueBody: upd.Issue.IssueBody,
		}
		if err := tc.SignalWorkflow(ctx, wfID, "", workflow.SignalHumanReply, sig); err != nil {
			_, _ = fmt.Fprintf(out, "signal warning for %s: %v\n", wfID, err)
			continue
		}
		signaled[num] = upd.LatestHumanComment.ID
		_, _ = fmt.Fprintf(out, "signaled %s with human clarification comment %d by %s\n", wfID, upd.LatestHumanComment.ID, upd.LatestHumanComment.Author)
	}
}

func hasActiveFactoryWorkflow(ctx context.Context, tc client.Client, namespace string) (bool, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := tc.ListWorkflow(reqCtx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: namespace,
		PageSize:  20,
		Query:     "WorkflowType = 'IssueFactoryWorkflow' AND ExecutionStatus = 'Running'",
	})
	if err != nil {
		return false, "", err
	}
	for _, ex := range resp.GetExecutions() {
		wfID := ex.GetExecution().GetWorkflowId()
		runID := ex.GetExecution().GetRunId()
		val, qErr := tc.QueryWorkflow(reqCtx, wfID, runID, workflow.QueryFactoryStatus)
		if qErr != nil {
			return true, wfID, nil
		}
		var st workflow.WorkflowStatus
		if val.Get(&st) != nil || st.Stage != "waiting_human" {
			return true, wfID, nil
		}
	}
	return false, "", nil
}

func ensureStartedOrSignaled(
	ctx context.Context,
	tc client.Client,
	cfg config.Config,
	issue contracts.CustomerIssueInput,
	opts RunOptions,
	out io.Writer,
) (string, error) {
	wfID := workflow.WorkflowID(issue.IssueNumber)
	running, st, runID, err := describeFactoryStage(ctx, tc, wfID)
	if err == nil && running {
		if st.Stage == "waiting_human" {
			sig := workflow.HumanReplySignal{
				CommentID:        time.Now().Unix(),
				Author:           "github-label",
				Body:             "(re-queued via factory:ready label)",
				CreatedAt:        time.Now().UTC().Format(time.RFC3339),
				UpdatedIssueBody: issue.IssueBody,
			}
			if sigErr := tc.SignalWorkflow(ctx, wfID, runID, workflow.SignalHumanReply, sig); sigErr != nil {
				return "", fmt.Errorf("resume waiting workflow %s: %w", wfID, sigErr)
			}
			_, _ = fmt.Fprintf(out, "resumed paused workflow %s (run=%s)\n", wfID, runID)
			return runID, nil
		}
		_, _ = fmt.Fprintf(out, "attached to running workflow %s (run=%s stage=%s)\n", wfID, runID, st.Stage)
		return runID, nil
	}

	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    wfID,
		TaskQueue:             cfg.TaskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}, workflow.IssueFactoryWorkflow, workflow.WorkflowInput{
		Issue:        issue,
		AutonomyMode: opts.AutonomyMode,
		SkipRemoteCI: opts.SkipRemoteCI,
	})
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(out, "started workflow %s (run=%s issue=#%d mode=%s)\n", wfID, run.GetRunID(), issue.IssueNumber, opts.AutonomyMode)
	return run.GetRunID(), nil
}

func describeFactoryStage(ctx context.Context, tc client.Client, wfID string) (bool, workflow.WorkflowStatus, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	desc, err := tc.DescribeWorkflow(reqCtx, wfID, "")
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			return false, workflow.WorkflowStatus{}, "", nil
		}
		return false, workflow.WorkflowStatus{}, "", err
	}
	info := desc.RawResponse.GetWorkflowExecutionInfo()
	if info.GetStatus() != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
		return false, workflow.WorkflowStatus{}, info.GetExecution().GetRunId(), nil
	}
	runID := info.GetExecution().GetRunId()
	val, qErr := tc.QueryWorkflow(reqCtx, wfID, runID, workflow.QueryFactoryStatus)
	if qErr != nil {
		return true, workflow.WorkflowStatus{}, runID, nil
	}
	var st workflow.WorkflowStatus
	_ = val.Get(&st)
	return true, st, runID, nil
}
