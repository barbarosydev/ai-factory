package evaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/barbarosydev/ai-factory/internal/agents/dev"
	"github.com/barbarosydev/ai-factory/internal/agents/tester"
	"github.com/barbarosydev/ai-factory/internal/contracts"
	"github.com/barbarosydev/ai-factory/internal/gitworktree"
)

var (
	externalAttrURLPattern = regexp.MustCompile(`(?i)<(?:link|script|img|iframe|source|video|audio)\b[^>]*(?:href|src)\s*=\s*['"]?(https?://[^'"\s>]+|//[^'"\s>]+)`)
	externalCSSURLPattern  = regexp.MustCompile(`(?i)(?:url\(\s*['"]?|@import\s+['"]?)(https?://[^'"\)\s;]+|//[^'"\)\s;]+)`)
	ansiEscapePattern      = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

// PlaywrightEvaluator executes the shared Playwright suite in ai-factory-websites
// against an isolated git worktree and provides read-only DOM/file inspection tools.
type PlaywrightEvaluator struct {
	websitesRepoPath string
}

// NewPlaywrightEvaluator creates an evaluator anchored to the main ai-factory-websites checkout.
func NewPlaywrightEvaluator(websitesRepoPath string) *PlaywrightEvaluator {
	return &PlaywrightEvaluator{websitesRepoPath: websitesRepoPath}
}

// RunPlaywrightSuite writes spec.json into the worktree and runs tests/site.spec.mjs.
func (e *PlaywrightEvaluator) RunPlaywrightSuite(
	ctx context.Context,
	worktreeDir string,
	spec contracts.WebsiteSpecification,
) (tester.PlaywrightResult, error) {
	if err := gitworktree.WriteSpec(worktreeDir, spec); err != nil {
		return tester.PlaywrightResult{}, fmt.Errorf("write spec.json: %w", err)
	}

	port, err := freeLoopbackPort()
	if err != nil {
		return tester.PlaywrightResult{}, fmt.Errorf("find free loopback port: %w", err)
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, "npx", "playwright", "test", "--reporter=json")
	cmd.Dir = e.websitesRepoPath
	cmd.Env = cleanExecEnv(
		"SITE_SLUG="+spec.SiteSlug,
		"SITE_WORKTREE_ROOT="+worktreeDir,
		"SITE_SERVER_PORT="+strconv.Itoa(port),
		"PW_REPORTER=json",
		"WRITE_EVIDENCE=1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	elapsedMs := time.Since(start).Milliseconds()

	desktopPath := filepath.Join("sites", spec.SiteSlug, "evidence", "desktop.png")
	mobilePath := filepath.Join("sites", spec.SiteSlug, "evidence", "mobile.png")

	parsed, parseErr := parsePlaywrightJSON(stdout.Bytes(), elapsedMs, desktopPath, mobilePath)
	if parseErr != nil {
		if runErr != nil {
			return tester.PlaywrightResult{}, fmt.Errorf("playwright invocation failed (%w): %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return tester.PlaywrightResult{}, fmt.Errorf("parse playwright json: %w", parseErr)
	}
	if runErr != nil && parsed.Passed {
		parsed.Passed = false
		parsed.Failures = append(parsed.Failures, contracts.FailureEvidence{
			CheckName:   "playwright-exit-code",
			Expected:    "exit 0",
			Actual:      runErr.Error(),
			Remediation: strings.TrimSpace(stderr.String()),
		})
	}
	return parsed, nil
}

// InspectDOMSnapshot summarizes the site's HTML landmarks, sections, headings, and links.
func (e *PlaywrightEvaluator) InspectDOMSnapshot(_ context.Context, worktreeDir, slug string) (string, error) {
	rawHTML, err := e.ReadSiteFile(worktreeDir, slug, "index.html")
	if err != nil {
		return "", err
	}
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("parse index.html: %w", err)
	}

	var title string
	var landmarks []string
	var sections []string
	var links []string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			switch tag {
			case "title":
				title = strings.TrimSpace(textContent(n))
			case "header", "nav", "main", "footer":
				landmarks = append(landmarks, tag)
			case "section":
				id := attrVal(n, "id")
				heading := firstHeadingText(n)
				sections = append(sections, fmt.Sprintf("#%s (heading=%q)", id, heading))
			case "a":
				links = append(links, attrVal(n, "href"))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return fmt.Sprintf("title=%q; landmarks=%v; sections=%v; anchors=%v", title, landmarks, sections, links), nil
}

// ReadSiteFile reads a file strictly inside worktreeDir/sites/<slug>/.
func (e *PlaywrightEvaluator) ReadSiteFile(worktreeDir, slug, relPath string) (string, error) {
	if err := contracts.ValidateSiteSlug(slug); err != nil {
		return "", err
	}
	sb, err := dev.OpenSiteSandbox(gitworktree.SiteDir(worktreeDir, slug))
	if err != nil {
		return "", err
	}
	defer func() { _ = sb.Close() }()
	return sb.ReadFile(relPath)
}

// ScanExternalURLs scans HTML/CSS/JS/SVG files in sites/<slug>/ for external asset/script URLs.
func (e *PlaywrightEvaluator) ScanExternalURLs(worktreeDir, slug string) ([]string, error) {
	if err := contracts.ValidateSiteSlug(slug); err != nil {
		return nil, err
	}
	sb, err := dev.OpenSiteSandbox(gitworktree.SiteDir(worktreeDir, slug))
	if err != nil {
		return nil, err
	}
	defer func() { _ = sb.Close() }()

	files, err := sb.ListFiles()
	if err != nil {
		return nil, err
	}

	var found []string
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext != ".html" && ext != ".css" && ext != ".js" && ext != ".svg" {
			continue
		}
		content, err := sb.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range externalAttrURLPattern.FindAllStringSubmatch(content, -1) {
			if len(m) > 1 {
				found = append(found, fmt.Sprintf("%s: %s", f, m[1]))
			}
		}
		for _, m := range externalCSSURLPattern.FindAllStringSubmatch(content, -1) {
			if len(m) > 1 {
				found = append(found, fmt.Sprintf("%s: %s", f, m[1]))
			}
		}
	}
	return found, nil
}

type pwReportJSON struct {
	Suites []pwSuite `json:"suites"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Stats struct {
		Expected   int     `json:"expected"`
		Unexpected int     `json:"unexpected"`
		Flaky      int     `json:"flaky"`
		Duration   float64 `json:"duration"`
	} `json:"stats"`
}

type pwSuite struct {
	Title  string    `json:"title"`
	Suites []pwSuite `json:"suites"`
	Specs  []struct {
		Title string `json:"title"`
		OK    bool   `json:"ok"`
		Tests []struct {
			Status  string `json:"status"`
			Results []struct {
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			} `json:"results"`
		} `json:"tests"`
	} `json:"specs"`
}

func parsePlaywrightJSON(raw []byte, elapsedMs int64, desktopPng, mobilePng string) (tester.PlaywrightResult, error) {
	trimmed := bytes.TrimSpace(raw)
	startIdx := bytes.IndexByte(trimmed, '{')
	if startIdx < 0 {
		return tester.PlaywrightResult{}, fmt.Errorf("no JSON object found in playwright output")
	}

	var report pwReportJSON
	if err := json.Unmarshal(trimmed[startIdx:], &report); err != nil {
		return tester.PlaywrightResult{}, err
	}

	var checks []contracts.SingleCheck
	var failures []contracts.FailureEvidence

	var walkSuite func(pwSuite)
	walkSuite = func(s pwSuite) {
		for _, spec := range s.Specs {
			check := contracts.SingleCheck{Name: spec.Title, Passed: spec.OK}
			if !spec.OK {
				var errMsgs []string
				for _, t := range spec.Tests {
					for _, r := range t.Results {
						if r.Error != nil && r.Error.Message != "" {
							errMsgs = append(errMsgs, cleanANSI(r.Error.Message))
						}
						for _, e := range r.Errors {
							if e.Message != "" {
								errMsgs = append(errMsgs, cleanANSI(e.Message))
							}
						}
					}
				}
				detail := strings.Join(errMsgs, " | ")
				if detail == "" {
					detail = "Playwright assertion failed"
				}
				check.Detail = detail
				failures = append(failures, contracts.FailureEvidence{
					CheckName:   spec.Title,
					Expected:    "All 8 Playwright verification gates pass",
					Actual:      detail,
					Remediation: "Fix the failing assertion in index.html or styles.css and re-verify.",
				})
			}
			checks = append(checks, check)
		}
		for _, child := range s.Suites {
			walkSuite(child)
		}
	}
	for _, s := range report.Suites {
		walkSuite(s)
	}

	for _, topErr := range report.Errors {
		msg := cleanANSI(topErr.Message)
		failures = append(failures, contracts.FailureEvidence{
			CheckName:   "playwright-suite-error",
			Expected:    "Playwright suite loads spec.json and runs cleanly",
			Actual:      msg,
			Remediation: msg,
		})
	}

	passed := report.Stats.Unexpected == 0 && len(report.Errors) == 0 && len(failures) == 0 && len(checks) > 0
	return tester.PlaywrightResult{
		Passed:            passed,
		DurationMs:        elapsedMs,
		Checks:            checks,
		Failures:          failures,
		DesktopScreenshot: desktopPng,
		MobileScreenshot:  mobilePng,
	}, nil
}

func cleanANSI(s string) string {
	return strings.TrimSpace(ansiEscapePattern.ReplaceAllString(s, ""))
}

// cleanExecEnv strips secret environment variables (LiteLLM keys, GitHub App keys/tokens)
// before spawning external verification subprocesses such as npx playwright.
func cleanExecEnv(extra ...string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "LITELLM_") ||
			strings.HasPrefix(upper, "GITHUB_APP_") ||
			strings.HasPrefix(upper, "GITHUB_INSTALLATION_") ||
			strings.HasPrefix(upper, "AI_FACTORY_GIT_") ||
			strings.HasSuffix(upper, "_KEY") ||
			strings.HasSuffix(upper, "_SECRET") ||
			strings.HasSuffix(upper, "_TOKEN") ||
			strings.HasSuffix(upper, "_PASSWORD") ||
			strings.HasSuffix(upper, "_PEM") ||
			strings.Contains(upper, "PRIVATE_KEY") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, extra...)
}

func freeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener addr type %T", ln.Addr())
	}
	return addr.Port, nil
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textContent(c))
	}
	return b.String()
}

func firstHeadingText(n *html.Node) string {
	if n.Type == html.ElementNode {
		switch strings.ToLower(n.Data) {
		case "h1", "h2", "h3":
			return strings.TrimSpace(textContent(n))
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := firstHeadingText(c); t != "" {
			return t
		}
	}
	return ""
}
