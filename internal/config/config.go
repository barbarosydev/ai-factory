package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AutonomyMode controls whether a verified PR awaits human merge or auto-merges.
type AutonomyMode string

const (
	AutonomyReview AutonomyMode = "review"
	AutonomyAuto   AutonomyMode = "auto"
)

// WorkflowPolicy configures bounded repair loops, human-in-the-loop wait timeouts,
// background heartbeats, and Temporal RetryPolicy limits.
type WorkflowPolicy struct {
	MaxRepairAttempts      int           `json:"max_repair_attempts"`
	MaxClarificationRounds int           `json:"max_clarification_rounds"`
	HumanWaitTimeout       time.Duration `json:"human_wait_timeout"`
	HeartbeatInterval      time.Duration `json:"heartbeat_interval"`
	HeartbeatTimeout       time.Duration `json:"heartbeat_timeout"`
	GitHubTimeout          time.Duration `json:"github_timeout"`
	PMTimeout              time.Duration `json:"pm_timeout"`
	DevTimeout             time.Duration `json:"dev_timeout"`
	TestTimeout            time.Duration `json:"test_timeout"`
	ReviewTimeout          time.Duration `json:"review_timeout"`
	MergeTimeout           time.Duration `json:"merge_timeout"`
	AgentMaxRetryAttempts  int           `json:"agent_max_retry_attempts"`
	GitHubMaxRetryAttempts int           `json:"github_max_retry_attempts"`
}

// DefaultWorkflowPolicy returns the default Temporal execution policy tuned for local Apple Silicon inference.
func DefaultWorkflowPolicy() WorkflowPolicy {
	return WorkflowPolicy{
		MaxRepairAttempts:      3,
		MaxClarificationRounds: 2,
		HumanWaitTimeout:       7 * 24 * time.Hour,
		HeartbeatInterval:      15 * time.Second,
		HeartbeatTimeout:       15 * time.Minute,
		GitHubTimeout:          5 * time.Minute,
		PMTimeout:              45 * time.Minute,
		DevTimeout:             60 * time.Minute,
		TestTimeout:            30 * time.Minute,
		ReviewTimeout:          30 * time.Minute,
		MergeTimeout:           30 * time.Minute,
		AgentMaxRetryAttempts:  2,
		GitHubMaxRetryAttempts: 4,
	}
}

// WithDefaults fills any zero-valued policy fields with production defaults so workflows
// remain backward-compatible across callers and replay histories.
func (p WorkflowPolicy) WithDefaults() WorkflowPolicy {
	def := DefaultWorkflowPolicy()
	if p.MaxRepairAttempts <= 0 {
		p.MaxRepairAttempts = def.MaxRepairAttempts
	}
	if p.MaxClarificationRounds <= 0 {
		p.MaxClarificationRounds = def.MaxClarificationRounds
	}
	if p.HumanWaitTimeout <= 0 {
		p.HumanWaitTimeout = def.HumanWaitTimeout
	}
	if p.HeartbeatInterval <= 0 {
		p.HeartbeatInterval = def.HeartbeatInterval
	}
	if p.HeartbeatTimeout <= 0 {
		p.HeartbeatTimeout = def.HeartbeatTimeout
	}
	if p.GitHubTimeout <= 0 {
		p.GitHubTimeout = def.GitHubTimeout
	}
	if p.PMTimeout <= 0 {
		p.PMTimeout = def.PMTimeout
	}
	if p.DevTimeout <= 0 {
		p.DevTimeout = def.DevTimeout
	}
	if p.TestTimeout <= 0 {
		p.TestTimeout = def.TestTimeout
	}
	if p.ReviewTimeout <= 0 {
		p.ReviewTimeout = def.ReviewTimeout
	}
	if p.MergeTimeout <= 0 {
		p.MergeTimeout = def.MergeTimeout
	}
	if p.AgentMaxRetryAttempts <= 0 {
		p.AgentMaxRetryAttempts = def.AgentMaxRetryAttempts
	}
	if p.GitHubMaxRetryAttempts <= 0 {
		p.GitHubMaxRetryAttempts = def.GitHubMaxRetryAttempts
	}
	return p
}

// Config holds runtime configuration for the AI Software Factory control plane.
// Secret values (LiteLLM virtual keys and GitHub App PEM) are read solely from
// process environment variables injected at runtime and are never written to disk
// or serialized into Temporal workflow histories.
type Config struct {
	TemporalAddress         string
	TemporalNamespace       string
	TaskQueue               string
	LiteLLMBaseURL          string
	WebsitesRepoPath        string
	GitHubOwner             string
	GitHubRepo              string
	AllowedGitHubUsers      []string
	GitHubAppID             int64
	GitHubInstallationID    int64
	AutonomyMode            AutonomyMode
	SkipRemoteCI            bool
	PollIntervalSeconds     int
	NumCtx                  int
	MaxConcurrentActivities int
	MetricsAddr             string
	LogFormat               string
	Policy                  WorkflowPolicy
	PMKey                   string
	DevKey                  string
	TestKey                 string
	SecKey                  string
	GitHubAppPEMBase64      string
}

// LoadFromEnv loads factory configuration from environment variables with local defaults.
func LoadFromEnv() (Config, error) {
	defPolicy := DefaultWorkflowPolicy()
	cfg := Config{
		TemporalAddress:         envOr("FACTORY_TEMPORAL_ADDRESS", "temporal.localhost:7233"),
		TemporalNamespace:       envOr("FACTORY_TEMPORAL_NAMESPACE", "default"),
		TaskQueue:               envOr("FACTORY_TASK_QUEUE", "ai-factory-v1"),
		LiteLLMBaseURL:          strings.TrimRight(envOr("FACTORY_LITELLM_URL", "http://litellm.localhost:8080/v1"), "/"),
		WebsitesRepoPath:        envOr("FACTORY_WEBSITES_REPO", "../ai-factory-websites"),
		GitHubOwner:             envOr("FACTORY_GITHUB_OWNER", "barbarosydev"),
		GitHubRepo:              envOr("FACTORY_GITHUB_REPO", "ai-factory-websites"),
		AllowedGitHubUsers:      parseCSVAllowlist(envOr("FACTORY_ALLOWED_GITHUB_USERS", "barbarosydev")),
		AutonomyMode:            AutonomyMode(envOr("FACTORY_AUTONOMY_MODE", string(AutonomyReview))),
		SkipRemoteCI:            os.Getenv("FACTORY_SKIP_REMOTE_CI") == "1" || strings.EqualFold(os.Getenv("FACTORY_SKIP_REMOTE_CI"), "true"),
		PollIntervalSeconds:     envIntOr("FACTORY_POLL_INTERVAL_SECONDS", 15),
		NumCtx:                  envIntOr("FACTORY_NUM_CTX", 32768),
		MaxConcurrentActivities: envIntOr("FACTORY_MAX_CONCURRENT_ACTIVITIES", 1),
		MetricsAddr:             envOr("FACTORY_METRICS_ADDR", "127.0.0.1:9091"),
		LogFormat:               strings.ToLower(envOr("FACTORY_LOG_FORMAT", "text")),
		Policy:                  defPolicy,
		PMKey:                   os.Getenv("LITELLM_PM_KEY"),
		DevKey:                  os.Getenv("LITELLM_DEV_KEY"),
		TestKey:                 os.Getenv("LITELLM_TEST_KEY"),
		SecKey:                  os.Getenv("LITELLM_SEC_KEY"),
		GitHubAppPEMBase64:      os.Getenv("GITHUB_APP_PRIVATE_KEY_BASE64"),
	}

	var err error
	if cfg.Policy.MaxRepairAttempts, err = envPositiveInt("FACTORY_MAX_REPAIR_ATTEMPTS", defPolicy.MaxRepairAttempts); err != nil {
		return Config{}, err
	}
	if cfg.Policy.MaxClarificationRounds, err = envPositiveInt("FACTORY_MAX_CLARIFICATION_ROUNDS", defPolicy.MaxClarificationRounds); err != nil {
		return Config{}, err
	}
	if cfg.Policy.AgentMaxRetryAttempts, err = envPositiveInt("FACTORY_AGENT_MAX_RETRIES", defPolicy.AgentMaxRetryAttempts); err != nil {
		return Config{}, err
	}
	if cfg.Policy.GitHubMaxRetryAttempts, err = envPositiveInt("FACTORY_GITHUB_MAX_RETRIES", defPolicy.GitHubMaxRetryAttempts); err != nil {
		return Config{}, err
	}
	if cfg.Policy.HumanWaitTimeout, err = envDuration("FACTORY_HUMAN_WAIT_TIMEOUT", defPolicy.HumanWaitTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Policy.HeartbeatInterval, err = envDuration("FACTORY_HEARTBEAT_INTERVAL", defPolicy.HeartbeatInterval); err != nil {
		return Config{}, err
	}
	if cfg.Policy.HeartbeatTimeout, err = envDuration("FACTORY_HEARTBEAT_TIMEOUT", defPolicy.HeartbeatTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Policy.GitHubTimeout, err = envDuration("FACTORY_GITHUB_TIMEOUT", defPolicy.GitHubTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Policy.PMTimeout, err = envDuration("FACTORY_PM_TIMEOUT", defPolicy.PMTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Policy.DevTimeout, err = envDuration("FACTORY_DEV_TIMEOUT", defPolicy.DevTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Policy.TestTimeout, err = envDuration("FACTORY_TEST_TIMEOUT", defPolicy.TestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Policy.ReviewTimeout, err = envDuration("FACTORY_REVIEW_TIMEOUT", defPolicy.ReviewTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Policy.MergeTimeout, err = envDuration("FACTORY_MERGE_TIMEOUT", defPolicy.MergeTimeout); err != nil {
		return Config{}, err
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
	if cfg.MaxConcurrentActivities < 1 {
		return Config{}, errors.New("FACTORY_MAX_CONCURRENT_ACTIVITIES must be >= 1")
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return Config{}, fmt.Errorf("invalid FACTORY_LOG_FORMAT %q: must be text or json", cfg.LogFormat)
	}
	if len(cfg.AllowedGitHubUsers) == 0 {
		return Config{}, errors.New("FACTORY_ALLOWED_GITHUB_USERS must contain at least one GitHub username")
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

func envPositiveInt(key string, fallback int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid %s=%q: must be an integer >= 1", key, v)
	}
	return n, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid %s=%q: must be a positive duration", key, v)
	}
	return d, nil
}

func parseCSVAllowlist(raw string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		u := strings.ToLower(strings.TrimSpace(part))
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}
