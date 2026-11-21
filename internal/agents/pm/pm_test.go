package pm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/contracts"
)

type stubStore struct {
	sites []string
	specs map[string]string
}

func (s stubStore) ListExistingSites() ([]string, error) { return s.sites, nil }
func (s stubStore) ReadExistingSpec(slug string) (string, error) {
	return s.specs[slug], nil
}

func TestAnalyzeIssueRetriesInvalidOutputThenPasses(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var reply string
		if n == 1 {
			// Invalid: ready with nil spec triggers validation feedback retry
			reply = `{"status":"ready"}`
		} else {
			spec := contracts.PMDecisionOutput{
				Status: "ready",
				Spec: &contracts.WebsiteSpecification{
					SchemaVersion: "v1",
					SiteSlug:      "kaffeekultur-berlin",
					Operation:     "create",
					Category:      "coffee-shop",
					PageTitle:     "Kaffeekultur Berlin",
					Tagline:       "Artisan espresso in Kreuzberg",
					Theme: contracts.ThemeSpec{
						PrimaryColor:    "#4A2C2A",
						BackgroundColor: "#FAF7F2",
						AccentColor:     "#D4A373",
						FontPairing:     "system-sans",
					},
					Navigation: []contracts.NavLinkSpec{{Label: "Menu", Href: "#menu"}},
					Sections: []contracts.SectionSpec{
						{ID: "hero", Heading: "Kaffeekultur", Purpose: "Hero", RequiredKeywords: []string{"Berlin"}},
						{ID: "menu", Heading: "Menu", Purpose: "Drinks", RequiredKeywords: []string{"Espresso"}},
						{ID: "hours", Heading: "Hours", Purpose: "Hours", RequiredKeywords: []string{"Monday"}},
					},
					FooterText: "Kaffeekultur Berlin",
				},
			}
			b, _ := json.Marshal(spec)
			reply = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id": "chatcmpl-pm",
			"choices": []map[string]any{
				{"index": 0, "message": map[string]any{"role": "assistant", "content": reply}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := llm.NewClient(srv.URL, "sk-pm-test", 5*time.Second)
	agent := NewAgent(client, 32768, stubStore{sites: []string{"bakery-mitte"}})

	out, err := agent.AnalyzeIssue(context.Background(), contracts.CustomerIssueInput{
		IssueNumber: 42,
		IssueTitle:  "Coffee shop in Berlin",
		IssueBody:   "Create a coffee shop site.",
		RequestType: "new_website",
	})
	if err != nil {
		t.Fatalf("AnalyzeIssue failed: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 LLM calls (1 retry), got %d", calls.Load())
	}
	if out.Status != "ready" || out.Spec == nil || out.Spec.SiteSlug != "kaffeekultur-berlin" {
		t.Fatalf("unexpected output: %+v", out)
	}
}
