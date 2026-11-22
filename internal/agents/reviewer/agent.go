package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/prompts"
)

const maxReviewTurns = 6

// DiffAudit summarizes changed paths and diff stats inside the worktree.
type DiffAudit struct {
	ChangedFiles    []string `json:"changed_files"`
	PathIsolationOK bool     `json:"path_isolation_ok"`
	DiffStat        string   `json:"diff_stat"`
}

// Inspector provides read-only git diff and URL inspection tools to the Reviewer Agent.
type Inspector interface {
	InspectGitDiff(ctx context.Context, worktreeDir, slug string) (DiffAudit, error)
	ScanExternalURLs(worktreeDir, slug string) ([]string, error)
	ReadSiteFile(worktreeDir, slug, relPath string) (string, error)
}

// Agent performs final security/path isolation audit and composes the PR summary.
type Agent struct {
	client    *llm.Client
	modelName string
	numCtx    int
	inspector Inspector
}

// NewAgent constructs a Reviewer Agent bound to sec-agent.
func NewAgent(client *llm.Client, numCtx int, inspector Inspector) *Agent {
	if numCtx <= 0 {
		numCtx = 32768
	}
	return &Agent{
		client:    client,
		modelName: "sec-agent",
		numCtx:    numCtx,
		inspector: inspector,
	}
}

// Review audits the finished site and composes a ReviewResult for the Pull Request.
func (a *Agent) Review(ctx context.Context, in contracts.ReviewInput, worktreeDir string) (contracts.ReviewResult, error) {
	if a.inspector == nil {
		return contracts.ReviewResult{}, fmt.Errorf("reviewer inspector is nil")
	}

	// Deterministic baseline guarantees that cannot be bypassed by model output.
	diffAudit, err := a.inspector.InspectGitDiff(ctx, worktreeDir, in.Spec.SiteSlug)
	if err != nil {
		return contracts.ReviewResult{}, fmt.Errorf("inspect git diff: %w", err)
	}
	externalURLs, err := a.inspector.ScanExternalURLs(worktreeDir, in.Spec.SiteSlug)
	if err != nil {
		return contracts.ReviewResult{}, fmt.Errorf("scan external urls: %w", err)
	}

	in.ChangedFiles = diffAudit.ChangedFiles
	in.DiffStat = diffAudit.DiffStat

	if !diffAudit.PathIsolationOK || len(externalURLs) > 0 {
		var findings []string
		if !diffAudit.PathIsolationOK {
			findings = append(findings, fmt.Sprintf("path isolation violation: changed files %v outside sites/%s/", diffAudit.ChangedFiles, in.Spec.SiteSlug))
		}
		if len(externalURLs) > 0 {
			findings = append(findings, fmt.Sprintf("disallowed external URLs found: %s", strings.Join(externalURLs, ", ")))
		}
		return contracts.ReviewResult{
			Approved:         false,
			PathIsolationOK:  diffAudit.PathIsolationOK,
			ExternalAssetsOK: len(externalURLs) == 0,
			SpecFidelityOK:   false,
			SummaryMarkdown:  "Automated security/path isolation checks blocked this build.",
			Findings:         findings,
		}, nil
	}

	inputJSON, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return contracts.ReviewResult{}, fmt.Errorf("marshal review input: %w", err)
	}

	messages := []llm.Message{
		{Role: "system", Content: prompts.ReviewerSystem},
		{
			Role:    "user",
			Content: fmt.Sprintf("Audit this ReviewInput and call `submit_review`:\n\n```json\n%s\n```", string(inputJSON)),
		},
	}

	tools := reviewerTools()
	tags := []string{fmt.Sprintf("issue-%d", in.Issue.IssueNumber), "role-sec"}

	for turn := 1; turn <= maxReviewTurns; turn++ {
		resp, err := a.client.ChatCompletion(ctx, llm.ChatRequest{
			Model:       a.modelName,
			Messages:    messages,
			Tools:       tools,
			Temperature: 0,
			MaxTokens:   8192,
			Options:     map[string]any{"num_ctx": a.numCtx},
			Tags:        tags,
		})
		if err != nil {
			return contracts.ReviewResult{}, fmt.Errorf("reviewer turn %d llm call: %w", turn, err)
		}

		msg := resp.Choices[0].Message
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "Call `submit_review` with your final audit verdict.",
			})
			continue
		}

		for _, tc := range msg.ToolCalls {
			switch tc.Function.Name {
			case "inspect_git_diff":
				messages = appendTool(messages, tc, diffAudit)
			case "scan_external_urls":
				messages = appendTool(messages, tc, map[string]any{
					"external_urls":      externalURLs,
					"external_assets_ok": len(externalURLs) == 0,
				})
			case "read_site_file":
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					messages = appendTool(messages, tc, map[string]any{"error": err.Error()})
					continue
				}
				content, readErr := a.inspector.ReadSiteFile(worktreeDir, in.Spec.SiteSlug, args.Path)
				if readErr != nil {
					messages = appendTool(messages, tc, map[string]any{"error": readErr.Error()})
					continue
				}
				messages = appendTool(messages, tc, map[string]any{"path": args.Path, "content": content})
			case "submit_review":
				var sub struct {
					Approved        bool     `json:"approved"`
					SpecFidelityOK  bool     `json:"spec_fidelity_ok"`
					SummaryMarkdown string   `json:"summary_markdown"`
					Findings        []string `json:"findings"`
				}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &sub); err != nil {
					messages = appendTool(messages, tc, map[string]any{"error": err.Error()})
					continue
				}
				return contracts.ReviewResult{
					Approved:         sub.Approved && diffAudit.PathIsolationOK && len(externalURLs) == 0,
					PathIsolationOK:  diffAudit.PathIsolationOK,
					ExternalAssetsOK: len(externalURLs) == 0,
					SpecFidelityOK:   sub.SpecFidelityOK,
					SummaryMarkdown:  strings.TrimSpace(sub.SummaryMarkdown),
					Findings:         sub.Findings,
				}, nil
			default:
				messages = appendTool(messages, tc, map[string]any{"error": fmt.Sprintf("unknown tool %q", tc.Function.Name)})
			}
		}
	}

	return contracts.ReviewResult{
		Approved:         false,
		PathIsolationOK:  diffAudit.PathIsolationOK,
		ExternalAssetsOK: len(externalURLs) == 0,
		SpecFidelityOK:   false,
		SummaryMarkdown:  fmt.Sprintf("Reviewer Agent exhausted %d turns without calling `submit_review`.", maxReviewTurns),
		Findings:         []string{fmt.Sprintf("reviewer agent exhausted %d turns without submitting an explicit review verdict", maxReviewTurns)},
	}, nil
}

func appendTool(msgs []llm.Message, tc llm.ToolCall, v any) []llm.Message {
	b, _ := json.Marshal(v)
	return append(msgs, llm.Message{
		Role:       "tool",
		ToolCallID: tc.ID,
		Name:       tc.Function.Name,
		Content:    string(b),
	})
}

func reviewerTools() []llm.Tool {
	return []llm.Tool{
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "inspect_git_diff",
				Description: "Verify that only files inside sites/<site_slug>/ were modified.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "scan_external_urls",
				Description: "Scan generated site files for disallowed external URLs or trackers.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "read_site_file",
				Description: "Read a generated file inside sites/<site_slug>/.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "submit_review",
				Description: "Submit the final security and fidelity review verdict with PR summary markdown.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"approved":         map[string]any{"type": "boolean"},
						"spec_fidelity_ok": map[string]any{"type": "boolean"},
						"summary_markdown": map[string]any{"type": "string"},
						"findings": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
					},
					"required": []string{"approved", "spec_fidelity_ok", "summary_markdown"},
				},
			},
		},
	}
}
