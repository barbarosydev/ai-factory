package activities

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/barbarosydev/ai-factory/internal/agents/dev"
	"github.com/barbarosydev/ai-factory/internal/agents/pm"
	"github.com/barbarosydev/ai-factory/internal/agents/reviewer"
	"github.com/barbarosydev/ai-factory/internal/agents/tester"
	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/internal/gitworktree"
)

// IssueTransition describes a factory label swap and optional comment/close on GitHub.
type IssueTransition struct {
	IssueNumber int      `json:"issue_number"`
	RemoveLabel string   `json:"remove_label,omitempty"`
	AddLabel    string   `json:"add_label"`
	CommentBody string   `json:"comment_body,omitempty"`
	CloseIssue  bool     `json:"close_issue,omitempty"`
	Questions   []string `json:"questions,omitempty"`
}

// PRRequest describes a Pull Request to create or update for a completed build.
type PRRequest struct {
	IssueNumber  int                            `json:"issue_number"`
	IssueTitle   string                         `json:"issue_title"`
	Branch       string                         `json:"branch"`
	CommitSHA    string                         `json:"commit_sha"`
	Spec         contracts.WebsiteSpecification `json:"spec"`
	TestReport   contracts.TestReport           `json:"test_report"`
	AllReports   []contracts.TestReport         `json:"all_reports"`
	Review       contracts.ReviewResult         `json:"review"`
	Attempts     int                            `json:"attempts"`
	AutonomyMode config.AutonomyMode            `json:"autonomy_mode"`
}

// PRResult is returned after opening or updating a GitHub Pull Request.
type PRResult struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Head   string `json:"head"`
	SHA    string `json:"sha"`
}

// MergeRequest configures waiting on PR checks and squash-merging in auto mode.
type MergeRequest struct {
	IssueNumber  int    `json:"issue_number"`
	PRNumber     int    `json:"pr_number"`
	HeadSHA      string `json:"head_sha"`
	SiteSlug     string `json:"site_slug"`
	SkipRemoteCI bool   `json:"skip_remote_ci"`
}

// MergeResult reports the outcome of AwaitChecksAndMerge.
type MergeResult struct {
	Merged bool   `json:"merged"`
	SHA    string `json:"sha,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// IssueController abstracts GitHub Issue & PR mutations for Temporal activities.
type IssueController interface {
	TransitionIssueState(ctx context.Context, req IssueTransition) error
	OpenPullRequest(ctx context.Context, req PRRequest) (PRResult, error)
	AwaitChecksAndMerge(ctx context.Context, req MergeRequest, heartbeat func(string)) (MergeResult, error)
}

// PrepareWorktreeInput identifies the issue and site slug for worktree setup.
type PrepareWorktreeInput struct {
	IssueNumber int    `json:"issue_number"`
	SiteSlug    string `json:"site_slug"`
}

// DeveloperActivityInput wraps DeveloperTaskInput with its isolated worktree path.
type DeveloperActivityInput struct {
	WorktreeDir string                       `json:"worktree_dir"`
	Task        contracts.DeveloperTaskInput `json:"task"`
}

// TesterActivityInput wraps TestRunInput with its isolated worktree path.
type TesterActivityInput struct {
	WorktreeDir string                 `json:"worktree_dir"`
	Input       contracts.TestRunInput `json:"input"`
}

// ReviewerActivityInput wraps ReviewInput with its isolated worktree path.
type ReviewerActivityInput struct {
	WorktreeDir string                `json:"worktree_dir"`
	Input       contracts.ReviewInput `json:"input"`
}

// PublishPRInput bundles all artifacts needed to commit, push, and open a PR.
type PublishPRInput struct {
	WorktreeDir  string                         `json:"worktree_dir"`
	IssueNumber  int                            `json:"issue_number"`
	IssueTitle   string                         `json:"issue_title"`
	Spec         contracts.WebsiteSpecification `json:"spec"`
	TestReport   contracts.TestReport           `json:"test_report"`
	AllReports   []contracts.TestReport         `json:"all_reports"`
	Review       contracts.ReviewResult         `json:"review"`
	Attempts     int                            `json:"attempts"`
	AutonomyMode config.AutonomyMode            `json:"autonomy_mode"`
}

// CleanupInput specifies how to clean up an issue's worktree on exit.
type CleanupInput struct {
	WorktreeDir string                 `json:"worktree_dir"`
	IssueNumber int                    `json:"issue_number"`
	SiteSlug    string                 `json:"site_slug"`
	Failed      bool                   `json:"failed"`
	Reports     []contracts.TestReport `json:"reports,omitempty"`
}

// FactoryActivities implements all Temporal activities for IssueFactoryWorkflow.
// Credentials remain encapsulated inside private client fields and never enter Temporal payloads.
type FactoryActivities struct {
	PM       *pm.Agent
	Dev      *dev.Agent
	Tester   *tester.Agent
	Reviewer *reviewer.Agent
	Git      *gitworktree.Manager
	GitHub   IssueController
}

// TransitionIssueStateActivity updates factory:* labels and comments on GitHub.
func (a *FactoryActivities) TransitionIssueStateActivity(ctx context.Context, req IssueTransition) error {
	if a.GitHub == nil {
		return fmt.Errorf("github controller not configured")
	}
	return a.GitHub.TransitionIssueState(ctx, req)
}

// RunPMAgentActivity classifies scope and produces WebsiteSpecification or clarification questions.
func (a *FactoryActivities) RunPMAgentActivity(ctx context.Context, issue contracts.CustomerIssueInput) (contracts.PMDecisionOutput, error) {
	if a.PM == nil {
		return contracts.PMDecisionOutput{}, fmt.Errorf("pm agent not configured")
	}
	stopHB := startHeartbeatLoop(ctx, fmt.Sprintf("pm analyze issue #%d", issue.IssueNumber))
	defer stopHB()
	return a.PM.AnalyzeIssue(ctx, issue)
}

// PrepareWorktreeActivity creates a clean isolated git worktree on factory/issue-<N>.
func (a *FactoryActivities) PrepareWorktreeActivity(ctx context.Context, in PrepareWorktreeInput) (string, error) {
	if a.Git == nil {
		return "", fmt.Errorf("git worktree manager not configured")
	}
	return a.Git.PrepareWorktree(ctx, in.IssueNumber, in.SiteSlug)
}

// RunDeveloperAgentActivity runs the bounded Developer tool loop inside the issue worktree.
func (a *FactoryActivities) RunDeveloperAgentActivity(ctx context.Context, in DeveloperActivityInput) (contracts.ImplementationResult, error) {
	if a.Dev == nil {
		return contracts.ImplementationResult{}, fmt.Errorf("developer agent not configured")
	}
	stopHB := startHeartbeatLoop(ctx, fmt.Sprintf("dev implement %s (attempt %d)", in.Task.Spec.SiteSlug, in.Task.Attempt))
	defer stopHB()
	siteDir := gitworktree.SiteDir(in.WorktreeDir, in.Task.Spec.SiteSlug)
	return a.Dev.Implement(ctx, in.Task, siteDir, func(detail string) {
		activity.RecordHeartbeat(ctx, detail)
	})
}

// RunTesterAgentActivity executes the independent Tester Agent with Playwright & DOM tools.
func (a *FactoryActivities) RunTesterAgentActivity(ctx context.Context, in TesterActivityInput) (contracts.TestReport, error) {
	if a.Tester == nil {
		return contracts.TestReport{}, fmt.Errorf("tester agent not configured")
	}
	stopHB := startHeartbeatLoop(ctx, fmt.Sprintf("tester verify %s (attempt %d)", in.Input.Spec.SiteSlug, in.Input.Attempt))
	defer stopHB()
	return a.Tester.Verify(ctx, in.Input, in.WorktreeDir, func(detail string) {
		activity.RecordHeartbeat(ctx, detail)
	})
}

// RunReviewerActivity executes path-isolation, URL scan, and PR composition via sec-agent.
func (a *FactoryActivities) RunReviewerActivity(ctx context.Context, in ReviewerActivityInput) (contracts.ReviewResult, error) {
	if a.Reviewer == nil {
		return contracts.ReviewResult{}, fmt.Errorf("reviewer agent not configured")
	}
	stopHB := startHeartbeatLoop(ctx, fmt.Sprintf("reviewing %s", in.Input.Spec.SiteSlug))
	defer stopHB()
	return a.Reviewer.Review(ctx, in.Input, in.WorktreeDir)
}

// PublishPullRequestActivity commits the site + evidence, pushes the branch, and opens a PR.
func (a *FactoryActivities) PublishPullRequestActivity(ctx context.Context, in PublishPRInput) (PRResult, error) {
	if a.Git == nil || a.GitHub == nil {
		return PRResult{}, fmt.Errorf("git or github adapter not configured")
	}
	sha, err := a.Git.CommitAndPush(ctx, in.WorktreeDir, in.Spec, in.IssueNumber)
	if err != nil {
		return PRResult{}, err
	}
	branch := gitworktree.BranchName(in.IssueNumber)
	return a.GitHub.OpenPullRequest(ctx, PRRequest{
		IssueNumber:  in.IssueNumber,
		IssueTitle:   strings.TrimSpace(in.IssueTitle),
		Branch:       branch,
		CommitSHA:    sha,
		Spec:         in.Spec,
		TestReport:   in.TestReport,
		AllReports:   in.AllReports,
		Review:       in.Review,
		Attempts:     in.Attempts,
		AutonomyMode: in.AutonomyMode,
	})
}

// AwaitChecksAndMergeActivity waits for PR CI checks (unless skipped) and squash-merges in auto mode.
func (a *FactoryActivities) AwaitChecksAndMergeActivity(ctx context.Context, req MergeRequest) (MergeResult, error) {
	if a.GitHub == nil {
		return MergeResult{}, fmt.Errorf("github controller not configured")
	}
	return a.GitHub.AwaitChecksAndMerge(ctx, req, func(detail string) {
		activity.RecordHeartbeat(ctx, detail)
	})
}

// CleanupWorktreeActivity snapshots failed artifacts (if Failed) and removes the worktree.
func (a *FactoryActivities) CleanupWorktreeActivity(ctx context.Context, in CleanupInput) error {
	if a.Git == nil {
		return fmt.Errorf("git worktree manager not configured")
	}
	return a.Git.CleanupWorktree(ctx, in.WorktreeDir, in.IssueNumber, in.SiteSlug, in.Failed, in.Reports)
}

func startHeartbeatLoop(ctx context.Context, detail string) func() {
	activity.RecordHeartbeat(ctx, detail)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, detail)
			}
		}
	}()
	return func() {
		close(done)
	}
}
