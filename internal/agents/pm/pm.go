package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/prompts"
)

// SpecStore provides read-only access to existing website metadata for modify requests.
type SpecStore interface {
	ListExistingSites() ([]string, error)
	ReadExistingSpec(slug string) (string, error)
}

// Agent translates a CustomerIssueInput into a validated PMDecisionOutput.
type Agent struct {
	client    *llm.Client
	modelName string
	numCtx    int
	store     SpecStore
}

// NewAgent constructs a PM / UX Agent bound to the given LiteLLM client.
func NewAgent(client *llm.Client, numCtx int, store SpecStore) *Agent {
	if numCtx <= 0 {
		numCtx = 32768
	}
	return &Agent{
		client:    client,
		modelName: "pm-agent",
		numCtx:    numCtx,
		store:     store,
	}
}

// AnalyzeIssue evaluates the customer issue and returns a ready WebsiteSpecification,
// clarification questions, or a clean out-of-scope rejection.
func (a *Agent) AnalyzeIssue(ctx context.Context, issue contracts.CustomerIssueInput) (contracts.PMDecisionOutput, error) {
	if a.store != nil {
		if sites, err := a.store.ListExistingSites(); err == nil && len(sites) > 0 {
			issue.ExistingSites = sites
		}
	}

	var existingSpecJSON string
	if a.store != nil && strings.TrimSpace(issue.TargetSiteSlug) != "" {
		if raw, err := a.store.ReadExistingSpec(issue.TargetSiteSlug); err == nil {
			existingSpecJSON = raw
		}
	}

	issueJSON, err := json.MarshalIndent(issue, "", "  ")
	if err != nil {
		return contracts.PMDecisionOutput{}, fmt.Errorf("marshal customer issue: %w", err)
	}

	userPrompt := fmt.Sprintf("Analyze this CustomerIssueInput and produce a PMDecisionOutput JSON object:\n\n```json\n%s\n```", string(issueJSON))
	if existingSpecJSON != "" {
		userPrompt += fmt.Sprintf("\n\nExisting WebsiteSpecification for target_site_slug %q:\n```json\n%s\n```", issue.TargetSiteSlug, existingSpecJSON)
	}

	messages := []llm.Message{
		{Role: "system", Content: prompts.PMSystem},
		{Role: "user", Content: userPrompt},
	}

	schema := contracts.PMDecisionJSONSchema()
	tags := []string{fmt.Sprintf("issue-%d", issue.IssueNumber), "role-pm"}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := a.client.ChatCompletion(ctx, llm.ChatRequest{
			Model:       a.modelName,
			Messages:    messages,
			Temperature: 0,
			MaxTokens:   8192,
			Options:     map[string]any{"num_ctx": a.numCtx},
			ResponseFormat: &llm.ResponseFormat{
				Type: "json_schema",
				JSONSchema: &llm.JSONSchemaConfig{
					Name:   "pm_decision_output",
					Strict: true,
					Schema: schema,
				},
			},
			Tags: tags,
		})
		if err != nil {
			return contracts.PMDecisionOutput{}, fmt.Errorf("pm llm call: %w", err)
		}

		raw := strings.TrimSpace(resp.Choices[0].Message.Content)
		var out contracts.PMDecisionOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			lastErr = fmt.Errorf("attempt %d invalid JSON: %w", attempt, err)
			messages = append(messages,
				llm.Message{Role: "assistant", Content: raw},
				llm.Message{Role: "user", Content: fmt.Sprintf("Your previous output was not valid JSON (%v). Fix it and return strictly valid JSON matching the schema.", err)},
			)
			continue
		}

		if err := out.Validate(); err != nil {
			lastErr = fmt.Errorf("attempt %d schema invariant failed: %w", attempt, err)
			messages = append(messages,
				llm.Message{Role: "assistant", Content: raw},
				llm.Message{Role: "user", Content: fmt.Sprintf("Your JSON failed validation: %v. Correct the fields and return valid JSON.", err)},
			)
			continue
		}

		return out, nil
	}

	return contracts.PMDecisionOutput{}, fmt.Errorf("pm agent exhausted schema retries: %w", lastErr)
}
