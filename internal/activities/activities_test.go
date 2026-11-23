package activities

import (
	"context"
	"testing"
)

func TestFactoryActivitiesControlPlaneNilGuards(t *testing.T) {
	t.Parallel()

	var acts FactoryActivities
	ctx := context.Background()

	if err := acts.TransitionIssueStateActivity(ctx, IssueTransition{IssueNumber: 1}); err == nil {
		t.Fatal("expected error when GitHub controller is nil")
	}
	if _, err := acts.PrepareWorktreeActivity(ctx, PrepareWorktreeInput{IssueNumber: 1, SiteSlug: "cafe"}); err == nil {
		t.Fatal("expected error when Git manager is nil")
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
