package dev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/barbarosydev/ai-factory/internal/agents/llm"
	"github.com/barbarosydev/ai-factory/internal/contracts"
)

func testSpec() contracts.WebsiteSpecification {
	return contracts.WebsiteSpecification{
		SchemaVersion: "v1",
		SiteSlug:      "kaffeekultur-berlin",
		Operation:     "create",
		Category:      "coffee-shop",
		PageTitle:     "Kaffeekultur Berlin",
		Tagline:       "Artisan coffee in Kreuzberg",
		Theme: contracts.ThemeSpec{
			PrimaryColor:    "#4A2C2A",
			BackgroundColor: "#FAF7F2",
			AccentColor:     "#D4A373",
			FontPairing:     "system-sans",
		},
		Navigation: []contracts.NavLinkSpec{{Label: "Menu", Href: "#menu"}},
		Sections: []contracts.SectionSpec{
			{ID: "hero", Heading: "Kaffeekultur", Purpose: "Hero"},
			{ID: "menu", Heading: "Menu", Purpose: "Drinks"},
			{ID: "hours", Heading: "Hours", Purpose: "Schedule"},
		},
		FooterText: "Kaffeekultur Berlin",
	}
}

func TestSiteSandboxRejectsEscapesAndBadExtensions(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	siteDir := filepath.Join(parent, "sites", "coffee-shop")
	outsideSecret := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(outsideSecret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	sb, err := OpenSiteSandbox(siteDir)
	if err != nil {
		t.Fatalf("OpenSiteSandbox: %v", err)
	}
	defer func() { _ = sb.Close() }()

	// Create a symlink inside siteDir pointing outside
	_ = os.Symlink(outsideSecret, filepath.Join(siteDir, "escape.html"))

	for _, bad := range []string{
		"../../secret.txt",
		"../secret.txt",
		"/etc/passwd",
		".git/config",
		".env",
		"escape.html",
	} {
		if _, err := sb.ReadFile(bad); err == nil {
			t.Errorf("ReadFile(%q) should have been blocked", bad)
		}
		if _, err := sb.WriteFile(bad, "pwned"); err == nil {
			t.Errorf("WriteFile(%q) should have been blocked", bad)
		}
	}

	for _, badExt := range []string{"run.sh", "payload.py", "bin.exe"} {
		if _, err := sb.WriteFile(badExt, "echo hi"); err == nil {
			t.Errorf("WriteFile(%q) should have rejected extension", badExt)
		}
	}

	if _, err := sb.WriteFile("index.html", "<h1>Valid</h1>"); err != nil {
		t.Fatalf("WriteFile(index.html) failed: %v", err)
	}
	got, err := sb.ReadFile("index.html")
	if err != nil || got != "<h1>Valid</h1>" {
		t.Fatalf("ReadFile(index.html) = %q, %v", got, err)
	}
}

func TestCheckSiteStructureCatchesMissingIDAndExternalURL(t *testing.T) {
	t.Parallel()

	sb, err := OpenSiteSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	_, _ = sb.WriteFile("index.html", `<!DOCTYPE html>
<html>
<head><meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="stylesheet" href="https://cdn.example.com/bad.css">
</head>
<body><header><nav><a href="#missing">Bad</a></nav></header><main><section id="hero"><h1>Hi</h1></section></main><footer>F</footer></body>
</html>`)

	res := sb.CheckSiteStructure(testSpec())
	if res.Passed {
		t.Fatal("expected CheckSiteStructure to fail")
	}
	if len(res.Errors) < 3 {
		t.Errorf("expected multiple structural errors (external URL, missing #menu/#hours, broken #missing link), got: %v", res.Errors)
	}
}

func TestDeveloperAgentMultiTurnLoop(t *testing.T) {
	t.Parallel()

	var turn atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := turn.Add(1)
		var tc []llm.ToolCall
		switch n {
		case 1:
			argsHTML, _ := json.Marshal(map[string]string{
				"path": "index.html",
				"content": `<!DOCTYPE html>
<html lang="en">
<head><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Kaffeekultur</title><link rel="stylesheet" href="styles.css"></head>
<body><header><nav><a href="#menu">Menu</a></nav></header><main><section id="hero"><h1>Kaffeekultur</h1></section><section id="menu"><h2>Menu</h2></section><section id="hours"><h2>Hours</h2></section></main><footer>Kaffeekultur</footer></body>
</html>`,
			})
			argsCSS, _ := json.Marshal(map[string]string{
				"path":    "styles.css",
				"content": `body { margin: 0; font-family: sans-serif; }`,
			})
			tc = []llm.ToolCall{
				{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "write_file", Arguments: string(argsHTML)}},
				{ID: "c2", Type: "function", Function: llm.FunctionCall{Name: "write_file", Arguments: string(argsCSS)}},
			}
		case 2:
			tc = []llm.ToolCall{
				{ID: "c3", Type: "function", Function: llm.FunctionCall{Name: "check_site_structure", Arguments: `{}`}},
			}
		default:
			tc = []llm.ToolCall{
				{ID: "c4", Type: "function", Function: llm.FunctionCall{Name: "finish", Arguments: `{"summary":"Implemented coffee shop site"}`}},
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-dev",
			"choices": []map[string]any{
				{"index": 0, "message": map[string]any{"role": "assistant", "tool_calls": tc}},
			},
		})
	}))
	defer srv.Close()

	client := llm.NewClient(srv.URL, "sk-dev-test", 5*time.Second)
	agent := NewAgent(client, 32768)

	var heartbeats int
	res, err := agent.Implement(context.Background(), contracts.DeveloperTaskInput{
		IssueNumber: 42,
		Attempt:     1,
		Spec:        testSpec(),
	}, filepath.Join(t.TempDir(), "kaffeekultur-berlin"), func(string) {
		heartbeats++
	})
	if err != nil {
		t.Fatalf("Implement failed: %v", err)
	}
	if res.TurnsUsed != 3 || heartbeats != 3 {
		t.Errorf("TurnsUsed=%d heartbeats=%d, want 3/3", res.TurnsUsed, heartbeats)
	}
	if len(res.FilesChanged) != 2 {
		t.Errorf("FilesChanged=%v, want [index.html styles.css]", res.FilesChanged)
	}
}

func TestScaffoldSiteFromSpecSkill(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tc := []llm.ToolCall{
			{ID: "s1", Type: "function", Function: llm.FunctionCall{Name: "scaffold_site_from_spec", Arguments: `{"auto_finish":true,"summary":"Scaffolded Berlin coffee shop"}`}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-scaffold",
			"choices": []map[string]any{
				{"index": 0, "message": map[string]any{"role": "assistant", "tool_calls": tc}},
			},
		})
	}))
	defer srv.Close()

	spec := testSpec()
	spec.Sections[0].RequiredKeywords = []string{"Kreuzberg", "Specialty"}
	spec.Sections[1].RequiredKeywords = []string{"Espresso", "Flat White"}
	spec.Sections[1].RequiredElements = []string{"h2", "ul", "table"}

	client := llm.NewClient(srv.URL, "sk-dev-test", 5*time.Second)
	agent := NewAgent(client, 32768)

	res, err := agent.Implement(context.Background(), contracts.DeveloperTaskInput{
		IssueNumber: 5,
		Attempt:     1,
		Spec:        spec,
	}, filepath.Join(t.TempDir(), "kaffeekultur-berlin"), nil)
	if err != nil {
		t.Fatalf("Implement with scaffold_site_from_spec failed: %v", err)
	}
	if res.TurnsUsed != 1 {
		t.Errorf("TurnsUsed=%d, want 1", res.TurnsUsed)
	}
	if len(res.FilesChanged) != 3 {
		t.Errorf("FilesChanged=%v, want [index.html script.js styles.css]", res.FilesChanged)
	}
}

func TestCheckSiteStructureRejectsLeakedPurposeCopy(t *testing.T) {
	t.Parallel()

	sb, err := OpenSiteSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()

	spec := testSpec()
	spec.Sections[0].Purpose = "Welcome visitors with the roastery tagline and primary call to action."

	_, _ = sb.WriteFile("index.html", `<!DOCTYPE html>
<html>
<head><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Kaffeekultur</title></head>
<body>
  <header><nav><a href="#menu">Menu</a></nav></header>
  <main>
    <section id="hero"><h1>Kaffeekultur</h1><p>Welcome visitors with the roastery tagline and primary call to action.</p></section>
    <section id="menu"><h2>Menu</h2></section>
    <section id="hours"><h2>Hours</h2></section>
  </main>
  <footer>Kaffeekultur Berlin</footer>
</body>
</html>`)

	res := sb.CheckSiteStructure(spec)
	if res.Passed {
		t.Fatal("expected CheckSiteStructure to reject leaked spec.purpose instruction text")
	}
}
