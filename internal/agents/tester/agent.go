package tester

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/prompts"
)

const maxTesterTurns = 6

// HeartbeatFunc reports activity progress to Temporal.
type HeartbeatFunc func(detail string)

// PlaywrightResult represents the raw deterministic output of running tests/site.spec.mjs.
type PlaywrightResult struct {
	Passed            bool                        `json:"passed"`
	DurationMs        int64                       `json:"duration_ms"`
	Checks            []contracts.SingleCheck     `json:"checks"`
	Failures          []contracts.FailureEvidence `json:"failures,omitempty"`
	DesktopScreenshot string                      `json:"desktop_screenshot_path"`
	MobileScreenshot  string                      `json:"mobile_screenshot_path"`
	RawOutput         string                      `json:"raw_output,omitempty"`
}

// Backend abstracts the deterministic Playwright runner and read-only site inspection.
type Backend interface {
	RunPlaywrightSuite(ctx context.Context, worktreeDir string, spec contracts.WebsiteSpecification) (PlaywrightResult, error)
	InspectDOMSnapshot(ctx context.Context, worktreeDir, slug string) (string, error)
	ReadSiteFile(worktreeDir, slug, relPath string) (string, error)
}

// Agent executes independent verification combining Playwright tools with LLM root-cause diagnosis.
type Agent struct {
	client    llm.ChatCompleter
	modelName string
	numCtx    int
	backend   Backend
}

// NewAgent constructs a Tester Agent bound to test-agent.
func NewAgent(client llm.ChatCompleter, numCtx int, backend Backend) *Agent {
	if numCtx <= 0 {
		numCtx = 32768
	}
	return &Agent{
		client:    client,
		modelName: "test-agent",
		numCtx:    numCtx,
		backend:   backend,
	}
}

// Verify runs the Tester Agent against worktreeDir for the given TestRunInput.
func (a *Agent) Verify(
	ctx context.Context,
	in contracts.TestRunInput,
	worktreeDir string,
	heartbeat HeartbeatFunc,
) (contracts.TestReport, error) {
	if a.backend == nil {
		return contracts.TestReport{}, fmt.Errorf("tester backend is nil")
	}

	inputJSON, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return contracts.TestReport{}, fmt.Errorf("marshal test input: %w", err)
	}

	messages := []llm.Message{
		{Role: "system", Content: prompts.TesterSystem},
		{
			Role:    "user",
			Content: fmt.Sprintf("Verify the website for TestRunInput:\n\n```json\n%s\n```", string(inputJSON)),
		},
	}

	tools := testerTools()
	tags := []string{fmt.Sprintf("issue-%d", in.IssueNumber), fmt.Sprintf("attempt-%d", in.Attempt), "role-test"}

	var pwRan bool
	var lastPW PlaywrightResult

	for turn := 1; turn <= maxTesterTurns; turn++ {
		if heartbeat != nil {
			heartbeat(fmt.Sprintf("tester turn %d/%d", turn, maxTesterTurns))
		}

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
			return contracts.TestReport{}, fmt.Errorf("tester turn %d llm call: %w", turn, err)
		}

		msg := resp.Choices[0].Message
		messages = append(messages, msg)

		if len(msg.ToolCalls) == 0 {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "Call `run_playwright_suite`, inspect files if needed, and then call `submit_test_report`.",
			})
			continue
		}

		for _, tc := range msg.ToolCalls {
			switch tc.Function.Name {
			case "run_playwright_suite":
				res, runErr := a.backend.RunPlaywrightSuite(ctx, worktreeDir, in.Spec)
				if runErr != nil {
					messages = appendToolMsg(messages, tc, map[string]any{"error": runErr.Error()})
					continue
				}
				pwRan = true
				lastPW = res
				snap, _ := a.backend.InspectDOMSnapshot(ctx, worktreeDir, in.Spec.SiteSlug)
				messages = appendToolMsg(messages, tc, map[string]any{
					"passed":                  res.Passed,
					"duration_ms":             res.DurationMs,
					"checks":                  res.Checks,
					"failures":                res.Failures,
					"desktop_screenshot_path": res.DesktopScreenshot,
					"mobile_screenshot_path":  res.MobileScreenshot,
					"dom_snapshot":            snap,
				})

			case "inspect_dom_snapshot":
				snap, snapErr := a.backend.InspectDOMSnapshot(ctx, worktreeDir, in.Spec.SiteSlug)
				if snapErr != nil {
					messages = appendToolMsg(messages, tc, map[string]any{"error": snapErr.Error()})
					continue
				}
				messages = appendToolMsg(messages, tc, map[string]any{"dom_snapshot": snap})

			case "read_site_file":
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					messages = appendToolMsg(messages, tc, map[string]any{"error": err.Error()})
					continue
				}
				content, readErr := a.backend.ReadSiteFile(worktreeDir, in.Spec.SiteSlug, args.Path)
				if readErr != nil {
					messages = appendToolMsg(messages, tc, map[string]any{"error": readErr.Error()})
					continue
				}
				messages = appendToolMsg(messages, tc, map[string]any{"path": args.Path, "content": content})

			case "submit_test_report":
				if !pwRan {
					messages = appendToolMsg(messages, tc, map[string]any{
						"status": "rejected",
						"error":  "run_playwright_suite must be called before submit_test_report",
					})
					continue
				}
				var sub struct {
					SemanticQualityOK bool                        `json:"semantic_quality_ok"`
					TesterSummary     string                      `json:"tester_summary"`
					Failures          []contracts.FailureEvidence `json:"failures"`
				}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &sub); err != nil {
					messages = appendToolMsg(messages, tc, map[string]any{"error": fmt.Sprintf("invalid report JSON: %v", err)})
					continue
				}

				// Ground-truth enforcement: Playwright failures can never be overridden as passing,
				// and deterministic Playwright failures are always preserved alongside LLM findings.
				mergedFailures := mergeFailures(lastPW.Failures, sub.Failures)
				passed := lastPW.Passed && sub.SemanticQualityOK && len(mergedFailures) == 0
				if !passed && len(mergedFailures) == 0 {
					mergedFailures = []contracts.FailureEvidence{{
						CheckName:   "semantic-quality-check",
						Expected:    "Complete site content without placeholder copy",
						Actual:      "Tester flagged semantic quality issue",
						Remediation: sub.TesterSummary,
					}}
				}

				return contracts.TestReport{
					Attempt:           in.Attempt,
					Passed:            passed,
					SemanticQualityOK: sub.SemanticQualityOK,
					DurationMs:        lastPW.DurationMs,
					Checks:            lastPW.Checks,
					Failures:          mergedFailures,
					TesterSummary:     sub.TesterSummary,
					DesktopScreenshot: lastPW.DesktopScreenshot,
					MobileScreenshot:  lastPW.MobileScreenshot,
				}, nil

			default:
				messages = appendToolMsg(messages, tc, map[string]any{"error": fmt.Sprintf("unknown tool %q", tc.Function.Name)})
			}
		}
	}

	// Fail-closed fallback if max turns reached without calling submit_test_report.
	if !pwRan {
		res, err := a.backend.RunPlaywrightSuite(ctx, worktreeDir, in.Spec)
		if err != nil {
			return contracts.TestReport{}, err
		}
		lastPW = res
	}
	failures := mergeFailures(lastPW.Failures, []contracts.FailureEvidence{{
		CheckName:   "tester-max-turns",
		Expected:    "Tester Agent calls submit_test_report with explicit verification verdict",
		Actual:      fmt.Sprintf("exhausted %d turns without calling submit_test_report", maxTesterTurns),
		Remediation: "Ensure valid HTML/CSS and re-run verification.",
	}})
	return contracts.TestReport{
		Attempt:           in.Attempt,
		Passed:            false,
		SemanticQualityOK: false,
		DurationMs:        lastPW.DurationMs,
		Checks:            lastPW.Checks,
		Failures:          failures,
		TesterSummary:     fmt.Sprintf("Tester Agent exhausted %d turns without calling submit_test_report.", maxTesterTurns),
		DesktopScreenshot: lastPW.DesktopScreenshot,
		MobileScreenshot:  lastPW.MobileScreenshot,
	}, nil
}

func mergeFailures(base, extra []contracts.FailureEvidence) []contracts.FailureEvidence {
	var out []contracts.FailureEvidence
	seen := make(map[string]int)
	for _, f := range base {
		key := strings.ToLower(strings.TrimSpace(f.CheckName))
		seen[key] = len(out)
		out = append(out, f)
	}
	for _, f := range extra {
		key := strings.ToLower(strings.TrimSpace(f.CheckName))
		if idx, exists := seen[key]; exists {
			if strings.TrimSpace(out[idx].FileHint) == "" && strings.TrimSpace(f.FileHint) != "" {
				out[idx].FileHint = f.FileHint
			}
			if strings.TrimSpace(f.Remediation) != "" {
				out[idx].Remediation = f.Remediation
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, f)
	}
	return out
}

func appendToolMsg(msgs []llm.Message, tc llm.ToolCall, payload any) []llm.Message {
	b, _ := json.Marshal(payload)
	return append(msgs, llm.Message{
		Role:       "tool",
		ToolCallID: tc.ID,
		Name:       tc.Function.Name,
		Content:    string(b),
	})
}

func testerTools() []llm.Tool {
	return []llm.Tool{
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "run_playwright_suite",
				Description: "Execute the 8-gate Playwright verification suite across desktop (1280x800) and mobile (375x812) viewports and capture screenshots.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "inspect_dom_snapshot",
				Description: "Return a concise summary of the rendered HTML sections, headings, links, and computed layout metrics.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "read_site_file",
				Description: "Read a generated site file (index.html, styles.css, script.js) to diagnose failures or verify copy.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "submit_test_report",
				Description: "Submit the final verification verdict and actionable FailureEvidence after running Playwright.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"semantic_quality_ok": map[string]any{"type": "boolean"},
						"tester_summary":      map[string]any{"type": "string"},
						"failures": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"check_name":  map[string]any{"type": "string"},
									"expected":    map[string]any{"type": "string"},
									"actual":      map[string]any{"type": "string"},
									"file_hint":   map[string]any{"type": "string"},
									"remediation": map[string]any{"type": "string"},
								},
								"required": []string{"check_name", "expected", "actual", "remediation"},
							},
						},
					},
					"required": []string{"semantic_quality_ok", "tester_summary"},
				},
			},
		},
	}
}
