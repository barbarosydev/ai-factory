package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/barbarosydev/ai-factory/internal/activities"
	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/contracts"
)

func sampleSpec() *contracts.WebsiteSpecification {
	return &contracts.WebsiteSpecification{
		SchemaVersion: "v1",
		SiteSlug:      "kaffeekultur-berlin",
		Operation:     "create",
		Category:      "coffee-shop",
		PageTitle:     "Kaffeekultur Berlin",
		Tagline:       "Artisan Coffee",
		Theme: contracts.ThemeSpec{
			PrimaryColor:    "#4A2C2A",
			BackgroundColor: "#FAF7F2",
			AccentColor:     "#D4A373",
			FontPairing:     "system-sans",
		},
		Sections: []contracts.SectionSpec{
			{ID: "hero", Heading: "Kaffeekultur", Purpose: "Hero"},
			{ID: "menu", Heading: "Menu", Purpose: "Drinks"},
			{ID: "hours", Heading: "Hours", Purpose: "Hours"},
		},
		FooterText: "Kaffeekultur Berlin",
	}
}

func registerBaseActivities(env *testsuite.TestWorkflowEnvironment) *activities.FactoryActivities {
	var a *activities.FactoryActivities
	env.RegisterActivity(a.TransitionIssueStateActivity)
	env.RegisterActivity(a.RunPMAgentActivity)
	env.RegisterActivity(a.PrepareWorktreeActivity)
	env.RegisterActivity(a.RunDeveloperAgentActivity)
	env.RegisterActivity(a.RunTesterAgentActivity)
	env.RegisterActivity(a.RunReviewerActivity)
	env.RegisterActivity(a.PublishPullRequestActivity)
	env.RegisterActivity(a.AwaitChecksAndMergeActivity)
	env.RegisterActivity(a.CleanupWorktreeActivity)
	return a
}

func TestWorkflowHappyPathRepairAndAutoMerge(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := registerBaseActivities(env)

	env.OnActivity(a.TransitionIssueStateActivity, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.RunPMAgentActivity, mock.Anything, mock.Anything).Return(contracts.PMDecisionOutput{
		Status: "ready",
		Spec:   sampleSpec(),
	}, nil)
	env.OnActivity(a.PrepareWorktreeActivity, mock.Anything, mock.Anything).Return("/tmp/wt/issue-42", nil)

	var devAttempts atomic.Int32
	env.OnActivity(a.RunDeveloperAgentActivity, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.DeveloperActivityInput) (contracts.ImplementationResult, error) {
		devAttempts.Add(1)
		if in.Task.Attempt == 2 && len(in.Task.PriorFailures) == 0 {
			t.Error("expected Attempt 2 to receive Attempt 1 FailureEvidence")
		}
		return contracts.ImplementationResult{Attempt: in.Task.Attempt, FilesChanged: []string{"index.html", "styles.css"}}, nil
	})

	env.OnActivity(a.RunTesterAgentActivity, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.TesterActivityInput) (contracts.TestReport, error) {
		if in.Input.Attempt == 1 {
			return contracts.TestReport{
				Attempt: 1,
				Passed:  false,
				Failures: []contracts.FailureEvidence{{
					CheckName:   "responsive-mobile-overflow",
					Expected:    "<=375",
					Actual:      "440",
					Remediation: "Fix .hours-table width",
				}},
			}, nil
		}
		return contracts.TestReport{Attempt: 2, Passed: true, SemanticQualityOK: true}, nil
	})

	env.OnActivity(a.RunReviewerActivity, mock.Anything, mock.Anything).Return(contracts.ReviewResult{
		Approved:         true,
		PathIsolationOK:  true,
		ExternalAssetsOK: true,
		SpecFidelityOK:   true,
		SummaryMarkdown:  "Looks great.",
	}, nil)

	env.OnActivity(a.PublishPullRequestActivity, mock.Anything, mock.Anything).Return(activities.PRResult{
		Number: 7,
		URL:    "https://github.com/barbarosydev/ai-factory-websites/pull/7",
		Head:   "factory/issue-42",
		SHA:    "abc1234",
	}, nil)

	env.OnActivity(a.AwaitChecksAndMergeActivity, mock.Anything, mock.Anything).Return(activities.MergeResult{
		Merged: true,
		SHA:    "def5678",
	}, nil)

	var cleanupFailed bool
	env.OnActivity(a.CleanupWorktreeActivity, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.CleanupInput) error {
		cleanupFailed = in.Failed
		return nil
	})

	env.ExecuteWorkflow(IssueFactoryWorkflow, WorkflowInput{
		Issue: contracts.CustomerIssueInput{
			IssueNumber: 42,
			IssueTitle:  "Coffee shop",
			IssueBody:   "Build a coffee shop site",
			RequestType: "new_website",
		},
		AutonomyMode: config.AutonomyAuto,
	})

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow failed: %v", env.GetWorkflowError())
	}

	var res WorkflowResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}
	if res.FinalState != "done" || !res.Merged || res.Attempts != 2 || cleanupFailed {
		t.Fatalf("unexpected WorkflowResult=%+v cleanupFailed=%t", res, cleanupFailed)
	}
	if devAttempts.Load() != 2 {
		t.Errorf("expected 2 developer attempts, got %d", devAttempts.Load())
	}
}

func TestWorkflowHumanClarificationSignalResume(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := registerBaseActivities(env)

	env.OnActivity(a.TransitionIssueStateActivity, mock.Anything, mock.Anything).Return(nil)

	var pmCalls atomic.Int32
	env.OnActivity(a.RunPMAgentActivity, mock.Anything, mock.Anything).Return(func(_ context.Context, issue contracts.CustomerIssueInput) (contracts.PMDecisionOutput, error) {
		if pmCalls.Add(1) == 1 {
			return contracts.PMDecisionOutput{
				Status:                 "needs_clarification",
				ClarificationQuestions: []string{"Which Berlin neighborhood is the shop in?"},
			}, nil
		}
		if len(issue.Clarifications) != 1 || issue.Clarifications[0].Body != "Kreuzberg" {
			t.Errorf("expected clarification 'Kreuzberg', got %+v", issue.Clarifications)
		}
		return contracts.PMDecisionOutput{Status: "ready", Spec: sampleSpec()}, nil
	})

	env.OnActivity(a.PrepareWorktreeActivity, mock.Anything, mock.Anything).Return("/tmp/wt/issue-43", nil)
	env.OnActivity(a.RunDeveloperAgentActivity, mock.Anything, mock.Anything).Return(contracts.ImplementationResult{Attempt: 1}, nil)
	env.OnActivity(a.RunTesterAgentActivity, mock.Anything, mock.Anything).Return(contracts.TestReport{Attempt: 1, Passed: true, SemanticQualityOK: true}, nil)
	env.OnActivity(a.RunReviewerActivity, mock.Anything, mock.Anything).Return(contracts.ReviewResult{Approved: true, PathIsolationOK: true, ExternalAssetsOK: true}, nil)
	env.OnActivity(a.PublishPullRequestActivity, mock.Anything, mock.Anything).Return(activities.PRResult{Number: 8, URL: "https://example/pull/8"}, nil)
	env.OnActivity(a.CleanupWorktreeActivity, mock.Anything, mock.Anything).Return(nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalHumanReply, HumanReplySignal{
			CommentID: 99,
			Author:    "barbarosydev",
			Body:      "Kreuzberg",
		})
	}, 2*time.Hour)

	env.ExecuteWorkflow(IssueFactoryWorkflow, WorkflowInput{
		Issue:        contracts.CustomerIssueInput{IssueNumber: 43, IssueTitle: "Coffee", IssueBody: "Ambiguous"},
		AutonomyMode: config.AutonomyReview,
	})

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow failed: %v", env.GetWorkflowError())
	}
	var res WorkflowResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}
	if res.FinalState != "review" || res.Merged || res.PRURL == "" {
		t.Fatalf("unexpected WorkflowResult: %+v", res)
	}
}

func TestWorkflowRejectionAndRepairExhaustion(t *testing.T) {
	t.Parallel()

	// 1. Out-of-scope rejection
	var suite1 testsuite.WorkflowTestSuite
	env1 := suite1.NewTestWorkflowEnvironment()
	a1 := registerBaseActivities(env1)
	env1.OnActivity(a1.TransitionIssueStateActivity, mock.Anything, mock.Anything).Return(nil)
	env1.OnActivity(a1.RunPMAgentActivity, mock.Anything, mock.Anything).Return(contracts.PMDecisionOutput{
		Status:          "rejected",
		RejectionReason: "Stripe checkout and login accounts are out of scope for v1.",
	}, nil)

	env1.ExecuteWorkflow(IssueFactoryWorkflow, WorkflowInput{
		Issue:        contracts.CustomerIssueInput{IssueNumber: 44, IssueTitle: "Shop", IssueBody: "Add Stripe"},
		AutonomyMode: config.AutonomyReview,
	})
	var res1 WorkflowResult
	_ = env1.GetWorkflowResult(&res1)
	if res1.FinalState != "rejected" {
		t.Fatalf("expected rejected, got %+v", res1)
	}

	// 2. Bounded 3-attempt exhaustion -> factory:failed + CleanupWorktree(Failed=true)
	var suite2 testsuite.WorkflowTestSuite
	env2 := suite2.NewTestWorkflowEnvironment()
	a2 := registerBaseActivities(env2)
	env2.OnActivity(a2.TransitionIssueStateActivity, mock.Anything, mock.Anything).Return(nil)
	env2.OnActivity(a2.RunPMAgentActivity, mock.Anything, mock.Anything).Return(contracts.PMDecisionOutput{Status: "ready", Spec: sampleSpec()}, nil)
	env2.OnActivity(a2.PrepareWorktreeActivity, mock.Anything, mock.Anything).Return("/tmp/wt/issue-45", nil)
	env2.OnActivity(a2.RunDeveloperAgentActivity, mock.Anything, mock.Anything).Return(contracts.ImplementationResult{Attempt: 1}, nil)
	env2.OnActivity(a2.RunTesterAgentActivity, mock.Anything, mock.Anything).Return(contracts.TestReport{
		Attempt:  1,
		Passed:   false,
		Failures: []contracts.FailureEvidence{{CheckName: "missing-section"}},
	}, nil)

	var failedArchiveCalled bool
	env2.OnActivity(a2.CleanupWorktreeActivity, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.CleanupInput) error {
		failedArchiveCalled = in.Failed
		return nil
	})

	env2.ExecuteWorkflow(IssueFactoryWorkflow, WorkflowInput{
		Issue:        contracts.CustomerIssueInput{IssueNumber: 45, IssueTitle: "Broken", IssueBody: "Broken"},
		AutonomyMode: config.AutonomyReview,
	})
	var res2 WorkflowResult
	_ = env2.GetWorkflowResult(&res2)
	if res2.FinalState != "failed" || !failedArchiveCalled || res2.Attempts != 3 {
		t.Fatalf("expected failed after 3 attempts with archive, got res=%+v failedArchiveCalled=%t", res2, failedArchiveCalled)
	}

	// 3. Custom configurable MaxRepairAttempts = 1
	var suite3 testsuite.WorkflowTestSuite
	env3 := suite3.NewTestWorkflowEnvironment()
	a3 := registerBaseActivities(env3)
	env3.OnActivity(a3.TransitionIssueStateActivity, mock.Anything, mock.Anything).Return(nil)
	env3.OnActivity(a3.RunPMAgentActivity, mock.Anything, mock.Anything).Return(contracts.PMDecisionOutput{Status: "ready", Spec: sampleSpec()}, nil)
	env3.OnActivity(a3.PrepareWorktreeActivity, mock.Anything, mock.Anything).Return("/tmp/wt/issue-46", nil)
	env3.OnActivity(a3.RunDeveloperAgentActivity, mock.Anything, mock.Anything).Return(contracts.ImplementationResult{Attempt: 1}, nil)
	env3.OnActivity(a3.RunTesterAgentActivity, mock.Anything, mock.Anything).Return(contracts.TestReport{
		Attempt:  1,
		Passed:   false,
		Failures: []contracts.FailureEvidence{{CheckName: "missing-section"}},
	}, nil)
	env3.OnActivity(a3.CleanupWorktreeActivity, mock.Anything, mock.Anything).Return(nil)

	policy := config.DefaultWorkflowPolicy()
	policy.MaxRepairAttempts = 1
	env3.ExecuteWorkflow(IssueFactoryWorkflow, WorkflowInput{
		Issue:        contracts.CustomerIssueInput{IssueNumber: 46, IssueTitle: "Broken", IssueBody: "Broken"},
		AutonomyMode: config.AutonomyReview,
		Policy:       policy,
	})
	var res3 WorkflowResult
	_ = env3.GetWorkflowResult(&res3)
	if res3.FinalState != "failed" || res3.Attempts != 1 {
		t.Fatalf("expected failed after custom 1 repair attempt, got %+v", res3)
	}
}
