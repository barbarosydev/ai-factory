package contracts

import (
	"fmt"
	"regexp"
	"strings"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var hexColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// IssueComment represents a chronological human or bot comment on a GitHub issue.
type IssueComment struct {
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// CustomerIssueInput is the normalized intake artifact sent to the PM/UX Agent.
type CustomerIssueInput struct {
	IssueNumber    int            `json:"issue_number"`
	IssueTitle     string         `json:"issue_title"`
	IssueBody      string         `json:"issue_body"`
	RequestType    string         `json:"request_type"` // "new_website" | "modify_website"
	TargetSiteSlug string         `json:"target_site_slug,omitempty"`
	ExistingSites  []string       `json:"existing_sites"`
	Clarifications []IssueComment `json:"clarifications,omitempty"`
}

// PMDecisionOutput is the structured decision returned by the PM/UX Agent.
type PMDecisionOutput struct {
	Status                 string                `json:"status"` // "ready" | "needs_clarification" | "rejected"
	RejectionReason        string                `json:"rejection_reason,omitempty"`
	ClarificationQuestions []string              `json:"clarification_questions,omitempty"`
	Spec                   *WebsiteSpecification `json:"spec,omitempty"`
}

// WebsiteSpecification is the structured contract shared between PM, Developer, Tester, and Reviewer.
type WebsiteSpecification struct {
	SchemaVersion string        `json:"schema_version"` // "v1"
	SiteSlug      string        `json:"site_slug"`
	Operation     string        `json:"operation"` // "create" | "modify"
	Category      string        `json:"category"`
	PageTitle     string        `json:"page_title"`
	Tagline       string        `json:"tagline"`
	Theme         ThemeSpec     `json:"theme"`
	Navigation    []NavLinkSpec `json:"navigation"`
	Sections      []SectionSpec `json:"sections"`
	FooterText    string        `json:"footer_text"`
}

// ThemeSpec defines the colors and font stack selected by the PM/UX Agent.
type ThemeSpec struct {
	PrimaryColor    string `json:"primary_color"`
	BackgroundColor string `json:"background_color"`
	AccentColor     string `json:"accent_color"`
	FontPairing     string `json:"font_pairing"`
}

// NavLinkSpec describes a top-level navigation item.
type NavLinkSpec struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// SectionSpec defines a required section and the deterministic checks expected inside it.
type SectionSpec struct {
	ID               string   `json:"id"`
	Heading          string   `json:"heading"`
	Purpose          string   `json:"purpose"`
	RequiredKeywords []string `json:"required_keywords"`
	RequiredElements []string `json:"required_elements"`
}

// DeveloperTaskInput is passed to the Developer Agent activity on each attempt.
type DeveloperTaskInput struct {
	IssueNumber   int                  `json:"issue_number"`
	Attempt       int                  `json:"attempt"`
	Spec          WebsiteSpecification `json:"spec"`
	PriorFailures []FailureEvidence    `json:"prior_failures,omitempty"`
}

// ImplementationResult summarizes the files written and tool turns used by the Developer Agent.
type ImplementationResult struct {
	Attempt      int      `json:"attempt"`
	Summary      string   `json:"summary"`
	FilesChanged []string `json:"files_changed"`
	TurnsUsed    int      `json:"turns_used"`
	ToolTrace    []string `json:"tool_trace"`
}

// TestRunInput is passed to the independent Tester Agent activity.
type TestRunInput struct {
	IssueNumber int                  `json:"issue_number"`
	Attempt     int                  `json:"attempt"`
	Spec        WebsiteSpecification `json:"spec"`
}

// SingleCheck records one deterministic Playwright or inspection check result.
type SingleCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// FailureEvidence gives the Developer Agent concrete diagnostics for bounded repair.
type FailureEvidence struct {
	CheckName   string `json:"check_name"`
	Expected    string `json:"expected"`
	Actual      string `json:"actual"`
	FileHint    string `json:"file_hint,omitempty"`
	Remediation string `json:"remediation"`
}

// TestReport is the structured artifact produced by the Tester Agent.
type TestReport struct {
	Attempt           int               `json:"attempt"`
	Passed            bool              `json:"passed"`
	SemanticQualityOK bool              `json:"semantic_quality_ok"`
	DurationMs        int64             `json:"duration_ms"`
	Checks            []SingleCheck     `json:"checks"`
	Failures          []FailureEvidence `json:"failures,omitempty"`
	TesterSummary     string            `json:"tester_summary"`
	DesktopScreenshot string            `json:"desktop_screenshot_path"`
	MobileScreenshot  string            `json:"mobile_screenshot_path"`
}

// ReviewInput is passed to the Security & Code Reviewer Agent.
type ReviewInput struct {
	Issue        CustomerIssueInput   `json:"issue"`
	Spec         WebsiteSpecification `json:"spec"`
	TestReport   TestReport           `json:"test_report"`
	ChangedFiles []string             `json:"changed_files"`
	DiffStat     string               `json:"diff_stat"`
}

// ReviewResult captures the Reviewer Agent's audit verdict and Pull Request summary.
type ReviewResult struct {
	Approved         bool     `json:"approved"`
	PathIsolationOK  bool     `json:"path_isolation_ok"`
	ExternalAssetsOK bool     `json:"external_assets_ok"`
	SpecFidelityOK   bool     `json:"spec_fidelity_ok"`
	SummaryMarkdown  string   `json:"summary_markdown"`
	Findings         []string `json:"findings,omitempty"`
}

// ValidateSiteSlug checks that a site slug is safe, lowercase kebab-case, and free of path traversal.
func ValidateSiteSlug(slug string) error {
	if slug == "" || slug == "_template" {
		return fmt.Errorf("invalid site_slug %q: reserved or empty", slug)
	}
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("invalid site_slug %q: must match ^[a-z0-9]+(?:-[a-z0-9]+)*$", slug)
	}
	return nil
}

// Validate checks structural invariants on PMDecisionOutput before passing it to Developer/Tester.
func (d PMDecisionOutput) Validate() error {
	switch d.Status {
	case "ready":
		if d.Spec == nil {
			return fmt.Errorf("status is ready but spec is nil")
		}
		return d.Spec.Validate()
	case "needs_clarification":
		if len(d.ClarificationQuestions) == 0 {
			return fmt.Errorf("status is needs_clarification but clarification_questions is empty")
		}
		return nil
	case "rejected":
		if strings.TrimSpace(d.RejectionReason) == "" {
			return fmt.Errorf("status is rejected but rejection_reason is empty")
		}
		return nil
	default:
		return fmt.Errorf("unsupported PM status %q", d.Status)
	}
}

// Validate verifies a WebsiteSpecification satisfies all factory v1 invariants.
func (s WebsiteSpecification) Validate() error {
	if err := ValidateSiteSlug(s.SiteSlug); err != nil {
		return err
	}
	if s.Operation != "create" && s.Operation != "modify" {
		return fmt.Errorf("invalid operation %q: must be create or modify", s.Operation)
	}
	if strings.TrimSpace(s.PageTitle) == "" {
		return fmt.Errorf("page_title must not be empty")
	}
	if !hexColorPattern.MatchString(s.Theme.PrimaryColor) ||
		!hexColorPattern.MatchString(s.Theme.BackgroundColor) ||
		!hexColorPattern.MatchString(s.Theme.AccentColor) {
		return fmt.Errorf("theme colors must be 6-digit hex codes")
	}
	if len(s.Sections) < 3 || len(s.Sections) > 8 {
		return fmt.Errorf("sections count %d out of range [3, 8]", len(s.Sections))
	}
	seenIDs := make(map[string]struct{}, len(s.Sections))
	for _, sec := range s.Sections {
		if !slugPattern.MatchString(sec.ID) {
			return fmt.Errorf("invalid section id %q: must be lowercase kebab-case", sec.ID)
		}
		if _, dup := seenIDs[sec.ID]; dup {
			return fmt.Errorf("duplicate section id %q", sec.ID)
		}
		seenIDs[sec.ID] = struct{}{}
		if strings.TrimSpace(sec.Heading) == "" {
			return fmt.Errorf("section %q heading must not be empty", sec.ID)
		}
	}
	return nil
}

// PMDecisionJSONSchema returns the strict JSON Schema passed to LiteLLM response_format.
func PMDecisionJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{
				"type": "string",
				"enum": []string{"ready", "needs_clarification", "rejected"},
			},
			"rejection_reason": map[string]any{"type": "string"},
			"clarification_questions": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"spec": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"schema_version": map[string]any{"type": "string"},
					"site_slug":      map[string]any{"type": "string"},
					"operation":      map[string]any{"type": "string", "enum": []string{"create", "modify"}},
					"category":       map[string]any{"type": "string"},
					"page_title":     map[string]any{"type": "string"},
					"tagline":        map[string]any{"type": "string"},
					"theme": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"primary_color":    map[string]any{"type": "string"},
							"background_color": map[string]any{"type": "string"},
							"accent_color":     map[string]any{"type": "string"},
							"font_pairing":     map[string]any{"type": "string"},
						},
						"required":             []string{"primary_color", "background_color", "accent_color", "font_pairing"},
						"additionalProperties": false,
					},
					"navigation": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"label": map[string]any{"type": "string"},
								"href":  map[string]any{"type": "string"},
							},
							"required":             []string{"label", "href"},
							"additionalProperties": false,
						},
					},
					"sections": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":                map[string]any{"type": "string"},
								"heading":           map[string]any{"type": "string"},
								"purpose":           map[string]any{"type": "string"},
								"required_keywords": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								"required_elements": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
							"required":             []string{"id", "heading", "purpose", "required_keywords", "required_elements"},
							"additionalProperties": false,
						},
					},
					"footer_text": map[string]any{"type": "string"},
				},
				"required": []string{
					"schema_version", "site_slug", "operation", "category",
					"page_title", "tagline", "theme", "navigation", "sections", "footer_text",
				},
				"additionalProperties": false,
			},
		},
		"required":             []string{"status"},
		"additionalProperties": false,
	}
}
