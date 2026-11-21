package dev

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/barbarosydev/ai-factory/internal/contracts"
)

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

a:focus-visible,
button:focus-visible,
input:focus-visible {
  outline: 3px solid var(--color-accent);
  outline-offset: 2px;
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
