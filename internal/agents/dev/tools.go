package dev

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// CheckSiteStructure performs fast deterministic HTML5, section ID, and local asset validation.
func (s *SiteSandbox) CheckSiteStructure(spec contracts.WebsiteSpecification) StructureCheckResult {
	var errs []string

	rawHTML, err := s.ReadFile("index.html")
	if err != nil {
		return StructureCheckResult{
			Passed: false,
			Errors: []string{"index.html is missing; call write_file to create index.html"},
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

func marshalJSONString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}
