package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/barbarosydev/ai-factory/internal/config"
)

func TestDoctorLiteLLMAndOllamaChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			switch r.Header.Get("Authorization") {
			case "Bearer sk-pm":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "pm-agent"}}})
			case "Bearer sk-dev":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "dev-agent"}}})
			case "Bearer sk-test":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "test-agent"}}})
			case "Bearer sk-sec":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "sec-agent"}}})
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]string{
					{"name": "gemma4:12b"},
					{"name": "gemma4:e4b"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resLLM := checkLiteLLM(context.Background(), srv.URL+"/v1", map[string]string{
		"pm-agent":   "sk-pm",
		"dev-agent":  "sk-dev",
		"test-agent": "sk-test",
		"sec-agent":  "sk-sec",
	})
	require.Equal(t, "PASS", resLLM.Status)

	resOllama := checkOllama(context.Background(), srv.URL)
	require.Equal(t, "PASS", resOllama.Status)
}

func TestDoctorWebsitesRepoCheck(t *testing.T) {
	tmp := t.TempDir()
	require.Equal(t, "FAIL", checkWebsitesRepo(tmp).Status)

	require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "tests"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "tests", "site.spec.mjs"), []byte("// ok\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "node_modules", "@playwright", "test"), 0o755))

	require.Equal(t, "PASS", checkWebsitesRepo(tmp).Status)
}

func TestDoctorPhoenixCheck(t *testing.T) {
	ctx := context.Background()

	disabledRes := checkPhoenix(ctx, config.Config{OTelDisabled: true})
	require.Equal(t, "PASS", disabledRes.Status)
	require.Contains(t, disabledRes.Detail, "OTEL_SDK_DISABLED=true")

	readyOK := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" && readyOK {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	passMeta := checkPhoenix(ctx, config.Config{
		PhoenixUIURL:    srv.URL,
		OTelProjectName: "ai-factory",
	})
	require.Equal(t, "PASS", passMeta.Status)
	require.Contains(t, passMeta.Detail, "mode=metadata-only")

	passContent := checkPhoenix(ctx, config.Config{
		PhoenixUIURL:       srv.URL,
		OTelProjectName:    "ai-factory",
		OTelCaptureContent: true,
	})
	require.Equal(t, "PASS", passContent.Status)
	require.Contains(t, passContent.Detail, "mode=content-capture-enabled")

	readyOK = false
	failRes := checkPhoenix(ctx, config.Config{
		PhoenixUIURL:    srv.URL,
		OTelProjectName: "ai-factory",
	})
	require.Equal(t, "FAIL", failRes.Status)
}
