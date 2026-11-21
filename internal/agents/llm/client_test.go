package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatCompletionSendsAuthTagAndDecodesToolCalls(t *testing.T) {
	t.Parallel()

	var gotAuth, gotTag string
	var gotMaxTokens int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTag = r.Header.Get("X-Litellm-Tag")
		var payload ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotMaxTokens = payload.MaxTokens
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test",
			"model": "dev-agent",
			"choices": [{
				"index": 0,
				"finish_reason": "tool_calls",
				"message": {
					"role": "assistant",
					"tool_calls": [{
						"id": "call_1",
						"type": "function",
						"function": {"name": "write_file", "arguments": "{\"path\":\"index.html\"}"}
					}]
				}
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
		}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "sk-dev-agent-test", 5*time.Second)
	resp, err := client.ChatCompletion(context.Background(), ChatRequest{
		Model:     "dev-agent",
		Messages:  []Message{{Role: "user", Content: "Build site"}},
		MaxTokens: 12288,
		Tags:      []string{"issue-42", "attempt-1"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}
	if gotAuth != "Bearer sk-dev-agent-test" {
		t.Errorf("Authorization = %q, want Bearer sk-dev-agent-test", gotAuth)
	}
	if gotTag != "issue-42,attempt-1" {
		t.Errorf("X-Litellm-Tag = %q, want issue-42,attempt-1", gotTag)
	}
	if gotMaxTokens != 12288 {
		t.Errorf("MaxTokens = %d, want 12288", gotMaxTokens)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 || resp.Choices[0].Message.ToolCalls[0].Function.Name != "write_file" {
		t.Fatalf("unexpected tool calls: %+v", resp.Choices[0].Message.ToolCalls)
	}
}
