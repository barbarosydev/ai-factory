package dev

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/prompts"
)

const maxToolTurns = 10

// HeartbeatFunc is invoked after each model/tool turn so Temporal activities keep heartbeating.
type HeartbeatFunc func(detail string)

// Agent implements or modifies static website files inside a sandboxed directory.
type Agent struct {
	client    *llm.Client
	modelName string
	numCtx    int
}

// NewAgent constructs a Developer Agent bound to the dev-agent LiteLLM client.
func NewAgent(client *llm.Client, numCtx int) *Agent {
	if numCtx <= 0 {
		numCtx = 32768
	}
	return &Agent{
		client:    client,
		modelName: "dev-agent",
		numCtx:    numCtx,
	}
}

// Implement executes the bounded Developer tool loop inside siteDir.
func (a *Agent) Implement(
	ctx context.Context,
	task contracts.DeveloperTaskInput,
	siteDir string,
	heartbeat HeartbeatFunc,
) (contracts.ImplementationResult, error) {
	sandbox, err := OpenSiteSandbox(siteDir)
	if err != nil {
		return contracts.ImplementationResult{}, err
	}
	defer func() { _ = sandbox.Close() }()

	taskJSON, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return contracts.ImplementationResult{}, fmt.Errorf("marshal developer task: %w", err)
	}

	messages := []llm.Message{
		{Role: "system", Content: prompts.DevSystem},
		{
			Role:    "user",
			Content: fmt.Sprintf("Implement or update the website for this DeveloperTaskInput (prefer `scaffold_site_from_spec` or `write_site_bundle` to complete quickly):\n\n```json\n%s\n```", string(taskJSON)),
		},
	}

	tools := developerTools()
	tags := []string{fmt.Sprintf("issue-%d", task.IssueNumber), fmt.Sprintf("attempt-%d", task.Attempt), "role-dev"}

	changedSet := make(map[string]bool)
	var toolTrace []string
	var finishSummary string
	finished := false

	for turn := 1; turn <= maxToolTurns; turn++ {
		if heartbeat != nil {
			heartbeat(fmt.Sprintf("dev turn %d/%d", turn, maxToolTurns))
		}

		resp, err := a.client.ChatCompletion(ctx, llm.ChatRequest{
			Model:       a.modelName,
			Messages:    messages,
			Tools:       tools,
			Temperature: 0.1,
			MaxTokens:   8192,
			Options:     map[string]any{"num_ctx": a.numCtx},
			Tags:        tags,
		})
		if err != nil {
			return contracts.ImplementationResult{}, fmt.Errorf("dev turn %d llm call: %w", turn, err)
		}

		assistantMsg := resp.Choices[0].Message
		messages = append(messages, assistantMsg)

		if len(assistantMsg.ToolCalls) == 0 {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "Call `scaffold_site_from_spec`, `write_site_bundle`, `write_file`, or `finish` rather than replying with plain text.",
			})
			continue
		}

		for _, tc := range assistantMsg.ToolCalls {
			toolTrace = append(toolTrace, fmt.Sprintf("turn-%d:%s", turn, tc.Function.Name))
			resultJSON, didFinish, summary := executeTool(sandbox, task.Spec, tc.Function, changedSet)
			if didFinish {
				finished = true
				finishSummary = summary
			}
			messages = append(messages, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    resultJSON,
			})
		}

		if finished {
			files := sortedKeys(changedSet)
			if len(files) == 0 {
				files, _ = sandbox.ListFiles()
			}
			return contracts.ImplementationResult{
				Attempt:      task.Attempt,
				Summary:      finishSummary,
				FilesChanged: files,
				TurnsUsed:    turn,
				ToolTrace:    toolTrace,
			}, nil
		}
	}

	if _, err := sandbox.ReadFile("index.html"); err == nil {
		files := sortedKeys(changedSet)
		if len(files) == 0 {
			files, _ = sandbox.ListFiles()
		}
		return contracts.ImplementationResult{
			Attempt:      task.Attempt,
			Summary:      "Reached max tool turns; handing current site files to Tester Agent.",
			FilesChanged: files,
			TurnsUsed:    maxToolTurns,
			ToolTrace:    toolTrace,
		}, nil
	}

	return contracts.ImplementationResult{}, fmt.Errorf("developer agent exceeded %d turns without creating index.html", maxToolTurns)
}

func executeTool(
	sandbox *SiteSandbox,
	spec contracts.WebsiteSpecification,
	fn llm.FunctionCall,
	changedSet map[string]bool,
) (resultJSON string, didFinish bool, summary string) {
	switch fn.Name {
	case "scaffold_site_from_spec":
		var args struct {
			AutoFinish *bool  `json:"auto_finish"`
			Summary    string `json:"summary"`
		}
		_ = json.Unmarshal([]byte(fn.Arguments), &args)
		files, check, err := sandbox.ScaffoldFromSpec(spec)
		if err != nil {
			return marshalJSONString(map[string]any{"error": err.Error()}), false, ""
		}
		for _, f := range files {
			changedSet[f] = true
		}
		autoFinish := args.AutoFinish == nil || *args.AutoFinish
		sum := strings.TrimSpace(args.Summary)
		if sum == "" {
			sum = fmt.Sprintf("Scaffolded responsive HTML5/CSS/JS website for %s (%d sections).", spec.SiteSlug, len(spec.Sections))
		}
		if autoFinish && check.Passed {
			return marshalJSONString(map[string]any{
				"status":          "completed",
				"files_written":   files,
				"structure_check": check,
			}), true, sum
		}
		return marshalJSONString(map[string]any{
			"status":          "scaffolded",
			"files_written":   files,
			"structure_check": check,
		}), false, ""

	case "write_site_bundle":
		var args struct {
			IndexHTML  string `json:"index_html"`
			StylesCSS  string `json:"styles_css"`
			ScriptJS   string `json:"script_js"`
			Summary    string `json:"summary"`
			AutoFinish *bool  `json:"auto_finish"`
		}
		if err := json.Unmarshal([]byte(fn.Arguments), &args); err != nil {
			return marshalJSONString(map[string]any{"error": fmt.Sprintf("invalid arguments: %v", err)}), false, ""
		}
		var written []string
		if strings.TrimSpace(args.IndexHTML) != "" {
			if _, err := sandbox.WriteFile("index.html", args.IndexHTML); err != nil {
				return marshalJSONString(map[string]any{"error": err.Error()}), false, ""
			}
			changedSet["index.html"] = true
			written = append(written, "index.html")
		}
		if strings.TrimSpace(args.StylesCSS) != "" {
			if _, err := sandbox.WriteFile("styles.css", args.StylesCSS); err != nil {
				return marshalJSONString(map[string]any{"error": err.Error()}), false, ""
			}
			changedSet["styles.css"] = true
			written = append(written, "styles.css")
		}
		if strings.TrimSpace(args.ScriptJS) != "" {
			if _, err := sandbox.WriteFile("script.js", args.ScriptJS); err != nil {
				return marshalJSONString(map[string]any{"error": err.Error()}), false, ""
			}
			changedSet["script.js"] = true
			written = append(written, "script.js")
		}
		check := sandbox.CheckSiteStructure(spec)
		autoFinish := args.AutoFinish == nil || *args.AutoFinish
		sum := strings.TrimSpace(args.Summary)
		if sum == "" {
			sum = fmt.Sprintf("Wrote site bundle (%s) for %s.", strings.Join(written, ", "), spec.SiteSlug)
		}
		if autoFinish && check.Passed {
			return marshalJSONString(map[string]any{
				"status":          "completed",
				"files_written":   written,
				"structure_check": check,
			}), true, sum
		}
		return marshalJSONString(map[string]any{
			"status":          "written",
			"files_written":   written,
			"structure_check": check,
		}), false, ""

	case "list_files":
		files, err := sandbox.ListFiles()
		if err != nil {
			return marshalJSONString(map[string]any{"error": err.Error()}), false, ""
		}
		return marshalJSONString(map[string]any{"files": files}), false, ""

	case "read_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(fn.Arguments), &args); err != nil {
			return marshalJSONString(map[string]any{"error": fmt.Sprintf("invalid arguments: %v", err)}), false, ""
		}
		content, err := sandbox.ReadFile(args.Path)
		if err != nil {
			return marshalJSONString(map[string]any{"error": err.Error()}), false, ""
		}
		return marshalJSONString(map[string]any{"path": args.Path, "content": content}), false, ""

	case "write_file":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(fn.Arguments), &args); err != nil {
			return marshalJSONString(map[string]any{"error": fmt.Sprintf("invalid arguments: %v", err)}), false, ""
		}
		n, err := sandbox.WriteFile(args.Path, args.Content)
		if err != nil {
			return marshalJSONString(map[string]any{"error": err.Error()}), false, ""
		}
		if clean, err := sanitizeRelPath(args.Path); err == nil {
			changedSet[clean] = true
		}
		return marshalJSONString(map[string]any{"status": "written", "path": args.Path, "bytes": n}), false, ""

	case "check_site_structure":
		res := sandbox.CheckSiteStructure(spec)
		return marshalJSONString(res), false, ""

	case "finish":
		var args struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal([]byte(fn.Arguments), &args)
		res := sandbox.CheckSiteStructure(spec)
		if !res.Passed {
			return marshalJSONString(map[string]any{
				"status": "rejected",
				"reason": "check_site_structure must pass before finishing",
				"errors": res.Errors,
			}), false, ""
		}
		return marshalJSONString(map[string]any{"status": "completed"}), true, args.Summary

	default:
		return marshalJSONString(map[string]any{"error": fmt.Sprintf("unknown tool %q", fn.Name)}), false, ""
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func developerTools() []llm.Tool {
	return []llm.Tool{
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "scaffold_site_from_spec",
				Description: "High-velocity skill: generate a complete, responsive, accessible HTML5/CSS3/JS website (index.html, styles.css, script.js) from WebsiteSpecification, validate structure, and finish in 1 call.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"auto_finish": map[string]any{"type": "boolean", "description": "Automatically complete implementation if structure validation passes (default true)"},
						"summary":     map[string]any{"type": "string", "description": "Concise summary of the generated website"},
					},
				},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "write_site_bundle",
				Description: "Batch skill: write index.html, styles.css, and/or script.js in one tool call, validate structure, and finish if valid.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"index_html":  map[string]any{"type": "string", "description": "Complete index.html contents"},
						"styles_css":  map[string]any{"type": "string", "description": "Complete styles.css contents"},
						"script_js":   map[string]any{"type": "string", "description": "Optional script.js contents"},
						"summary":     map[string]any{"type": "string", "description": "Concise summary of changes"},
						"auto_finish": map[string]any{"type": "boolean", "description": "Finish immediately if structure check passes (default true)"},
					},
				},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "list_files",
				Description: "List existing files inside the target website directory.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "read_file",
				Description: "Read a file inside the target website directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "Relative file path such as index.html or styles.css"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "write_file",
				Description: "Create or overwrite a static web file (.html, .css, .js, .svg) inside the target website directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "Relative file path such as index.html or styles.css"},
						"content": map[string]any{"type": "string", "description": "Complete file contents"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "check_site_structure",
				Description: "Validate index.html HTML5 semantics, required section IDs, required keywords, internal anchor links, and local asset references.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "finish",
				Description: "Complete the implementation after check_site_structure passes.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"summary": map[string]any{"type": "string", "description": "Concise summary of files created or modified"},
					},
					"required": []string{"summary"},
				},
			},
		},
	}
}
