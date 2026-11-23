package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/barbarosydev/ai-factory/internal/activities"
	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/internal/githubapp"
	"github.com/barbarosydev/ai-factory/internal/gitworktree"
	"github.com/barbarosydev/ai-factory/internal/telemetry"
	"github.com/barbarosydev/ai-factory/internal/workflow"
)

// RunOptions controls how factory run/once executes.
type RunOptions struct {
	AutonomyMode config.AutonomyMode
	SkipRemoteCI bool
	Once         bool
	IssueNumber  int
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

	logger := telemetry.NewLogger(cfg.LogFormat, out)
	reg := telemetry.NewRegistry()
	stopMetrics, err := telemetry.StartServer(ctx, cfg.MetricsAddr, reg, logger)
	if err != nil {
		return err
	}
	defer stopMetrics()

	var gitMgr *gitworktree.Manager
	ghClient, auth, err := newGitHubClient(cfg, func() ([]string, error) {
		return gitMgr.ListExistingSites()
	})
	if err != nil {
		return err
	}
	gitMgr = gitworktree.NewManager(cfg.WebsitesRepoPath, cfg.GitHubAppID, auth.Token)

	acts := &activities.FactoryActivities{
		Git:    gitMgr,
		GitHub: ghClient,
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
	// a slot for sticky workflow cache tasks. MaxConcurrentActivityExecutionSize defaults to 1 to keep
	// at most one LLM / Playwright / Git activity active on the Mac (raise on multi-GPU / AKS workers).
	w := worker.New(tc, cfg.TaskQueue, worker.Options{
		MaxConcurrentActivityExecutionSize: cfg.MaxConcurrentActivities,
	})
	w.RegisterWorkflow(workflow.IssueFactoryWorkflow)
	w.RegisterActivity(acts)

	if err := w.Start(); err != nil {
		return fmt.Errorf("start temporal worker: %w", err)
	}
	reg.SetReady(true)
	defer reg.SetReady(false)
	defer w.Stop()

	logger.Info("ai-factory worker started",
		"temporal", cfg.TemporalAddress,
		"queue", cfg.TaskQueue,
		"agent_queue", cfg.AgentTaskQueue,
		"repo", cfg.GitHubOwner+"/"+cfg.GitHubRepo,
		"mode", opts.AutonomyMode,
		"max_concurrent_activities", cfg.MaxConcurrentActivities,
	)

	if opts.Once {
		return runOnce(ctx, cfg, tc, ghClient, opts, reg, logger, out)
	}
	return runPoller(ctx, cfg, tc, ghClient, opts, reg, logger, out)
}

func runOnce(
	ctx context.Context,
	cfg config.Config,
	tc client.Client,
	ghClient *githubapp.Client,
	opts RunOptions,
	reg *telemetry.Registry,
	logger *slog.Logger,
	out io.Writer,
) error {
	start := time.Now()
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
			elapsed := time.Since(start)
			if done.err != nil {
				reg.RecordWorkflow("failed", elapsed, 0)
				return fmt.Errorf("workflow %s failed: %w", wfID, done.err)
			}
			reg.RecordWorkflow(done.res.FinalState, elapsed, done.res.Attempts)
			if logger != nil {
				logger.Info("workflow finished",
					"workflow_id", wfID,
					"final_state", done.res.FinalState,
					"site", done.res.SiteSlug,
					"attempts", done.res.Attempts,
					"merged", done.res.Merged,
					"pr_url", done.res.PRURL,
					"duration_ms", elapsed.Milliseconds(),
				)
			}
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
	reg *telemetry.Registry,
	logger *slog.Logger,
	out io.Writer,
) error {
	signaledComments := make(map[int]int64)
	watchedRuns := make(map[string]bool)
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
		runID, err := ensureStartedOrSignaled(ctx, tc, cfg, nextIssue, opts, out)
		if err != nil {
			_, _ = fmt.Fprintf(out, "start workflow for issue #%d error: %v\n", nextIssue.IssueNumber, err)
			return
		}
		wfID := workflow.WorkflowID(nextIssue.IssueNumber)
		watchKey := wfID + "/" + runID
		if runID != "" && !watchedRuns[watchKey] {
			watchedRuns[watchKey] = true
			start := time.Now()
			go observeWorkflowCompletion(ctx, tc, wfID, runID, start, reg, logger)
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

func observeWorkflowCompletion(
	ctx context.Context,
	tc client.Client,
	wfID string,
	runID string,
	start time.Time,
	reg *telemetry.Registry,
	logger *slog.Logger,
) {
	var res workflow.WorkflowResult
	err := tc.GetWorkflow(ctx, wfID, runID).Get(ctx, &res)
	if ctx.Err() != nil {
		return
	}
	elapsed := time.Since(start)
	if err != nil {
		reg.RecordWorkflow("failed", elapsed, 0)
		if logger != nil {
			logger.Warn("workflow failed", "workflow_id", wfID, "run_id", runID, "error", err)
		}
		return
	}
	reg.RecordWorkflow(res.FinalState, elapsed, res.Attempts)
	if logger != nil {
		logger.Info("workflow finished",
			"workflow_id", wfID,
			"final_state", res.FinalState,
			"site", res.SiteSlug,
			"attempts", res.Attempts,
			"merged", res.Merged,
			"pr_url", res.PRURL,
			"duration_ms", elapsed.Milliseconds(),
		)
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
		Issue:          issue,
		AutonomyMode:   opts.AutonomyMode,
		SkipRemoteCI:   opts.SkipRemoteCI,
		Policy:         cfg.Policy,
		AgentTaskQueue: cfg.AgentTaskQueue,
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
