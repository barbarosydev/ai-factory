package dev

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"

	"github.com/barbarosydev/ai-factory/internal/contracts"
)

const (
	maxReadBytes  = 64 * 1024
	maxWriteBytes = 512 * 1024
)

var allowedExtensions = map[string]bool{
	".html": true,
	".css":  true,
	".js":   true,
	".svg":  true,
	".json": true,
}

var externalCSSURLPattern = regexp.MustCompile(`(?i)url\(\s*['"]?https?://`)

// SiteSandbox confines all Developer Agent filesystem tools strictly to a single site directory.
type SiteSandbox struct {
	rootDir string
	root    *os.Root
}

// OpenSiteSandbox opens a path-jailed os.Root anchored at siteDir.
func OpenSiteSandbox(siteDir string) (*SiteSandbox, error) {
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure site directory: %w", err)
	}
	root, err := os.OpenRoot(siteDir)
	if err != nil {
		return nil, fmt.Errorf("open root %s: %w", siteDir, err)
	}
	return &SiteSandbox{rootDir: siteDir, root: root}, nil
}

// Close closes the underlying os.Root handle.
func (s *SiteSandbox) Close() error {
	if s.root != nil {
		return s.root.Close()
	}
	return nil
}

// sanitizeRelPath validates a relative path inside the site directory without allowing traversal or dotfiles.
func sanitizeRelPath(raw string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." {
		return "", fmt.Errorf("path must not be empty")
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(raw) {
		return "", fmt.Errorf("absolute paths are forbidden: %q", raw)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path traversal forbidden: %q", raw)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("hidden or invalid path segment in %q", raw)
		}
	}
	return clean, nil
}

// ListFiles returns all non-hidden files inside the sandboxed site directory.
func (s *SiteSandbox) ListFiles() ([]string, error) {
	var files []string
	err := fs.WalkDir(s.root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// ReadFile reads up to 64 KiB of a file strictly inside the sandbox.
func (s *SiteSandbox) ReadFile(rel string) (string, error) {
	clean, err := sanitizeRelPath(rel)
	if err != nil {
		return "", err
	}
	f, err := s.root.Open(clean)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", clean, err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %q: %w", clean, err)
	}
	if len(data) > maxReadBytes {
		return "", fmt.Errorf("file %q exceeds %d bytes", clean, maxReadBytes)
	}
	return string(data), nil
}

// WriteFile writes a file with an allowlisted static web extension inside the sandbox.
func (s *SiteSandbox) WriteFile(rel, content string) (int, error) {
	clean, err := sanitizeRelPath(rel)
	if err != nil {
		return 0, err
	}
	ext := strings.ToLower(path.Ext(clean))
	if !allowedExtensions[ext] {
		return 0, fmt.Errorf("file extension %q is not allowed (allowed: .html, .css, .js, .svg, .json)", ext)
	}
	if len(content) > maxWriteBytes {
		return 0, fmt.Errorf("content exceeds max size of %d bytes", maxWriteBytes)
	}

	if dir := path.Dir(clean); dir != "." {
		if err := s.mkdirAll(dir); err != nil {
			return 0, err
		}
	}

	f, err := s.root.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("write %q: %w", clean, err)
	}
	defer func() { _ = f.Close() }()

	n, err := f.WriteString(content)
	if err != nil {
		return 0, fmt.Errorf("write %q: %w", clean, err)
	}
	return n, nil
}

// ScaffoldFromSpec synthesizes a responsive, accessible HTML5 + CSS3 + vanilla JS website
// directly from WebsiteSpecification inside the sandboxed directory.
func (s *SiteSandbox) ScaffoldFromSpec(spec contracts.WebsiteSpecification) ([]string, StructureCheckResult, error) {
	indexHTML := buildScaffoldHTML(spec)
	stylesCSS := buildScaffoldCSS(spec)
	scriptJS := buildScaffoldJS()

	if _, err := s.WriteFile("index.html", indexHTML); err != nil {
		return nil, StructureCheckResult{}, err
	}
	if _, err := s.WriteFile("styles.css", stylesCSS); err != nil {
		return nil, StructureCheckResult{}, err
	}
	if _, err := s.WriteFile("script.js", scriptJS); err != nil {
		return nil, StructureCheckResult{}, err
	}

	files := []string{"index.html", "script.js", "styles.css"}
	check := s.CheckSiteStructure(spec)
	return files, check, nil
}

func (s *SiteSandbox) mkdirAll(relDir string) error {
	parts := strings.Split(relDir, "/")
	cur := ""
	for _, seg := range parts {
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		if err := s.root.Mkdir(cur, 0o755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("mkdir %q: %w", cur, err)
		}
	}
	return nil
}

// StructureCheckResult holds deterministic HTML/asset check errors.
type StructureCheckResult struct {
	Passed bool     `json:"passed"`
	Errors []string `json:"errors,omitempty"`
}

// CheckSiteStructure performs fast deterministic HTML5, section ID, keyword, and local asset validation.
func (s *SiteSandbox) CheckSiteStructure(spec contracts.WebsiteSpecification) StructureCheckResult {
	var errs []string

	rawHTML, err := s.ReadFile("index.html")
	if err != nil {
		return StructureCheckResult{
			Passed: false,
			Errors: []string{"index.html is missing; call scaffold_site_from_spec or write_file to create index.html"},
		}
	}

	if css, err := s.ReadFile("styles.css"); err == nil {
		if externalCSSURLPattern.MatchString(css) {
			errs = append(errs, "styles.css contains an external http(s):// url(...); remove external URLs")
		}
	}

	doc, err := html.Parse(bytes.NewBufferString(rawHTML))
	if err != nil {
		return StructureCheckResult{
			Passed: false,
			Errors: []string{fmt.Sprintf("index.html failed HTML parse: %v", err)},
		}
	}

	var hasDoctype, hasViewport, hasHeaderOrNav, hasMain, hasFooter bool
	ids := make(map[string]bool)
	idText := make(map[string]string)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.DoctypeNode && strings.EqualFold(n.Data, "html") {
			hasDoctype = true
		}
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "header", "nav":
				hasHeaderOrNav = true
			case "main":
				hasMain = true
			case "footer":
				hasFooter = true
			case "meta":
				if strings.EqualFold(attr(n, "name"), "viewport") && strings.Contains(attr(n, "content"), "width=device-width") {
					hasViewport = true
				}
			case "link":
				if href := attr(n, "href"); href != "" {
					checkAssetRef(s, "link href", href, &errs)
				}
			case "script":
				if src := attr(n, "src"); src != "" {
					checkAssetRef(s, "script src", src, &errs)
				}
			case "img":
				if src := attr(n, "src"); src != "" {
					checkAssetRef(s, "img src", src, &errs)
				}
			}
			if id := attr(n, "id"); id != "" {
				ids[id] = true
				idText[id] = strings.ToLower(extractNodeText(n))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if !hasDoctype {
		errs = append(errs, "index.html is missing <!DOCTYPE html>")
	}
	if !hasViewport {
		errs = append(errs, `index.html is missing <meta name="viewport" content="width=device-width, initial-scale=1.0">`)
	}
	if !hasHeaderOrNav {
		errs = append(errs, "index.html must include a <header> or <nav> landmark")
	}
	if !hasMain {
		errs = append(errs, "index.html must include a <main> landmark")
	}
	if !hasFooter {
		errs = append(errs, "index.html must include a <footer> landmark")
	}

	for _, sec := range spec.Sections {
		if !ids[sec.ID] {
			errs = append(errs, fmt.Sprintf("missing element with id=%q for section %q", sec.ID, sec.Heading))
			continue
		}
		secLower := idText[sec.ID]
		for _, kw := range sec.RequiredKeywords {
			if kw != "" && !strings.Contains(secLower, strings.ToLower(kw)) {
				errs = append(errs, fmt.Sprintf("section #%s is missing required_keyword %q", sec.ID, kw))
			}
		}
	}

	// Check internal anchor links point to existing IDs
	var checkLinks func(*html.Node)
	checkLinks = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "a") {
			if href := attr(n, "href"); strings.HasPrefix(href, "#") && len(href) > 1 {
				target := href[1:]
				if !ids[target] {
					errs = append(errs, fmt.Sprintf("broken anchor link href=%q: no element with id=%q exists", href, target))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			checkLinks(c)
		}
	}
	checkLinks(doc)

	return StructureCheckResult{
		Passed: len(errs) == 0,
		Errors: errs,
	}
}

func extractNodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
			b.WriteByte(' ')
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func checkAssetRef(s *SiteSandbox, kind, ref string, errs *[]string) {
	lower := strings.ToLower(strings.TrimSpace(ref))
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "//") {
		*errs = append(*errs, fmt.Sprintf("%s=%q references an external URL; use local files or inline SVG only", kind, ref))
		return
	}
	if strings.HasPrefix(lower, "data:") {
		return
	}
	clean, err := sanitizeRelPath(ref)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s=%q is invalid: %v", kind, ref, err))
		return
	}
	if _, err := s.root.Stat(clean); err != nil {
		*errs = append(*errs, fmt.Sprintf("%s=%q does not exist on disk yet", kind, clean))
	}
}

func buildScaffoldHTML(spec contracts.WebsiteSpecification) string {
	esc := template.HTMLEscapeString
	sectionIDs := make(map[string]bool, len(spec.Sections))
	for _, sec := range spec.Sections {
		sectionIDs[sec.ID] = true
	}

	var navLinks []contracts.NavLinkSpec
	for _, nav := range spec.Navigation {
		href := strings.TrimSpace(nav.Href)
		if strings.HasPrefix(href, "#") && len(href) > 1 && sectionIDs[href[1:]] {
			navLinks = append(navLinks, contracts.NavLinkSpec{Label: nav.Label, Href: href})
		}
	}
	if len(navLinks) == 0 {
		for _, sec := range spec.Sections {
			navLinks = append(navLinks, contracts.NavLinkSpec{Label: sec.Heading, Href: "#" + sec.ID})
		}
	}

	ctaTarget := "#top"
	if len(spec.Sections) > 1 {
		ctaTarget = "#" + spec.Sections[1].ID
	} else if len(spec.Sections) == 1 {
		ctaTarget = "#" + spec.Sections[0].ID
	}

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("  <meta charset=\"UTF-8\">\n")
	b.WriteString("  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n")
	fmt.Fprintf(&b, "  <title>%s</title>\n", esc(spec.PageTitle))
	b.WriteString("  <link rel=\"stylesheet\" href=\"styles.css\">\n")
	b.WriteString("</head>\n<body id=\"top\">\n")
	b.WriteString("  <header class=\"site-header\">\n")
	b.WriteString("    <div class=\"container header-inner\">\n")
	fmt.Fprintf(&b, "      <a class=\"brand\" href=\"#top\">%s</a>\n", esc(spec.PageTitle))
	b.WriteString("      <nav class=\"site-nav\" aria-label=\"Main navigation\">\n")
	b.WriteString("        <ul class=\"nav-list\">\n")
	for _, nav := range navLinks {
		fmt.Fprintf(&b, "          <li><a href=%q>%s</a></li>\n", nav.Href, esc(nav.Label))
	}
	b.WriteString("        </ul>\n")
	b.WriteString("      </nav>\n")
	b.WriteString("    </div>\n")
	b.WriteString("  </header>\n\n")

	b.WriteString("  <main>\n")
	for i, sec := range spec.Sections {
		if i == 0 {
			fmt.Fprintf(&b, "    <section id=%q class=\"section hero-section\">\n", sec.ID)
			b.WriteString("      <div class=\"container hero-grid\">\n")
			b.WriteString("        <div class=\"hero-copy\">\n")
			fmt.Fprintf(&b, "          <span class=\"eyebrow\">%s</span>\n", esc(spec.Category))
			fmt.Fprintf(&b, "          <h1>%s</h1>\n", esc(sec.Heading))
			fmt.Fprintf(&b, "          <p class=\"lead\">%s</p>\n", esc(spec.Tagline))
			fmt.Fprintf(&b, "          <p>%s</p>\n", esc(sec.Purpose))
			writeSectionHighlights(&b, sec, ctaTarget)
			fmt.Fprintf(&b, "          <div class=\"hero-actions\"><a class=\"btn-primary\" href=%q>Explore %s</a></div>\n", ctaTarget, esc(spec.PageTitle))
			b.WriteString("        </div>\n")
			b.WriteString("      </div>\n")
			b.WriteString("    </section>\n\n")
			continue
		}

		fmt.Fprintf(&b, "    <section id=%q class=\"section content-section\">\n", sec.ID)
		b.WriteString("      <div class=\"container\">\n")
		b.WriteString("        <div class=\"section-header\">\n")
		fmt.Fprintf(&b, "          <h2>%s</h2>\n", esc(sec.Heading))
		fmt.Fprintf(&b, "          <p class=\"section-purpose\">%s</p>\n", esc(sec.Purpose))
		b.WriteString("        </div>\n")
		writeSectionHighlights(&b, sec, ctaTarget)
		b.WriteString("      </div>\n")
		b.WriteString("    </section>\n\n")
	}
	b.WriteString("  </main>\n\n")

	b.WriteString("  <footer class=\"site-footer\">\n")
	b.WriteString("    <div class=\"container footer-inner\">\n")
	fmt.Fprintf(&b, "      <p>%s</p>\n", esc(spec.FooterText))
	b.WriteString("    </div>\n")
	b.WriteString("  </footer>\n")
	b.WriteString("  <script src=\"script.js\"></script>\n")
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

func writeSectionHighlights(b *strings.Builder, sec contracts.SectionSpec, ctaTarget string) {
	esc := template.HTMLEscapeString
	if len(sec.RequiredKeywords) > 0 {
		b.WriteString("        <div class=\"cards-grid\">\n")
		for _, kw := range sec.RequiredKeywords {
			fmt.Fprintf(b, "          <article class=\"card\"><h3>%s</h3><p>%s — %s</p></article>\n",
				esc(kw), esc(sec.Heading), esc(kw))
		}
		b.WriteString("        </div>\n")
		b.WriteString("        <ul class=\"feature-list\">\n")
		for _, kw := range sec.RequiredKeywords {
			fmt.Fprintf(b, "          <li>%s</li>\n", esc(kw))
		}
		b.WriteString("        </ul>\n")
	}
	for _, el := range sec.RequiredElements {
		switch strings.ToLower(strings.TrimSpace(el)) {
		case "address":
			fmt.Fprintf(b, "        <address class=\"info-box\">%s · %s</address>\n", esc(sec.Heading), esc(strings.Join(sec.RequiredKeywords, " · ")))
		case "table":
			b.WriteString("        <table class=\"info-table\"><thead><tr><th>Item</th><th>Details</th></tr></thead><tbody>\n")
			for _, kw := range sec.RequiredKeywords {
				fmt.Fprintf(b, "          <tr><td>%s</td><td>%s</td></tr>\n", esc(kw), esc(sec.Purpose))
			}
			b.WriteString("        </tbody></table>\n")
		case "form", "button", "input":
			b.WriteString("        <form class=\"contact-form\" action=\"#top\" method=\"get\">\n")
			b.WriteString("          <label>Name <input type=\"text\" name=\"name\" placeholder=\"Your name\"></label>\n")
			b.WriteString("          <button class=\"btn-primary\" type=\"submit\">Send Inquiry</button>\n")
			b.WriteString("        </form>\n")
		case "blockquote":
			fmt.Fprintf(b, "        <blockquote class=\"quote\">%s</blockquote>\n", esc(sec.Purpose))
		case "a":
			fmt.Fprintf(b, "        <p><a class=\"text-link\" href=%q>Learn more about %s</a></p>\n", ctaTarget, esc(sec.Heading))
		}
	}
}

func buildScaffoldCSS(spec contracts.WebsiteSpecification) string {
	primary := spec.Theme.PrimaryColor
	if primary == "" {
		primary = "#2C1A12"
	}
	bg := spec.Theme.BackgroundColor
	if bg == "" {
		bg = "#FAF6F0"
	}
	accent := spec.Theme.AccentColor
	if accent == "" {
		accent = "#C87D44"
	}

	return fmt.Sprintf(`:root {
  --color-primary: %s;
  --color-bg: %s;
  --color-accent: %s;
  --color-surface: #ffffff;
  --color-text: #1c1917;
  --color-muted: #57534e;
  --max-width: 1120px;
  --radius: 12px;
}

*,
*::before,
*::after {
  box-sizing: border-box;
}

html,
body {
  margin: 0;
  padding: 0;
  max-width: 100%%;
  overflow-x: hidden;
  background-color: var(--color-bg);
  color: var(--color-text);
  font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  line-height: 1.6;
}

h1, h2, h3 {
  color: var(--color-primary);
  font-family: Georgia, "Times New Roman", serif;
  line-height: 1.25;
  margin-top: 0;
}

.container {
  width: 100%%;
  max-width: var(--max-width);
  margin: 0 auto;
  padding: 0 1.25rem;
}

.site-header {
  background: var(--color-surface);
  border-bottom: 1px solid rgba(0, 0, 0, 0.08);
  position: sticky;
  top: 0;
  z-index: 10;
}

.header-inner {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding-top: 1rem;
  padding-bottom: 1rem;
}

.brand {
  font-weight: 700;
  font-size: 1.15rem;
  color: var(--color-primary);
  text-decoration: none;
}

.nav-list {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
  list-style: none;
  margin: 0;
  padding: 0;
}

.nav-list a {
  color: var(--color-primary);
  text-decoration: none;
  font-weight: 500;
}

.nav-list a:hover {
  color: var(--color-accent);
}

.section {
  padding: 3.5rem 0;
}

.hero-section {
  background: linear-gradient(135deg, var(--color-surface) 0%%, var(--color-bg) 100%%);
  border-bottom: 1px solid rgba(0, 0, 0, 0.06);
}

.eyebrow {
  display: inline-block;
  font-size: 0.85rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--color-accent);
  margin-bottom: 0.5rem;
}

.lead {
  font-size: 1.2rem;
  color: var(--color-muted);
}

.btn-primary {
  display: inline-block;
  background: var(--color-primary);
  color: #ffffff;
  padding: 0.75rem 1.35rem;
  border-radius: var(--radius);
  text-decoration: none;
  border: none;
  font-weight: 600;
  cursor: pointer;
}

.cards-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 1.25rem;
  margin: 1.5rem 0;
}

.card {
  background: var(--color-surface);
  padding: 1.25rem;
  border-radius: var(--radius);
  border: 1px solid rgba(0, 0, 0, 0.08);
}

.feature-list {
  padding-left: 1.25rem;
  margin: 1rem 0;
}

.info-table {
  width: 100%%;
  border-collapse: collapse;
  margin: 1rem 0;
}

.info-table th,
.info-table td {
  text-align: left;
  padding: 0.65rem;
  border-bottom: 1px solid rgba(0, 0, 0, 0.1);
}

.contact-form {
  display: grid;
  gap: 0.85rem;
  max-width: 480px;
  margin-top: 1rem;
}

.contact-form input {
  width: 100%%;
  padding: 0.6rem;
  border: 1px solid rgba(0, 0, 0, 0.2);
  border-radius: 8px;
}

.site-footer {
  background: var(--color-primary);
  color: #ffffff;
  padding: 2rem 0;
  margin-top: 2rem;
}

@media (max-width: 768px) {
  .section {
    padding: 2.25rem 0;
  }
  .cards-grid {
    grid-template-columns: 1fr;
  }
}
`, primary, bg, accent)
}

func buildScaffoldJS() string {
	return `document.addEventListener('DOMContentLoaded', () => {
  const forms = document.querySelectorAll('form');
  forms.forEach((form) => {
    form.addEventListener('submit', (event) => {
      event.preventDefault();
    });
  });
});
`
}

func marshalJSONString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}
