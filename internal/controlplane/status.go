package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/workflow"
)

// PrintStatus renders active Temporal workflows, loaded Ollama models, and open factory:* issues.
func PrintStatus(ctx context.Context, cfg config.Config, ollamaBaseURL string, out io.Writer) error {
	if strings.TrimSpace(ollamaBaseURL) == "" {
		ollamaBaseURL = "http://localhost:11434"
	}

	printTemporalStatus(ctx, cfg, out)
	printOllamaPS(ctx, ollamaBaseURL, out)
	printGitHubIssueSummary(ctx, cfg, out)
	return nil
}

func printTemporalStatus(ctx context.Context, cfg config.Config, out io.Writer) {
	_, _ = fmt.Fprintln(out, "=== Temporal Workflows ===")
	c, err := client.Dial(client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
	})
	if err != nil {
		_, _ = fmt.Fprintf(out, "  unavailable (%s): %v\n\n", cfg.TemporalAddress, err)
		return
	}
	defer c.Close()

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := c.ListWorkflow(reqCtx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: cfg.TemporalNamespace,
		PageSize:  20,
		Query:     "WorkflowType = 'IssueFactoryWorkflow'",
	})
	if err != nil {
		_, _ = fmt.Fprintf(out, "  query failed: %v\n\n", err)
		return
	}
	if len(resp.GetExecutions()) == 0 {
		_, _ = fmt.Fprintln(out, "  (none recorded)")
		_, _ = fmt.Fprintln(out)
		return
	}
	for _, ex := range resp.GetExecutions() {
		wfID := ex.GetExecution().GetWorkflowId()
		line := fmt.Sprintf("  - %-18s status=%-10s", wfID, ex.GetStatus().String())
		val, qErr := c.QueryWorkflow(reqCtx, wfID, ex.GetExecution().GetRunId(), workflow.QueryFactoryStatus)
		if qErr == nil {
			var st workflow.WorkflowStatus
			if val.Get(&st) == nil {
				line += fmt.Sprintf(" stage=%s attempt=%d site=%q mode=%s", st.Stage, st.Attempt, st.SiteSlug, st.AutonomyMode)
			}
		}
		_, _ = fmt.Fprintln(out, line)
	}
	_, _ = fmt.Fprintln(out)
}

func printOllamaPS(ctx context.Context, ollamaBaseURL string, out io.Writer) {
	_, _ = fmt.Fprintln(out, "=== Ollama Loaded Models ===")
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(ollamaBaseURL, "/")+"/api/ps", nil)
	if err != nil {
		_, _ = fmt.Fprintf(out, "  error: %v\n\n", err)
		return
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(out, "  unavailable (%s): %v\n\n", ollamaBaseURL, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var ps struct {
		Models []struct {
			Name     string `json:"name"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ps); err != nil {
		_, _ = fmt.Fprintf(out, "  decode error: %v\n\n", err)
		return
	}
	if len(ps.Models) == 0 {
		_, _ = fmt.Fprintln(out, "  (no models loaded in VRAM)")
		_, _ = fmt.Fprintln(out)
		return
	}
	for _, m := range ps.Models {
		gb := float64(m.SizeVRAM) / (1024 * 1024 * 1024)
		_, _ = fmt.Fprintf(out, "  - %-16s %.2f GB VRAM\n", m.Name, gb)
	}
	_, _ = fmt.Fprintln(out)
}

func printGitHubIssueSummary(ctx context.Context, cfg config.Config, out io.Writer) {
	_, _ = fmt.Fprintf(out, "=== GitHub Factory Issues (%s/%s) ===\n", cfg.GitHubOwner, cfg.GitHubRepo)
	if cfg.GitHubAppID <= 0 || cfg.GitHubInstallationID <= 0 || strings.TrimSpace(cfg.GitHubAppPEMBase64) == "" {
		_, _ = fmt.Fprintln(out, "  (GitHub App credentials not configured in env/Keychain)")
		return
	}

	client, _, err := newGitHubClient(cfg, nil)
	if err != nil {
		_, _ = fmt.Fprintf(out, "  github error: %v\n", err)
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	grouped, err := client.ListFactoryIssuesByLabel(reqCtx)
	if err != nil {
		_, _ = fmt.Fprintf(out, "  error listing issues: %v\n", err)
		return
	}
	if len(grouped) == 0 {
		_, _ = fmt.Fprintln(out, "  (no open factory:* issues)")
		return
	}
	labels := make([]string, 0, len(grouped))
	for k := range grouped {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	for _, lbl := range labels {
		_, _ = fmt.Fprintf(out, "  - %-18s %v\n", lbl, grouped[lbl])
	}
}
