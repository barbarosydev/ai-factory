package telemetry

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRegistryProbesAndPrometheusOutput(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	h := reg.Handler()

	// Before worker starts, /healthz is 200 and /readyz is 503.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	reg.SetReady(true)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	reg.RecordAgentCall("pm", "pm-agent", 500, 1200, 18*time.Second, nil)
	reg.RecordPlaywrightRun(true, 1450)
	reg.RecordWorkflow("done", 95*time.Second, 1)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `ai_factory_ready 1`)
	require.Contains(t, body, `ai_factory_workflows_total{final_state="done"} 1`)
	require.Contains(t, body, `ai_factory_agent_calls_total{role="pm",model="pm-agent",status="ok"} 1`)
	require.Contains(t, body, `ai_factory_agent_tokens_total{role="pm",model="pm-agent",kind="prompt"} 500`)
	require.Contains(t, body, `ai_factory_agent_tokens_total{role="pm",model="pm-agent",kind="completion"} 1200`)
	require.Contains(t, body, `ai_factory_playwright_runs_total{passed="true"} 1`)
	require.Contains(t, body, `ai_factory_repair_attempts_total 1`)
}

func TestStartServerAndJSONLogger(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := NewLogger("json", &logBuf)
	reg := NewRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := StartServer(ctx, "127.0.0.1:0", reg, logger)
	require.NoError(t, err)
	defer stop()

	require.Contains(t, logBuf.String(), `"msg":"observability server listening"`)
}
