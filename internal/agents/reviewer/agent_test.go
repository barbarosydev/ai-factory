package reviewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/contracts"
)

type stubInspector struct {
	audit DiffAudit
	urls  []string
}

func (s stubInspector) InspectGitDiff(context.Context, string, string) (DiffAudit, error) {
	return s.audit, nil
}
func (s stubInspector) ScanExternalURLs(string, string) ([]string, error) { return s.urls, nil }
func (s stubInspector) ReadSiteFile(string, string, string) (string, error) {
	return "<h1>Coffee</h1>", nil
}

func TestReviewerBlocksPathEscapeBeforeCallingLLM(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	agent := NewAgent(llm.NewClient(srv.URL, "sk-sec-test", 5*time.Second), 32768, stubInspector{
		audit: DiffAudit{ChangedFiles: []string{".github/workflows/ci.yml"}, PathIsolationOK: false},
	})

	res, err := agent.Review(context.Background(), contracts.ReviewInput{
		Spec: contracts.WebsiteSpecification{SiteSlug: "coffee-shop"},
	}, "/tmp/wt")
	if err != nil {
		t.Fatalf("Review returned unexpected error: %v", err)
	}
	if res.Approved || res.PathIsolationOK {
		t.Fatalf("expected deterministic rejection for path isolation violation, got %+v", res)
	}
	if called {
		t.Fatal("expected LLM not to be called when deterministic path isolation failed")
	}
}

func TestReviewerHappyPath(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		args, _ := json.Marshal(map[string]any{
			"approved":         true,
			"spec_fidelity_ok": true,
			"summary_markdown": "Implemented responsive coffee-shop site matching all 5 sections.",
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-sec",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []llm.ToolCall{{
						ID:       "r1",
						Type:     "function",
						Function: llm.FunctionCall{Name: "submit_review", Arguments: string(args)},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	agent := NewAgent(llm.NewClient(srv.URL, "sk-sec-test", 5*time.Second), 32768, stubInspector{
		audit: DiffAudit{ChangedFiles: []string{"sites/coffee-shop/index.html"}, PathIsolationOK: true},
	})

	res, err := agent.Review(context.Background(), contracts.ReviewInput{
		Issue: contracts.CustomerIssueInput{IssueNumber: 42},
		Spec:  contracts.WebsiteSpecification{SiteSlug: "coffee-shop"},
	}, "/tmp/wt")
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}
	if !res.Approved || !res.PathIsolationOK || !res.ExternalAssetsOK {
		t.Fatalf("unexpected ReviewResult: %+v", res)
	}
}

func TestReviewerFailsClosedWhenMaxTurnsExhaustedWithoutSubmitReview(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-sec-text",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "Looks fine in plain text without calling submit_review.",
				},
			}},
		})
	}))
	defer srv.Close()

	agent := NewAgent(llm.NewClient(srv.URL, "sk-sec-test", 5*time.Second), 32768, stubInspector{
		audit: DiffAudit{ChangedFiles: []string{"sites/coffee-shop/index.html"}, PathIsolationOK: true},
	})

	res, err := agent.Review(context.Background(), contracts.ReviewInput{
		Issue:      contracts.CustomerIssueInput{IssueNumber: 42},
		Spec:       contracts.WebsiteSpecification{SiteSlug: "coffee-shop"},
		TestReport: contracts.TestReport{Passed: true},
	}, "/tmp/wt")
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}
	if res.Approved || len(res.Findings) == 0 {
		t.Fatalf("expected fail-closed ReviewResult when submit_review is never called, got: %+v", res)
	}
}
