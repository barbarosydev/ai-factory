package controlplane

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"

	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/internal/workflow"
)

func TestNewWorkflowReceivesConfiguredAgentQueue(t *testing.T) {
	t.Setenv("FACTORY_AGENT_TASK_QUEUE", "custom-agent-queue")
	cfg, err := config.LoadFromEnv()
	require.NoError(t, err)

	tc := mocks.NewClient(t)
	run := mocks.NewWorkflowRun(t)
	tc.On("DescribeWorkflow", mock.Anything, "factory-issue-42", "").
		Return(nil, serviceerror.NewNotFound("not found"))
	tc.On("ExecuteWorkflow", mock.Anything, mock.MatchedBy(func(opts client.StartWorkflowOptions) bool {
		return opts.TaskQueue == cfg.TaskQueue
	}), mock.Anything, mock.MatchedBy(func(in workflow.WorkflowInput) bool {
		return in.AgentTaskQueue == "custom-agent-queue"
	})).Return(run, nil)
	run.On("GetRunID").Return("run-42")

	id, err := ensureStartedOrSignaled(context.Background(), tc, cfg,
		contracts.CustomerIssueInput{IssueNumber: 42}, RunOptions{}, nil, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "run-42", id)
}
