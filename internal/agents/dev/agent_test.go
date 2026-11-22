package dev

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/contracts"
)

type stubCompleter struct {
	fn func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)
}

func (s stubCompleter) ChatCompletion(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return s.fn(ctx, req)
}

func TestAgentImplementUsesInjectedChatCompleter(t *testing.T) {
	t.Parallel()

	siteDir := t.TempDir()
	spec := testSpec()

	const validHTML = `<!DOCTYPE html>
<html>
<head><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Kaffeekultur</title><link rel="stylesheet" href="styles.css"></head>
<body>
  <header><nav><a href="#menu">Menu</a></nav></header>
  <main>
    <section id="hero"><h1>Kaffeekultur</h1><p>Freshly roasted in Kreuzberg.</p></section>
    <section id="menu"><h2>Menu</h2><p>Single-origin Espresso daily.</p></section>
    <section id="hours"><h2>Hours</h2><p>Mon-Fri 08:00-18:00</p></section>
  </main>
  <footer>Kaffeekultur Berlin</footer>
</body>
</html>`

	turn := 0
	completer := stubCompleter{
		fn: func(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			turn++
			for _, tool := range req.Tools {
				if tool.Function.Name == "scaffold_site_from_spec" {
					t.Fatalf("retired scaffold_site_from_spec must not be exposed to LLM")
				}
			}

			var tc llm.ToolCall
			if turn == 1 {
				argsBytes, _ := json.Marshal(map[string]string{
					"index_html": validHTML,
					"styles_css": "body { margin: 0; font-family: system-ui, sans-serif; }",
					"summary":    "Built authentic coffee shop page",
				})
				tc = llm.ToolCall{
					ID:   "call-1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "write_site_bundle",
						Arguments: string(argsBytes),
					},
				}
			} else {
				tc = llm.ToolCall{
					ID:   "call-2",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "finish",
						Arguments: `{"summary":"Implemented authentic site via injected completer."}`,
					},
				}
			}

			var resp llm.ChatResponse
			resp.Choices = []struct {
				Index        int         `json:"index"`
				Message      llm.Message `json:"message"`
				FinishReason string      `json:"finish_reason"`
			}{
				{
					Index: 0,
					Message: llm.Message{
						Role:      "assistant",
						ToolCalls: []llm.ToolCall{tc},
					},
				},
			}
			return resp, nil
		},
	}

	agent := NewAgent(completer, 8192)
	var heartbeats int
	res, err := agent.Implement(context.Background(), contracts.DeveloperTaskInput{
		IssueNumber: 99,
		Attempt:     1,
		Spec:        spec,
	}, siteDir, func(_ string) { heartbeats++ })
	if err != nil {
		t.Fatalf("Implement failed: %v", err)
	}
	if heartbeats == 0 {
		t.Fatalf("expected heartbeat callback to be invoked")
	}
	if res.TurnsUsed != 1 {
		t.Fatalf("expected 1 turn, got %d", res.TurnsUsed)
	}
	if len(res.FilesChanged) != 2 {
		t.Fatalf("expected 2 changed files, got %v", res.FilesChanged)
	}
}
