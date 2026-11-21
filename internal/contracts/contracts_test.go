package contracts

import "testing"

func validSpec() WebsiteSpecification {
	return WebsiteSpecification{
		SchemaVersion: "v1",
		SiteSlug:      "kaffeekultur-berlin",
		Operation:     "create",
		Category:      "coffee-shop",
		PageTitle:     "Kaffeekultur Berlin",
		Tagline:       "Specialty coffee in Kreuzberg",
		Theme: ThemeSpec{
			PrimaryColor:    "#4A2C2A",
			BackgroundColor: "#FAF7F2",
			AccentColor:     "#D4A373",
			FontPairing:     "system-sans",
		},
		Navigation: []NavLinkSpec{
			{Label: "About", Href: "#about"},
			{Label: "Menu", Href: "#menu"},
		},
		Sections: []SectionSpec{
			{ID: "hero", Heading: "Kaffeekultur Berlin", Purpose: "Hero", RequiredKeywords: []string{"Kreuzberg"}},
			{ID: "about", Heading: "About", Purpose: "Story", RequiredKeywords: []string{"roastery"}},
			{ID: "hours", Heading: "Hours", Purpose: "Opening times", RequiredKeywords: []string{"Monday"}},
		},
		FooterText: "Kaffeekultur Berlin",
	}
}

func TestValidateSiteSlug(t *testing.T) {
	t.Parallel()
	tests := []struct {
		slug    string
		wantErr bool
	}{
		{"kaffeekultur-berlin", false},
		{"bakery1", false},
		{"", true},
		{"_template", true},
		{"../evil", true},
		{"CoffeeShop", true},
		{"a/b", true},
	}
	for _, tc := range tests {
		err := ValidateSiteSlug(tc.slug)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateSiteSlug(%q) err=%v wantErr=%v", tc.slug, err, tc.wantErr)
		}
	}
}

func TestPMDecisionValidate(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	if err := (PMDecisionOutput{Status: "ready", Spec: &spec}).Validate(); err != nil {
		t.Fatalf("expected valid ready decision, got %v", err)
	}
	if err := (PMDecisionOutput{Status: "ready"}).Validate(); err == nil {
		t.Fatal("expected error for ready status with nil spec")
	}
	if err := (PMDecisionOutput{Status: "needs_clarification", ClarificationQuestions: []string{"Which city?"}}).Validate(); err != nil {
		t.Fatalf("expected valid clarification decision, got %v", err)
	}
	if err := (PMDecisionOutput{Status: "rejected", RejectionReason: "Payments are outside v1 scope"}).Validate(); err != nil {
		t.Fatalf("expected valid rejection decision, got %v", err)
	}
	if err := (PMDecisionOutput{Status: "unknown"}).Validate(); err == nil {
		t.Fatal("expected error for unknown status")
	}
}
