package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadFromEnvDefaultsAndOverrides(t *testing.T) {
	t.Setenv("FACTORY_AUTONOMY_MODE", "auto")
	t.Setenv("FACTORY_MAX_REPAIR_ATTEMPTS", "5")
	t.Setenv("FACTORY_MAX_CLARIFICATION_ROUNDS", "3")
	t.Setenv("FACTORY_HUMAN_WAIT_TIMEOUT", "24h")
	t.Setenv("FACTORY_HEARTBEAT_INTERVAL", "10s")
	t.Setenv("FACTORY_PM_TIMEOUT", "20m")
	t.Setenv("FACTORY_LOG_FORMAT", "JSON")
	t.Setenv("FACTORY_METRICS_ADDR", ":9091")
	t.Setenv("FACTORY_MAX_CONCURRENT_ACTIVITIES", "2")
	t.Setenv("FACTORY_ALLOWED_GITHUB_USERS", " BarbarosYdev , alice ")
	t.Setenv("FACTORY_AGENT_RUNTIME", "GO")

	cfg, err := LoadFromEnv()
	require.NoError(t, err)
	require.Equal(t, AutonomyAuto, cfg.AutonomyMode)
	require.Equal(t, "go", cfg.AgentRuntime)
	require.Equal(t, "ai-factory-agents", cfg.AgentTaskQueue)
	require.Equal(t, []string{"barbarosydev", "alice"}, cfg.AllowedGitHubUsers)
	require.Equal(t, 5, cfg.Policy.MaxRepairAttempts)
	require.Equal(t, 3, cfg.Policy.MaxClarificationRounds)
	require.Equal(t, 24*time.Hour, cfg.Policy.HumanWaitTimeout)
	require.Equal(t, 10*time.Second, cfg.Policy.HeartbeatInterval)
	require.Equal(t, 20*time.Minute, cfg.Policy.PMTimeout)
	require.Equal(t, "json", cfg.LogFormat)
	require.Equal(t, ":9091", cfg.MetricsAddr)
	require.Equal(t, 2, cfg.MaxConcurrentActivities)
}

func TestLoadFromEnvRejectsInvalidPolicyValues(t *testing.T) {
	t.Setenv("FACTORY_MAX_REPAIR_ATTEMPTS", "0")
	_, err := LoadFromEnv()
	require.Error(t, err)
	require.Contains(t, err.Error(), "FACTORY_MAX_REPAIR_ATTEMPTS")

	t.Setenv("FACTORY_MAX_REPAIR_ATTEMPTS", "3")
	t.Setenv("FACTORY_PM_TIMEOUT", "not-a-duration")
	_, err = LoadFromEnv()
	require.Error(t, err)
	require.Contains(t, err.Error(), "FACTORY_PM_TIMEOUT")

	t.Setenv("FACTORY_PM_TIMEOUT", "")
	t.Setenv("FACTORY_LOG_FORMAT", "xml")
	_, err = LoadFromEnv()
	require.Error(t, err)
	require.Contains(t, err.Error(), "FACTORY_LOG_FORMAT")
}

func TestValidateRuntimeSecrets(t *testing.T) {
	cfg := Config{
		PMKey:   "pm",
		DevKey:  "dev",
		TestKey: "test",
		SecKey:  "sec",
	}
	require.NoError(t, cfg.ValidateRuntimeSecrets(false))
	require.Error(t, cfg.ValidateRuntimeSecrets(true))

	cfg.GitHubAppID = 123
	cfg.GitHubInstallationID = 456
	cfg.GitHubAppPEMBase64 = "cGVt"
	require.NoError(t, cfg.ValidateRuntimeSecrets(true))
}
