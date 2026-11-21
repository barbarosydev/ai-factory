package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/barbarosydev/ai-factory/internal/config"
)

// CheckResult represents one diagnostic probe in factory doctor.
type CheckResult struct {
	Name   string
	Status string // "PASS" | "WARN" | "FAIL"
	Detail string
}

// RunDoctor executes connectivity, tool, repository, and GitHub App diagnostics.
func RunDoctor(ctx context.Context, cfg config.Config, ollamaBaseURL string, strictGitHub bool, out io.Writer) bool {
	if strings.TrimSpace(ollamaBaseURL) == "" {
		ollamaBaseURL = "http://localhost:11434"
	}

	results := []CheckResult{
		checkWebsitesRepo(cfg.WebsitesRepoPath),
		checkTemporal(ctx, cfg.TemporalAddress, cfg.TemporalNamespace),
		checkLiteLLM(ctx, cfg.LiteLLMBaseURL, map[string]string{
			"pm-agent":   cfg.PMKey,
			"dev-agent":  cfg.DevKey,
			"test-agent": cfg.TestKey,
			"sec-agent":  cfg.SecKey,
		}),
		checkOllama(ctx, ollamaBaseURL),
		checkGitHubApp(ctx, cfg, strictGitHub),
	}

	ok := true
	for _, r := range results {
		_, _ = fmt.Fprintf(out, "[%s] %-16s %s\n", r.Status, r.Name, r.Detail)
		if r.Status == "FAIL" {
			ok = false
		}
	}
	return ok
}

func checkWebsitesRepo(repoPath string) CheckResult {
	required := []string{
		".git",
		"tests/site.spec.mjs",
		"node_modules/@playwright/test",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(repoPath, rel)); err != nil {
			return CheckResult{
				Name:   "websites-repo",
				Status: "FAIL",
				Detail: fmt.Sprintf("%s missing (%s): ensure repo is cloned and npm ci is complete", rel, repoPath),
			}
		}
	}
	return CheckResult{
		Name:   "websites-repo",
		Status: "PASS",
		Detail: fmt.Sprintf("ready at %s (Playwright installed)", repoPath),
	}
}

func checkTemporal(ctx context.Context, address, namespace string) CheckResult {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	c, err := client.Dial(client.Options{
		HostPort:  address,
		Namespace: namespace,
	})
	if err != nil {
		return CheckResult{
			Name:   "temporal",
			Status: "FAIL",
			Detail: fmt.Sprintf("dial %s: %v", address, err),
		}
	}
	defer c.Close()

	if _, err := c.CheckHealth(dialCtx, &client.CheckHealthRequest{}); err != nil {
		return CheckResult{
			Name:   "temporal",
			Status: "FAIL",
			Detail: fmt.Sprintf("health check failed at %s: %v", address, err),
		}
	}
	return CheckResult{
		Name:   "temporal",
		Status: "PASS",
		Detail: fmt.Sprintf("gRPC healthy at %s (namespace=%s)", address, namespace),
	}
}

func checkLiteLLM(ctx context.Context, baseURL string, agentKeys map[string]string) CheckResult {
	routes := []string{"pm-agent", "dev-agent", "test-agent", "sec-agent"}
	hc := &http.Client{Timeout: 5 * time.Second}
	var verified []string

	for _, route := range routes {
		apiKey := strings.TrimSpace(agentKeys[route])
		if apiKey == "" {
			return CheckResult{
				Name:   "litellm",
				Status: "FAIL",
				Detail: fmt.Sprintf("missing virtual key for route %q (ensure ai-factory-infra is running)", route),
			}
		}

		ids, status, err := fetchLiteLLMModels(ctx, hc, baseURL, apiKey)
		if err != nil {
			return CheckResult{Name: "litellm", Status: "FAIL", Detail: fmt.Sprintf("GET %s/models (%s): %v", baseURL, route, err)}
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return CheckResult{
				Name:   "litellm",
				Status: "FAIL",
				Detail: fmt.Sprintf("virtual key for %s rejected (HTTP %d) at %s", route, status, baseURL),
			}
		}
		if status < 200 || status >= 300 {
			return CheckResult{Name: "litellm", Status: "FAIL", Detail: fmt.Sprintf("unexpected HTTP %d from %s/models (%s)", status, baseURL, route)}
		}
		if !slices.Contains(ids, route) {
			return CheckResult{
				Name:   "litellm",
				Status: "FAIL",
				Detail: fmt.Sprintf("route %q not authorized for its virtual key (got %v)", route, ids),
			}
		}
		verified = append(verified, route)
	}

	return CheckResult{
		Name:   "litellm",
		Status: "PASS",
		Detail: fmt.Sprintf("gateway up at %s (scoped routes: %s)", baseURL, strings.Join(verified, ", ")),
	}
}

func fetchLiteLLMModels(ctx context.Context, hc *http.Client, baseURL, apiKey string) ([]string, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, nil
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode /models: %w", err)
	}
	var ids []string
	for _, m := range parsed.Data {
		ids = append(ids, m.ID)
	}
	return ids, resp.StatusCode, nil
}

func checkOllama(ctx context.Context, ollamaBaseURL string) CheckResult {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	hc := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(ollamaBaseURL, "/")+"/api/tags", nil)
	if err != nil {
		return CheckResult{Name: "ollama", Status: "FAIL", Detail: err.Error()}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return CheckResult{Name: "ollama", Status: "FAIL", Detail: fmt.Sprintf("unreachable at %s: %v", ollamaBaseURL, err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CheckResult{Name: "ollama", Status: "FAIL", Detail: fmt.Sprintf("HTTP %d from %s/api/tags", resp.StatusCode, ollamaBaseURL)}
	}

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tags); err != nil {
		return CheckResult{Name: "ollama", Status: "FAIL", Detail: fmt.Sprintf("decode /api/tags: %v", err)}
	}
	var names []string
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	for _, want := range []string{"gemma4:12b", "gemma4:e4b"} {
		if !slices.Contains(names, want) {
			return CheckResult{
				Name:   "ollama",
				Status: "FAIL",
				Detail: fmt.Sprintf("required model %q missing from %s (available: %v)", want, ollamaBaseURL, names),
			}
		}
	}
	return CheckResult{
		Name:   "ollama",
		Status: "PASS",
		Detail: fmt.Sprintf("gemma4:12b and gemma4:e4b installed at %s", ollamaBaseURL),
	}
}

func checkGitHubApp(ctx context.Context, cfg config.Config, strict bool) CheckResult {
	statusMissing := "WARN"
	if strict {
		statusMissing = "FAIL"
	}
	if cfg.GitHubAppID <= 0 || cfg.GitHubInstallationID <= 0 || strings.TrimSpace(cfg.GitHubAppPEMBase64) == "" {
		return CheckResult{
			Name:   "github-app",
			Status: statusMissing,
			Detail: "GITHUB_APP_ID, GITHUB_INSTALLATION_ID, or Keychain PEM not configured yet (see docs/github-app-setup.md)",
		}
	}

	ghClient, _, err := newGitHubClient(cfg, nil)
	if err != nil {
		return CheckResult{Name: "github-app", Status: "FAIL", Detail: err.Error()}
	}

	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ghClient.CheckInstallationAccess(checkCtx); err != nil {
		return CheckResult{Name: "github-app", Status: "FAIL", Detail: err.Error()}
	}
	return CheckResult{
		Name:   "github-app",
		Status: "PASS",
		Detail: fmt.Sprintf("installation token minted and %s/%s accessible", cfg.GitHubOwner, cfg.GitHubRepo),
	}
}
