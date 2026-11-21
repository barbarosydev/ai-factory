package githubapp

import (
	"strings"

	"github.com/barbarosydev/ai-factory/internal/contracts"
)

const (
	// ClarificationMarker identifies factory-posted clarification questions so human replies after it can resume waiting workflows.
	ClarificationMarker = "<!-- factory:clarification-request:v1 -->"

	// FactoryRunMarker identifies automated status comments posted by the factory.
	FactoryRunMarker = "<!-- factory:run-status:v1 -->"
)

// ParseIssueForm normalizes a GitHub Issue body (Issue Form or free-text fallback)
// into a structured CustomerIssueInput contract.
func ParseIssueForm(
	issueNumber int,
	title string,
	body string,
	existingSites []string,
	clarifications []contracts.IssueComment,
) contracts.CustomerIssueInput {
	sections := parseMarkdownSections(body)

	modifySlug := cleanField(sections, "target site slug")
	modifyChanges := cleanField(sections, "requested changes")
	newSlug := cleanField(sections, "site slug (optional)", "site slug")

	requestType := "new_website"
	targetSlug := strings.ToLower(strings.TrimSpace(newSlug))

	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	if modifySlug != "" || modifyChanges != "" || strings.HasPrefix(lowerTitle, "[modify site]") {
		requestType = "modify_website"
		if modifySlug != "" {
			targetSlug = strings.ToLower(strings.TrimSpace(modifySlug))
		}
	}

	return contracts.CustomerIssueInput{
		IssueNumber:    issueNumber,
		IssueTitle:     strings.TrimSpace(title),
		IssueBody:      strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n")),
		RequestType:    requestType,
		TargetSiteSlug: targetSlug,
		ExistingSites:  existingSites,
		Clarifications: clarifications,
	}
}

// ExtractClarifications separates authentic human replies from factory/bot comments
// and identifies the newest human reply posted after the latest clarification prompt.
func ExtractClarifications(comments []contracts.IssueComment) ([]contracts.IssueComment, *contracts.IssueComment) {
	var human []contracts.IssueComment
	sawPrompt := false
	var latestAfterPrompt *contracts.IssueComment

	for _, c := range comments {
		if isFactoryComment(c) {
			if strings.Contains(c.Body, ClarificationMarker) {
				sawPrompt = true
				latestAfterPrompt = nil
			}
			continue
		}
		if strings.TrimSpace(c.Body) == "" {
			continue
		}
		human = append(human, c)
		if sawPrompt {
			copyComment := c
			latestAfterPrompt = &copyComment
		}
	}
	return human, latestAfterPrompt
}

func isFactoryComment(c contracts.IssueComment) bool {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(c.Author)), "[bot]") {
		return true
	}
	return strings.Contains(c.Body, ClarificationMarker) || strings.Contains(c.Body, FactoryRunMarker)
}

func parseMarkdownSections(body string) map[string]string {
	sections := make(map[string]string)
	normalized := strings.ReplaceAll(body, "\r\n", "\n")

	var currentHeading string
	var currentLines []string
	flush := func() {
		if currentHeading != "" {
			sections[currentHeading] = strings.TrimSpace(strings.Join(currentLines, "\n"))
		}
		currentLines = nil
	}

	for _, line := range strings.Split(normalized, "\n") {
		if strings.HasPrefix(line, "### ") {
			flush()
			currentHeading = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "### ")))
			continue
		}
		if currentHeading != "" {
			currentLines = append(currentLines, line)
		}
	}
	flush()
	return sections
}

func cleanField(sections map[string]string, keys ...string) string {
	for _, k := range keys {
		val, ok := sections[k]
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(val)
		if trimmed == "" || strings.EqualFold(trimmed, "_No response_") {
			return ""
		}
		return trimmed
	}
	return ""
}
