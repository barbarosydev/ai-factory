package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AutonomyMode controls whether a verified PR awaits human merge or auto-merges.
type AutonomyMode string

const (
	AutonomyReview AutonomyMode = "review"
	AutonomyAuto   AutonomyMode = "auto"
)

// Config holds runtime configuration for the AI Software Factory control plane.
// Secret values (LiteLLM virtual keys and GitHub App PEM) are read solely from
// process environment variables injected at runtime and are never written to disk
// or serialized into Temporal workflow histories.
type Config struct {
	TemporalAddress      string
	TemporalNamespace    string
	TaskQueue            string
	LiteLLMBaseURL       string
	WebsitesRepoPath     string
	GitHubOwner          string
	GitHubRepo           string
	GitHubAppID          int64
	GitHubInstallationID int64
	AutonomyMode         AutonomyMode
	SkipRemoteCI         bool
	PollIntervalSeconds  int
	NumCtx               int
	PMKey                string
	DevKey               string
	TestKey              string
	SecKey               string
	GitHubAppPEMBase64   string
}

// LoadFromEnv loads factory configuration from environment variables with local defaults.
func LoadFromEnv() (Config, error) {
	cfg := Config{
		TemporalAddress:     envOr("FACTORY_TEMPORAL_ADDRESS", "temporal.localhost:7233"),
		TemporalNamespace:   envOr("FACTORY_TEMPORAL_NAMESPACE", "default"),
		TaskQueue:           envOr("FACTORY_TASK_QUEUE", "ai-factory-v1"),
		LiteLLMBaseURL:      strings.TrimRight(envOr("FACTORY_LITELLM_URL", "http://litellm.localhost:8080/v1"), "/"),
		WebsitesRepoPath:    envOr("FACTORY_WEBSITES_REPO", "../ai-factory-websites"),
		GitHubOwner:         envOr("FACTORY_GITHUB_OWNER", "barbarosydev"),
		GitHubRepo:          envOr("FACTORY_GITHUB_REPO", "ai-factory-websites"),
		AutonomyMode:        AutonomyMode(envOr("FACTORY_AUTONOMY_MODE", string(AutonomyReview))),
		SkipRemoteCI:        os.Getenv("FACTORY_SKIP_REMOTE_CI") == "1" || strings.EqualFold(os.Getenv("FACTORY_SKIP_REMOTE_CI"), "true"),
		PollIntervalSeconds: envIntOr("FACTORY_POLL_INTERVAL_SECONDS", 15),
		NumCtx:              envIntOr("FACTORY_NUM_CTX", 32768),
		PMKey:               os.Getenv("LITELLM_PM_KEY"),
		DevKey:              os.Getenv("LITELLM_DEV_KEY"),
		TestKey:             os.Getenv("LITELLM_TEST_KEY"),
		SecKey:              os.Getenv("LITELLM_SEC_KEY"),
		GitHubAppPEMBase64:  os.Getenv("GITHUB_APP_PRIVATE_KEY_BASE64"),
	}

	if appID := strings.TrimSpace(os.Getenv("GITHUB_APP_ID")); appID != "" {
		v, err := strconv.ParseInt(appID, 10, 64)
		if err != nil || v <= 0 {
			return Config{}, fmt.Errorf("invalid GITHUB_APP_ID: %w", err)
		}
		cfg.GitHubAppID = v
	}
	if instID := strings.TrimSpace(os.Getenv("GITHUB_INSTALLATION_ID")); instID != "" {
		v, err := strconv.ParseInt(instID, 10, 64)
		if err != nil || v <= 0 {
			return Config{}, fmt.Errorf("invalid GITHUB_INSTALLATION_ID: %w", err)
		}
		cfg.GitHubInstallationID = v
	}

	absPath, err := filepath.Abs(cfg.WebsitesRepoPath)
	if err == nil {
		cfg.WebsitesRepoPath = absPath
	}

	if cfg.AutonomyMode != AutonomyReview && cfg.AutonomyMode != AutonomyAuto {
		return Config{}, fmt.Errorf("invalid FACTORY_AUTONOMY_MODE %q: must be review or auto", cfg.AutonomyMode)
	}
	if cfg.PollIntervalSeconds < 5 {
		return Config{}, errors.New("FACTORY_POLL_INTERVAL_SECONDS must be >= 5")
	}
	return cfg, nil
}

// ValidateRuntimeSecrets ensures all runtime credentials required for live agent
// execution and GitHub App integration are present in process memory.
func (c Config) ValidateRuntimeSecrets(requireGitHub bool) error {
	var missing []string
	if strings.TrimSpace(c.PMKey) == "" {
		missing = append(missing, "LITELLM_PM_KEY")
	}
	if strings.TrimSpace(c.DevKey) == "" {
		missing = append(missing, "LITELLM_DEV_KEY")
	}
	if strings.TrimSpace(c.TestKey) == "" {
		missing = append(missing, "LITELLM_TEST_KEY")
	}
	if strings.TrimSpace(c.SecKey) == "" {
		missing = append(missing, "LITELLM_SEC_KEY")
	}
	if requireGitHub {
		if c.GitHubAppID <= 0 {
			missing = append(missing, "GITHUB_APP_ID")
		}
		if c.GitHubInstallationID <= 0 {
			missing = append(missing, "GITHUB_INSTALLATION_ID")
		}
		if strings.TrimSpace(c.GitHubAppPEMBase64) == "" {
			missing = append(missing, "GITHUB_APP_PRIVATE_KEY_BASE64")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required runtime credentials: %s", strings.Join(missing, ", "))
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
