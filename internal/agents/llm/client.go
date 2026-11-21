package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message represents an OpenAI-compatible chat message.
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

// ToolCall represents a single model-initiated tool invocation.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the function name and raw JSON argument string.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool describes an OpenAI-compatible function tool exposed to an agent.
type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition defines the schema for an agent tool.
type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ResponseFormat enforces structured JSON Schema output.
type ResponseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *JSONSchemaConfig `json:"json_schema,omitempty"`
}

// JSONSchemaConfig configures strict JSON Schema output.
type JSONSchemaConfig struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

// ChatRequest captures a single completion request to LiteLLM.
type ChatRequest struct {
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	Tools           []Tool          `json:"tools,omitempty"`
	ResponseFormat  *ResponseFormat `json:"response_format,omitempty"`
	Temperature     float64         `json:"temperature"`
	MaxTokens       int             `json:"max_tokens"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Options         map[string]any  `json:"options,omitempty"`
	Tags            []string        `json:"-"`
}

// ChatResponse models the subset of LiteLLM/OpenAI chat completion response used by agents.
type ChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int     `json:"index"`
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Client is a minimal HTTP client for the LiteLLM OpenAI-compatible gateway.
// Each agent constructs or calls a Client scoped to its own virtual API key.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient constructs a LiteLLM client for a single agent route key.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// ChatCompletion executes a POST /chat/completions call against LiteLLM.
func (c *Client) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if c.apiKey == "" {
		return ChatResponse{}, fmt.Errorf("litellm api key is empty for model %q", req.Model)
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 8192
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build chat request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if len(req.Tags) > 0 {
		httpReq.Header.Set("X-Litellm-Tag", strings.Join(req.Tags, ","))
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("litellm request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read litellm response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("litellm returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed ChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, fmt.Errorf("decode litellm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("litellm returned zero choices")
	}
	return parsed, nil
}
