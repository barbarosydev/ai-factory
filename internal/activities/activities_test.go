package activities

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/internal/telemetry"
)

func TestFactoryActivitiesNilGuards(t *testing.T) {
	t.Parallel()

	var acts FactoryActivities
	ctx := context.Background()

	if err := acts.TransitionIssueStateActivity(ctx, IssueTransition{IssueNumber: 1}); err == nil {
		t.Fatal("expected error when GitHub controller is nil")
	}
	if _, err := acts.RunPMAgentActivity(ctx, contracts.CustomerIssueInput{IssueNumber: 1}); err == nil {
		t.Fatal("expected error when PM agent is nil")
	}
	if _, err := acts.PrepareWorktreeActivity(ctx, PrepareWorktreeInput{IssueNumber: 1, SiteSlug: "cafe"}); err == nil {
		t.Fatal("expected error when Git manager is nil")
	}
	if _, err := acts.RunDeveloperAgentActivity(ctx, DeveloperActivityInput{}); err == nil {
		t.Fatal("expected error when Developer agent is nil")
	}
	if _, err := acts.RunTesterAgentActivity(ctx, TesterActivityInput{}); err == nil {
		t.Fatal("expected error when Tester agent is nil")
	}
	if _, err := acts.RunReviewerActivity(ctx, ReviewerActivityInput{}); err == nil {
		t.Fatal("expected error when Reviewer agent is nil")
	}
	if _, err := acts.PublishPullRequestActivity(ctx, PublishPRInput{}); err == nil {
		t.Fatal("expected error when Git/GitHub adapters are nil")
	}
	if _, err := acts.AwaitChecksAndMergeActivity(ctx, MergeRequest{}); err == nil {
		t.Fatal("expected error when GitHub controller is nil")
	}
	if err := acts.CleanupWorktreeActivity(ctx, CleanupInput{}); err == nil {
		t.Fatal("expected error when Git manager is nil")
	}
}

func TestRegistryRecordsPlaywrightMetric(t *testing.T) {
	t.Parallel()

	reg := telemetry.NewRegistry()
	reg.RecordPlaywrightRun(true, 1250)
	prom := reg.RenderPrometheus()
	if !strings.Contains(prom, `ai_factory_playwright_runs_total{passed="true"} 1`) {
		t.Fatalf("expected Playwright pass metric in Prometheus output, got:\n%s", prom)
	}
	_ = time.Second
}
