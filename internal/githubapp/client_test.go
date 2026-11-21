package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/barbarosydev/ai-factory/internal/activities"
	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/contracts"
)

func generateTestKeyBase64(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	return base64.StdEncoding.EncodeToString(pemBytes)
}

func TestParseIssueFormAndClarifications(t *testing.T) {
	newBody := `### Site Slug (optional)

kaffeekultur-berlin

### Business / Site Category

Coffee Shop

### What should the website include?

Hero, menu, opening hours, and Berlin location.

### Must-Have Facts (optional)

_No response_`

	parsed := ParseIssueForm(12, "[New Site]: Coffee shop", newBody, []string{"bakery"}, nil)
	require.Equal(t, "new_website", parsed.RequestType)
	require.Equal(t, "kaffeekultur-berlin", parsed.TargetSiteSlug)
	require.Equal(t, []string{"bakery"}, parsed.ExistingSites)

	modBody := `### Target Site Slug

Kaffeekultur-Berlin

### Requested Changes

Add winter specials.`

	modParsed := ParseIssueForm(14, "[Modify Site]: Winter menu", modBody, []string{"kaffeekultur-berlin"}, nil)
	require.Equal(t, "modify_website", modParsed.RequestType)
	require.Equal(t, "kaffeekultur-berlin", modParsed.TargetSiteSlug)

	comments := []contracts.IssueComment{
		{ID: 1, Author: "barbarosydev", Body: "Early note before bot"},
		{ID: 2, Author: "ai-factory[bot]", Body: ClarificationMarker + "\n1. What are the opening hours?"},
		{ID: 3, Author: "barbarosydev", Body: "Mon-Fri 08:00-18:00"},
		{ID: 4, Author: "ai-factory[bot]", Body: ClarificationMarker + "\n1. Confirm street address?"},
	}
	human, latest := ExtractClarifications(comments)
	require.Len(t, human, 2)
	require.Nil(t, latest, "no human comment after latest clarification marker yet")

	comments = append(comments, contracts.IssueComment{ID: 5, Author: "barbarosydev", Body: "Oranienstrasse 42"})
	_, latest = ExtractClarifications(comments)
	require.NotNil(t, latest)
	require.Equal(t, int64(5), latest.ID)
	require.Equal(t, "Oranienstrasse 42", latest.Body)
}

func TestInstallationAuthCachingAndRefresh(t *testing.T) {
	keyB64 := generateTestKeyBase64(t)
	var tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/99/access_tokens":
			require.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "))
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_test_token_" + string(rune('0'+tokenCalls)),
				"expires_at": time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			require.Equal(t, "token ghs_test_token_1", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count":  1,
				"repositories": []map[string]any{{"full_name": "barbarosydev/ai-factory-websites"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	auth, err := NewInstallationAuth(42, 99, keyB64, srv.URL, srv.Client().Transport)
	require.NoError(t, err)
	gh, err := NewGitHubClient(auth)
	require.NoError(t, err)
	client := NewClient(gh, auth, "barbarosydev", "ai-factory-websites", nil)

	require.NoError(t, client.CheckInstallationAccess(context.Background()))
	require.NoError(t, client.CheckInstallationAccess(context.Background()))
	require.Equal(t, 1, tokenCalls, "cached installation token should be reused across requests")
}

func TestGitHubControllerWorkflowAndPoller(t *testing.T) {
	keyB64 := generateTestKeyBase64(t)
	var mu sync.Mutex
	var prCreatedBody string
	var removedLabels []string
	var addedLabels []string
	var commentBodies []string
	var mergedPR bool
	checkPollCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/99/access_tokens":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_live",
				"expires_at": time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/barbarosydev/ai-factory-websites/issues":
			lbl := r.URL.Query().Get("labels")
			switch lbl {
			case "factory:ready":
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{
						"number": 20,
						"title":  "[New Site]: Second",
						"body":   "### What should the website include?\nShop 2",
						"state":  "open",
						"labels": []map[string]string{{"name": "factory:ready"}},
					},
					{
						"number": 10,
						"title":  "[New Site]: First",
						"body":   "### Site Slug (optional)\ncafe-one\n### What should the website include?\nShop 1",
						"state":  "open",
						"labels": []map[string]string{{"name": "factory:ready"}},
					},
				})
			case "factory:waiting":
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{
						"number": 15,
						"title":  "[New Site]: Waiting",
						"body":   "### What should the website include?\nNeeds hours",
						"state":  "open",
						"labels": []map[string]string{{"name": "factory:waiting"}},
					},
				})
			default:
				_ = json.NewEncoder(w).Encode([]any{})
			}
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
			if strings.Contains(r.URL.Path, "/issues/15/") {
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{
						"id":         101,
						"user":       map[string]string{"login": "ai-factory[bot]"},
						"body":       ClarificationMarker + "\n1. What hours?",
						"created_at": time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
					},
					{
						"id":         102,
						"user":       map[string]string{"login": "barbarosydev"},
						"body":       "Daily 09:00-17:00",
						"created_at": time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339),
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			parts := strings.Split(r.URL.Path, "/")
			removedLabels = append(removedLabels, parts[len(parts)-1])
			w.WriteHeader(http.StatusNotFound) // test idempotent 404 ignore
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			addedLabels = append(addedLabels, labels...)
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": labels[0]}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			commentBodies = append(commentBodies, payload.Body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 200, "body": payload.Body})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/barbarosydev/ai-factory-websites/pulls":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/barbarosydev/ai-factory-websites/pulls":
			var reqBody struct {
				Title string `json:"title"`
				Body  string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			prCreatedBody = reqBody.Body
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number":   77,
				"html_url": "https://github.com/barbarosydev/ai-factory-websites/pull/77",
				"head":     map[string]any{"sha": "abc123def"},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/check-runs"):
			checkPollCount++
			status := "in_progress"
			conclusion := ""
			if checkPollCount >= 2 {
				status = "completed"
				conclusion = "success"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"check_runs": []map[string]any{
					{"name": "Site CI", "status": status, "conclusion": conclusion},
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/barbarosydev/ai-factory-websites/pulls/77/merge":
			mergedPR = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"merged":  true,
				"sha":     "mergedsha999",
				"message": "Pull Request successfully merged",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	auth, err := NewInstallationAuth(42, 99, keyB64, srv.URL, srv.Client().Transport)
	require.NoError(t, err)
	gh, err := NewGitHubClient(auth)
	require.NoError(t, err)
	client := NewClient(gh, auth, "barbarosydev", "ai-factory-websites", func() ([]string, error) {
		return []string{"bakery"}, nil
	})
	client.checkPollInterval = 10 * time.Millisecond

	ctx := context.Background()
	polled, err := client.PollOpenFactoryIssues(ctx)
	require.NoError(t, err)
	require.Len(t, polled.Ready, 2)
	require.Equal(t, 10, polled.Ready[0].IssueNumber, "oldest ready issue should come first (FIFO)")
	require.Len(t, polled.WaitingUpdates, 1)
	require.Equal(t, 15, polled.WaitingUpdates[0].Issue.IssueNumber)
	require.Equal(t, "Daily 09:00-17:00", polled.WaitingUpdates[0].LatestHumanComment.Body)

	require.NoError(t, client.TransitionIssueState(ctx, activities.IssueTransition{
		IssueNumber: 10,
		RemoveLabel: "factory:ready",
		AddLabel:    "factory:waiting",
		Questions:   []string{"What color palette?"},
	}))
	require.Contains(t, removedLabels, "factory:ready")
	require.Contains(t, addedLabels, "factory:waiting")
	require.Contains(t, commentBodies[0], ClarificationMarker)

	pr, err := client.OpenPullRequest(ctx, activities.PRRequest{
		IssueNumber:  10,
		IssueTitle:   "[New Site]: First",
		Branch:       "factory/issue-10",
		CommitSHA:    "abc123def",
		Attempts:     1,
		AutonomyMode: config.AutonomyAuto,
		Spec: contracts.WebsiteSpecification{
			SchemaVersion: "v1",
			SiteSlug:      "cafe-one",
			Operation:     "create",
			Category:      "Coffee Shop",
			PageTitle:     "Cafe One",
			Theme: contracts.ThemeSpec{
				PrimaryColor:    "#112233",
				BackgroundColor: "#FAF7F2",
				AccentColor:     "#C87D44",
				FontPairing:     "Editorial Serif + Clean Sans",
			},
		},
		TestReport: contracts.TestReport{
			Passed:            true,
			SemanticQualityOK: true,
			DurationMs:        1420,
			Checks:            []contracts.SingleCheck{{Name: "zero console errors", Passed: true}},
			DesktopScreenshot: "sites/cafe-one/evidence/desktop.png",
			MobileScreenshot:  "sites/cafe-one/evidence/mobile.png",
		},
		Review: contracts.ReviewResult{
			Approved:         true,
			PathIsolationOK:  true,
			ExternalAssetsOK: true,
			SpecFidelityOK:   true,
			SummaryMarkdown:  "Looks clean.",
		},
	})
	require.NoError(t, err)
	require.Equal(t, 77, pr.Number)
	require.Contains(t, prCreatedBody, "Closes #10")
	require.Contains(t, prCreatedBody, "https://github.com/barbarosydev/ai-factory-websites/blob/abc123def/sites/cafe-one/evidence/desktop.png?raw=true")
	require.Contains(t, prCreatedBody, "https://github.com/barbarosydev/ai-factory-websites/blob/abc123def/sites/cafe-one/evidence/mobile.png?raw=true")

	var heartbeats int
	mergeRes, err := client.AwaitChecksAndMerge(ctx, activities.MergeRequest{
		IssueNumber: 10,
		PRNumber:    77,
		HeadSHA:     "abc123def",
		SiteSlug:    "cafe-one",
	}, func(string) { heartbeats++ })
	require.NoError(t, err)
	require.True(t, mergeRes.Merged)
	require.True(t, mergedPR)
	require.Equal(t, "mergedsha999", mergeRes.SHA)
	require.GreaterOrEqual(t, heartbeats, 2)
}
