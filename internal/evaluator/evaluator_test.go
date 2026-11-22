package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePlaywrightJSONPassAndFail(t *testing.T) {
	t.Parallel()

	passJSON := []byte(`{
		"stats": {"expected": 1, "unexpected": 0, "flaky": 0, "duration": 900},
		"suites": [{
			"title": "tests/site.spec.mjs",
			"suites": [{
				"title": "Site verification: coffee-shop",
				"specs": [{"title": "passes all 8 deterministic gates", "ok": true, "tests": [{"status": "expected"}]}]
			}]
		}]
	}`)

	res, err := parsePlaywrightJSON(passJSON, 950, "sites/coffee-shop/evidence/desktop.png", "sites/coffee-shop/evidence/mobile.png")
	if err != nil {
		t.Fatalf("parse passJSON: %v", err)
	}
	if !res.Passed || len(res.Checks) != 1 || len(res.Failures) != 0 {
		t.Fatalf("unexpected pass result: %+v", res)
	}

	failJSON := []byte(`{
		"stats": {"expected": 0, "unexpected": 1, "flaky": 0, "duration": 1200},
		"suites": [{
			"title": "tests/site.spec.mjs",
			"specs": [{
				"title": "passes all 8 deterministic gates",
				"ok": false,
				"tests": [{
					"status": "unexpected",
					"results": [{
						"status": "failed",
						"error": {"message": "Error: Mobile horizontal overflow: scrollWidth=440 > innerWidth=375"}
					}]
				}]
			}]
		}]
	}`)

	failRes, err := parsePlaywrightJSON(failJSON, 1200, "d.png", "m.png")
	if err != nil {
		t.Fatalf("parse failJSON: %v", err)
	}
	if failRes.Passed || len(failRes.Failures) != 1 {
		t.Fatalf("expected failed result with 1 failure, got: %+v", failRes)
	}
	if !strings.Contains(failRes.Failures[0].Actual, "scrollWidth=440") {
		t.Errorf("unexpected failure detail: %q", failRes.Failures[0].Actual)
	}
}

func TestInspectDOMAndScanExternalURLs(t *testing.T) {
	t.Parallel()

	wt := t.TempDir()
	siteDir := filepath.Join(wt, "sites", "bakery-mitte")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "index.html"), []byte(`<!DOCTYPE html>
<html><head><title>Bäckerei Mitte</title><script src="https://evil.example/a.js"></script></head>
<body><header><nav><a href="#menu">Menu</a></nav></header><main><section id="menu"><h2>Menu</h2></section></main><footer>F</footer></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "styles.css"), []byte(`body { background: url("https://cdn.example/bg.png"); }`), 0o644); err != nil {
		t.Fatal(err)
	}

	ev := NewPlaywrightEvaluator(wt)
	snap, err := ev.InspectDOMSnapshot(context.Background(), wt, "bakery-mitte")
	if err != nil {
		t.Fatalf("InspectDOMSnapshot: %v", err)
	}
	if !strings.Contains(snap, "Bäckerei Mitte") || !strings.Contains(snap, "#menu") {
		t.Errorf("unexpected DOM snapshot: %s", snap)
	}

	urls, err := ev.ScanExternalURLs(wt, "bakery-mitte")
	if err != nil {
		t.Fatalf("ScanExternalURLs: %v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("expected 2 external URLs detected, got %v", urls)
	}
}

func TestCleanExecEnvStripsSecrets(t *testing.T) {
	t.Setenv("LITELLM_PM_KEY", "sk-pm-secret")
	t.Setenv("LITELLM_DEV_KEY", "sk-dev-secret")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_BASE64", "c2VjcmV0")
	t.Setenv("AI_FACTORY_GIT_TOKEN", "ghs_secret")
	t.Setenv("CUSTOM_API_TOKEN", "tok_secret")

	env := cleanExecEnv("SITE_SLUG=coffee-shop", "WRITE_EVIDENCE=1")
	joined := strings.Join(env, "\n")

	for _, forbidden := range []string{
		"LITELLM_PM_KEY=",
		"LITELLM_DEV_KEY=",
		"GITHUB_APP_PRIVATE_KEY_BASE64=",
		"AI_FACTORY_GIT_TOKEN=",
		"CUSTOM_API_TOKEN=",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("cleanExecEnv leaked secret env var %q", forbidden)
		}
	}
	if !strings.Contains(joined, "SITE_SLUG=coffee-shop") || !strings.Contains(joined, "WRITE_EVIDENCE=1") {
		t.Errorf("cleanExecEnv lost explicit extra vars: %s", joined)
	}
}
