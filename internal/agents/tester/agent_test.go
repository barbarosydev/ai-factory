package tester

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

type stubBackend struct {
	pwResult PlaywrightResult
}

func (s stubBackend) RunPlaywrightSuite(context.Context, string, contracts.WebsiteSpecification) (PlaywrightResult, error) {
	return s.pwResult, nil
}
func (s stubBackend) InspectDOMSnapshot(context.Context, string, string) (string, error) {
	return "sections: #hero, #menu, #hours; mobile scrollWidth=440 > 375", nil
}
func (s stubBackend) ReadSiteFile(string, string, string) (string, error) {
	return ".hours-table { width: 440px; }", nil
}

func TestTesterAgentCannotOverrideFailedPlaywright(t *testing.T) {
	t.Parallel()

	var turn atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := turn.Add(1)
		var tc []llm.ToolCall
		switch n {
		case 1:
			tc = []llm.ToolCall{{ID: "t1", Type: "function", Function: llm.FunctionCall{Name: "run_playwright_suite", Arguments: `{}`}}}
		case 2:
			tc = []llm.ToolCall{{ID: "t2", Type: "function", Function: llm.FunctionCall{Name: "read_site_file", Arguments: `{"path":"styles.css"}`}}}
		default:
			args, _ := json.Marshal(map[string]any{
				"semantic_quality_ok": true,
				"tester_summary":      "Found fixed width on .hours-table causing mobile overflow.",
				"failures": []contracts.FailureEvidence{{
					CheckName:   "responsive-mobile-overflow",
					Expected:    "scrollWidth <= 375",
					Actual:      "scrollWidth = 440",
					FileHint:    "styles.css",
					Remediation: "Replace width: 440px on .hours-table with max-width: 100%.",
				}},
			})
			tc = []llm.ToolCall{{ID: "t3", Type: "function", Function: llm.FunctionCall{Name: "submit_test_report", Arguments: string(args)}}}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "tool_calls": tc}}},
		})
	}))
	defer srv.Close()

	client := llm.NewClient(srv.URL, "sk-test-key", 5*time.Second)
	agent := NewAgent(client, 32768, stubBackend{
		pwResult: PlaywrightResult{
			Passed:     false,
			DurationMs: 1850,
			Checks:     []contracts.SingleCheck{{Name: "responsive-mobile", Passed: false}},
			Failures:   []contracts.FailureEvidence{{CheckName: "responsive-mobile", Expected: "<=375", Actual: "440", Remediation: "Fix CSS"}},
		},
	})

	report, err := agent.Verify(context.Background(), contracts.TestRunInput{
		IssueNumber: 42,
		Attempt:     1,
		Spec: contracts.WebsiteSpecification{
			SiteSlug: "kaffeekultur-berlin",
		},
	}, "/tmp/wt", nil)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if report.Passed {
		t.Fatal("expected report.Passed=false when Playwright failed")
	}
	if len(report.Failures) != 2 {
		t.Fatalf("expected both Playwright failure and Tester diagnosis to be preserved, got: %+v", report.Failures)
	}
	if report.Failures[1].FileHint != "styles.css" {
		t.Fatalf("expected Tester Agent root-cause FileHint, got: %+v", report.Failures)
	}
}

func TestTesterAgentFailsClosedWhenSubmitTestReportNeverCalled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test-noop",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "Looks good"}}},
		})
	}))
	defer srv.Close()

	agent := NewAgent(llm.NewClient(srv.URL, "sk-test-key", 5*time.Second), 32768, stubBackend{
		pwResult: PlaywrightResult{Passed: true, DurationMs: 900},
	})

	report, err := agent.Verify(context.Background(), contracts.TestRunInput{
		IssueNumber: 43,
		Attempt:     1,
		Spec:        contracts.WebsiteSpecification{SiteSlug: "kaffeekultur-berlin"},
	}, "/tmp/wt", nil)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if report.Passed || report.SemanticQualityOK || len(report.Failures) == 0 {
		t.Fatalf("expected fail-closed TestReport when submit_test_report is not called, got: %+v", report)
	}
}
