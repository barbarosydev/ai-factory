package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/barbarosydev/ai-factory/internal/config"
)

func TestParseRunOptionsDefaultsAndFlags(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		AutonomyMode: config.AutonomyReview,
		SkipRemoteCI: false,
	}

	opts := parseRunOptions("run", cfg, []string{"--mode", "auto", "--skip-remote-ci", "--issue", "42"}, false)
	require.Equal(t, config.AutonomyAuto, opts.AutonomyMode)
	require.True(t, opts.SkipRemoteCI)
	require.False(t, opts.Once)
	require.Equal(t, 42, opts.IssueNumber)

	onceOpts := parseRunOptions("once", cfg, []string{"--issue", "9"}, true)
	require.Equal(t, config.AutonomyReview, onceOpts.AutonomyMode)
	require.True(t, onceOpts.Once)
	require.Equal(t, 9, onceOpts.IssueNumber)
}
