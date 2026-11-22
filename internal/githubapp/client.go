package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/barbarosydev/ai-factory/internal/activities"
	"github.com/barbarosydev/ai-factory/internal/contracts"
)

const (
	defaultCheckPollInterval = 10 * time.Second
	defaultCheckMaxWait      = 12 * time.Minute
	noCheckRunsGrace         = 2 * time.Minute
)

// WaitingIssueUpdate describes an open factory:waiting issue ready to receive a HumanReplySignal.
type WaitingIssueUpdate struct {
	Issue              contracts.CustomerIssueInput
	LatestHumanComment contracts.IssueComment
	ReQueuedByReady    bool
}

// PolledWork holds the current open issues ready to start or waiting for human resume.
type PolledWork struct {
	Ready          []contracts.CustomerIssueInput
	WaitingUpdates []WaitingIssueUpdate
}

// Client implements activities.IssueController and GitHub issue/PR polling via go-github/v93.
type Client struct {
	gh                *github.Client
	auth              *InstallationAuth
	owner             string
	repo              string
	webBaseURL        string
	existingSites     func() ([]string, error)
	allowedUsers      []string
	checkPollInterval time.Duration
	checkMaxWait      time.Duration
}

// SetAllowedUsers configures the allowlisted GitHub usernames permitted to enqueue issues
// and send human clarification replies on a public repository.
func (c *Client) SetAllowedUsers(users []string) {
	var clean []string
	seen := make(map[string]bool, len(users))
	for _, u := range users {
		norm := strings.ToLower(strings.TrimSpace(u))
		if norm != "" && !seen[norm] {
			seen[norm] = true
			clean = append(clean, norm)
		}
	}
	c.allowedUsers = clean
}

func (c *Client) isAllowedUser(login string) bool {
	if len(c.allowedUsers) == 0 {
		return true
	}
	norm := strings.ToLower(strings.TrimSpace(login))
	for _, u := range c.allowedUsers {
		if u == norm {
			return true
		}
	}
	return false
}

// NewClient creates a GitHub App client scoped to owner/repo.
func NewClient(
	gh *github.Client,
	auth *InstallationAuth,
	owner string,
	repo string,
	existingSitesFn func() ([]string, error),
) *Client {
	return &Client{
		gh:                gh,
		auth:              auth,
		owner:             owner,
		repo:              repo,
		webBaseURL:        "https://github.com",
		existingSites:     existingSitesFn,
		checkPollInterval: defaultCheckPollInterval,
		checkMaxWait:      defaultCheckMaxWait,
	}
}

// CheckInstallationAccess verifies the App JWT can mint an installation token and see owner/repo.
func (c *Client) CheckInstallationAccess(ctx context.Context) error {
	if c.auth == nil || c.gh == nil {
		return errors.New("github app client not configured")
	}
	if _, err := c.auth.Token(ctx); err != nil {
		return err
	}
	target := c.owner + "/" + c.repo
	opts := &github.ListOptions{PerPage: 100}
	for {
		repos, resp, err := c.gh.Apps.ListRepos(ctx, opts)
		if err != nil {
			return fmt.Errorf("list installation repositories: %w", err)
		}
		for _, r := range repos.Repositories {
			if strings.EqualFold(r.GetFullName(), target) {
				return nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return fmt.Errorf("github app installation does not have access to %s", target)
}

// PollOpenFactoryIssues lists open factory:ready (oldest-first FIFO) and factory:waiting issues with new replies.
func (c *Client) PollOpenFactoryIssues(ctx context.Context) (PolledWork, error) {
	existing, err := c.listExistingSites()
	if err != nil {
		return PolledWork{}, err
	}

	readyIssues, err := c.listIssuesByLabel(ctx, "factory:ready")
	if err != nil {
		return PolledWork{}, err
	}
	waitingIssues, err := c.listIssuesByLabel(ctx, "factory:waiting")
	if err != nil {
		return PolledWork{}, err
	}

	var work PolledWork
	waitingSeen := make(map[int]struct{})

	for _, iss := range waitingIssues {
		if iss.IsPullRequest() {
			continue
		}
		if !c.isAllowedUser(iss.GetUser().GetLogin()) {
			continue
		}
		waitingSeen[iss.GetNumber()] = struct{}{}
		comments, err := c.listIssueComments(ctx, iss.GetNumber())
		if err != nil {
			return PolledWork{}, err
		}
		human, latestAfterPrompt := ExtractClarifications(comments, c.allowedUsers...)
		parsed := ParseIssueForm(iss.GetNumber(), iss.GetTitle(), iss.GetBody(), existing, human)
		reQueued := hasLabel(iss, "factory:ready")
		if latestAfterPrompt != nil {
			work.WaitingUpdates = append(work.WaitingUpdates, WaitingIssueUpdate{
				Issue:              parsed,
				LatestHumanComment: *latestAfterPrompt,
				ReQueuedByReady:    reQueued,
			})
		} else if reQueued {
			work.WaitingUpdates = append(work.WaitingUpdates, WaitingIssueUpdate{
				Issue: parsed,
				LatestHumanComment: contracts.IssueComment{
					ID:        iss.GetUpdatedAt().UnixNano(),
					Author:    iss.GetUser().GetLogin(),
					Body:      "(re-queued via factory:ready label)",
					CreatedAt: iss.GetUpdatedAt().UTC().Format(time.RFC3339),
				},
				ReQueuedByReady: true,
			})
		}
	}

	for _, iss := range readyIssues {
		if iss.IsPullRequest() {
			continue
		}
		if !c.isAllowedUser(iss.GetUser().GetLogin()) {
			continue
		}
		if _, alreadyWaiting := waitingSeen[iss.GetNumber()]; alreadyWaiting {
			continue
		}
		comments, err := c.listIssueComments(ctx, iss.GetNumber())
		if err != nil {
			return PolledWork{}, err
		}
		human, _ := ExtractClarifications(comments, c.allowedUsers...)
		work.Ready = append(work.Ready, ParseIssueForm(iss.GetNumber(), iss.GetTitle(), iss.GetBody(), existing, human))
	}

	sort.Slice(work.Ready, func(i, j int) bool {
		return work.Ready[i].IssueNumber < work.Ready[j].IssueNumber
	})
	sort.Slice(work.WaitingUpdates, func(i, j int) bool {
		return work.WaitingUpdates[i].Issue.IssueNumber < work.WaitingUpdates[j].Issue.IssueNumber
	})
	return work, nil
}

// GetIssue fetches and parses a single open GitHub issue by number.
func (c *Client) GetIssue(ctx context.Context, issueNumber int) (contracts.CustomerIssueInput, []string, error) {
	iss, _, err := c.gh.Issues.Get(ctx, c.owner, c.repo, issueNumber)
	if err != nil {
		return contracts.CustomerIssueInput{}, nil, fmt.Errorf("fetch issue #%d: %w", issueNumber, err)
	}
	if iss.IsPullRequest() {
		return contracts.CustomerIssueInput{}, nil, fmt.Errorf("#%d is a pull request, not an issue", issueNumber)
	}
	if !strings.EqualFold(iss.GetState(), "open") {
		return contracts.CustomerIssueInput{}, nil, fmt.Errorf("issue #%d is %s (must be open)", issueNumber, iss.GetState())
	}
	if !c.isAllowedUser(iss.GetUser().GetLogin()) {
		return contracts.CustomerIssueInput{}, nil, fmt.Errorf("issue #%d author %q is not in FACTORY_ALLOWED_GITHUB_USERS", issueNumber, iss.GetUser().GetLogin())
	}

	existing, err := c.listExistingSites()
	if err != nil {
		return contracts.CustomerIssueInput{}, nil, err
	}
	comments, err := c.listIssueComments(ctx, issueNumber)
	if err != nil {
		return contracts.CustomerIssueInput{}, nil, err
	}
	human, _ := ExtractClarifications(comments, c.allowedUsers...)
	var labels []string
	for _, lbl := range iss.Labels {
		labels = append(labels, lbl.GetName())
	}
	sort.Strings(labels)
	return ParseIssueForm(iss.GetNumber(), iss.GetTitle(), iss.GetBody(), existing, human), labels, nil
}

// ListFactoryIssuesByLabel returns open issue numbers grouped by factory:* label for factory status.
func (c *Client) ListFactoryIssuesByLabel(ctx context.Context) (map[string][]int, error) {
	grouped := make(map[string][]int)
	opts := &github.IssueListByRepoOptions{
		State:       "open",
		Sort:        "created",
		Direction:   "asc",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		issues, resp, err := c.gh.Issues.ListByRepo(ctx, c.owner, c.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list open issues: %w", err)
		}
		for _, iss := range issues {
			if iss.IsPullRequest() {
				continue
			}
			for _, lbl := range iss.Labels {
				name := lbl.GetName()
				if strings.HasPrefix(name, "factory:") {
					grouped[name] = append(grouped[name], iss.GetNumber())
				}
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return grouped, nil
}

// TransitionIssueState updates factory:* labels, posts structured comments, and optionally closes the issue.
func (c *Client) TransitionIssueState(ctx context.Context, req activities.IssueTransition) error {
	if req.RemoveLabel != "" {
		_, err := c.gh.Issues.RemoveLabelForIssue(ctx, c.owner, c.repo, req.IssueNumber, req.RemoveLabel)
		if err != nil && !isHTTPStatus(err, http.StatusNotFound) {
			return fmt.Errorf("remove label %q on #%d: %w", req.RemoveLabel, req.IssueNumber, err)
		}
	}
	if req.AddLabel != "" {
		if _, _, err := c.gh.Issues.AddLabelsToIssue(ctx, c.owner, c.repo, req.IssueNumber, []string{req.AddLabel}); err != nil {
			return fmt.Errorf("add label %q on #%d: %w", req.AddLabel, req.IssueNumber, err)
		}
	}

	comment := formatTransitionComment(req)
	if comment != "" {
		if _, _, err := c.gh.Issues.CreateComment(ctx, c.owner, c.repo, req.IssueNumber, github.IssueCommentRequest{
			Body: comment,
		}); err != nil {
			return fmt.Errorf("comment on #%d: %w", req.IssueNumber, err)
		}
	}

	if req.CloseIssue {
		if _, _, err := c.gh.Issues.Update(ctx, c.owner, c.repo, req.IssueNumber, github.UpdateIssueRequest{
			State: ptr("closed"),
		}); err != nil {
			return fmt.Errorf("close issue #%d: %w", req.IssueNumber, err)
		}
	}
	return nil
}

// OpenPullRequest creates or updates a Pull Request for a verified website build.
func (c *Client) OpenPullRequest(ctx context.Context, req activities.PRRequest) (activities.PRResult, error) {
	verb := "create"
	if req.Spec.Operation == "modify" {
		verb = "update"
	}
	title := fmt.Sprintf("feat(%s): %s website for #%d", req.Spec.SiteSlug, verb, req.IssueNumber)
	body := c.formatPRBody(req)

	openPRs, _, err := c.gh.PullRequests.List(ctx, c.owner, c.repo, &github.PullRequestListOptions{
		State: "open",
		Head:  c.owner + ":" + req.Branch,
		Base:  "main",
	})
	if err != nil {
		return activities.PRResult{}, fmt.Errorf("list existing PRs for %s: %w", req.Branch, err)
	}

	if len(openPRs) > 0 {
		existing := openPRs[0]
		updated, _, editErr := c.gh.PullRequests.Edit(ctx, c.owner, c.repo, existing.GetNumber(), &github.PullRequest{
			Title: ptr(title),
			Body:  ptr(body),
		})
		if editErr != nil {
			return activities.PRResult{}, fmt.Errorf("update existing PR #%d: %w", existing.GetNumber(), editErr)
		}
		sha := req.CommitSHA
		if sha == "" {
			sha = updated.GetHead().GetSHA()
		}
		return activities.PRResult{
			Number: updated.GetNumber(),
			URL:    updated.GetHTMLURL(),
			Head:   req.Branch,
			SHA:    sha,
		}, nil
	}

	created, _, err := c.gh.PullRequests.Create(ctx, c.owner, c.repo, github.CreatePullRequest{
		Title: ptr(title),
		Head:  req.Branch,
		Base:  "main",
		Body:  ptr(body),
	})
	if err != nil {
		return activities.PRResult{}, fmt.Errorf("create PR for %s: %w", req.Branch, err)
	}
	sha := req.CommitSHA
	if sha == "" {
		sha = created.GetHead().GetSHA()
	}
	return activities.PRResult{
		Number: created.GetNumber(),
		URL:    created.GetHTMLURL(),
		Head:   req.Branch,
		SHA:    sha,
	}, nil
}

// AwaitChecksAndMerge waits for GitHub Check Runs (unless skipped) and squash-merges the PR in auto mode.
func (c *Client) AwaitChecksAndMerge(ctx context.Context, req activities.MergeRequest, heartbeat func(string)) (activities.MergeResult, error) {
	if req.SkipRemoteCI {
		if heartbeat != nil {
			heartbeat("skipping remote CI checks and squash-merging PR")
		}
		return c.mergePullRequest(ctx, req, "Merged autonomously with --skip-remote-ci after local Playwright and Reviewer approval.")
	}

	start := time.Now()
	deadline := start.Add(c.checkMaxWait)
	for {
		if heartbeat != nil {
			heartbeat(fmt.Sprintf("checking PR #%d checks for %s", req.PRNumber, req.HeadSHA))
		}
		results, _, err := c.gh.Checks.ListCheckRunsForRef(ctx, c.owner, c.repo, req.HeadSHA, &github.ListCheckRunsOptions{
			Filter:      ptr("latest"),
			ListOptions: github.ListOptions{PerPage: 50},
		})
		if err != nil {
			return activities.MergeResult{}, fmt.Errorf("list check runs for %s: %w", req.HeadSHA, err)
		}

		runs := results.CheckRuns
		if len(runs) > 0 {
			allCompleted := true
			var summaries []string
			for _, r := range runs {
				status := r.GetStatus()
				if status != "completed" {
					allCompleted = false
					break
				}
				conclusion := r.GetConclusion()
				if conclusion != "success" && conclusion != "neutral" && conclusion != "skipped" {
					return activities.MergeResult{}, fmt.Errorf("check run %q ended with %q", r.GetName(), conclusion)
				}
				summaries = append(summaries, fmt.Sprintf("%s=%s", r.GetName(), conclusion))
			}
			if allCompleted {
				detail := "Checks passed (" + strings.Join(summaries, ", ") + ") and PR was squash-merged."
				return c.mergePullRequest(ctx, req, detail)
			}
		} else if time.Since(start) > noCheckRunsGrace {
			return activities.MergeResult{}, fmt.Errorf("no GitHub check runs registered for %s within %s", req.HeadSHA, noCheckRunsGrace)
		}

		if time.Now().After(deadline) {
			return activities.MergeResult{}, fmt.Errorf("timed out waiting %s for PR #%d check runs", c.checkMaxWait, req.PRNumber)
		}

		timer := time.NewTimer(c.checkPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return activities.MergeResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) mergePullRequest(ctx context.Context, req activities.MergeRequest, commentDetail string) (activities.MergeResult, error) {
	mergeTitle := fmt.Sprintf("feat(%s): merge factory build for #%d (#%d)", req.SiteSlug, req.IssueNumber, req.PRNumber)
	res, _, err := c.gh.PullRequests.Merge(ctx, c.owner, c.repo, req.PRNumber, "", &github.PullRequestOptions{
		CommitTitle: mergeTitle,
		SHA:         req.HeadSHA,
		MergeMethod: "squash",
	})
	if err != nil {
		return activities.MergeResult{}, fmt.Errorf("squash-merge PR #%d: %w", req.PRNumber, err)
	}
	if !res.GetMerged() {
		return activities.MergeResult{}, fmt.Errorf("PR #%d was not merged: %s", req.PRNumber, res.GetMessage())
	}

	_ = c.TransitionIssueState(ctx, activities.IssueTransition{
		IssueNumber: req.IssueNumber,
		RemoveLabel: "factory:running",
		AddLabel:    "factory:done",
		CommentBody: fmt.Sprintf("Merged PR #%d (`%s`). %s", req.PRNumber, res.GetSHA(), commentDetail),
	})

	// Best-effort cleanup of merged remote branch so factory/issue-* branches do not accumulate.
	_, _ = c.gh.Git.DeleteRef(ctx, c.owner, c.repo, fmt.Sprintf("heads/factory/issue-%d", req.IssueNumber))

	return activities.MergeResult{
		Merged: true,
		SHA:    res.GetSHA(),
		Detail: commentDetail,
	}, nil
}

func (c *Client) listIssuesByLabel(ctx context.Context, label string) ([]*github.Issue, error) {
	var all []*github.Issue
	opts := &github.IssueListByRepoOptions{
		State:       "open",
		Labels:      []string{label},
		Sort:        "created",
		Direction:   "asc",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		issues, resp, err := c.gh.Issues.ListByRepo(ctx, c.owner, c.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list issues with label %q: %w", label, err)
		}
		all = append(all, issues...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return all, nil
}

func (c *Client) listIssueComments(ctx context.Context, issueNumber int) ([]contracts.IssueComment, error) {
	var out []contracts.IssueComment
	opts := &github.IssueListCommentsOptions{
		Sort:        ptr("created"),
		Direction:   ptr("asc"),
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		comments, resp, err := c.gh.Issues.ListComments(ctx, c.owner, c.repo, issueNumber, opts)
		if err != nil {
			return nil, fmt.Errorf("list comments on #%d: %w", issueNumber, err)
		}
		for _, cm := range comments {
			out = append(out, contracts.IssueComment{
				ID:        cm.GetID(),
				Author:    cm.GetUser().GetLogin(),
				Body:      cm.GetBody(),
				CreatedAt: cm.GetCreatedAt().UTC().Format(time.RFC3339),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (c *Client) listExistingSites() ([]string, error) {
	if c.existingSites == nil {
		return nil, nil
	}
	return c.existingSites()
}

func (c *Client) formatPRBody(req activities.PRRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Closes #%d\n\n", req.IssueNumber)
	b.WriteString("## Factory Build Summary\n\n")
	fmt.Fprintf(&b, "- **Site**: `sites/%s/`\n", req.Spec.SiteSlug)
	fmt.Fprintf(&b, "- **Operation**: `%s` (`%s`)\n", req.Spec.Operation, req.Spec.Category)
	fmt.Fprintf(&b, "- **Page Title**: %s\n", req.Spec.PageTitle)
	fmt.Fprintf(&b, "- **Theme**: primary `%s`, background `%s`, accent `%s`, font `%s`\n",
		req.Spec.Theme.PrimaryColor, req.Spec.Theme.BackgroundColor, req.Spec.Theme.AccentColor, req.Spec.Theme.FontPairing)
	fmt.Fprintf(&b, "- **Repair Attempts**: %d\n", req.Attempts)
	fmt.Fprintf(&b, "- **Autonomy Mode**: `%s`\n\n", req.AutonomyMode)

	b.WriteString("## Security & Reviewer Audit (`sec-agent`)\n\n")
	fmt.Fprintf(&b, "- **Approved**: `%t` | **Path Isolation**: `%t` | **Zero External Assets**: `%t` | **Spec Fidelity**: `%t`\n\n",
		req.Review.Approved, req.Review.PathIsolationOK, req.Review.ExternalAssetsOK, req.Review.SpecFidelityOK)
	if strings.TrimSpace(req.Review.SummaryMarkdown) != "" {
		b.WriteString(strings.TrimSpace(req.Review.SummaryMarkdown) + "\n\n")
	}

	b.WriteString("## Independent Verification (`test-agent` + Playwright)\n\n")
	fmt.Fprintf(&b, "- **Passed**: `%t` | **Semantic Quality OK**: `%t` | **Duration**: %d ms\n",
		req.TestReport.Passed, req.TestReport.SemanticQualityOK, req.TestReport.DurationMs)
	if strings.TrimSpace(req.TestReport.TesterSummary) != "" {
		fmt.Fprintf(&b, "- **Summary**: %s\n", strings.TrimSpace(req.TestReport.TesterSummary))
	}
	if len(req.TestReport.Checks) > 0 {
		b.WriteString("\n| Gate | Result |\n| --- | --- |\n")
		for _, chk := range req.TestReport.Checks {
			status := "PASS"
			if !chk.Passed {
				status = "FAIL"
			}
			fmt.Fprintf(&b, "| %s | %s |\n", chk.Name, status)
		}
	}

	if req.CommitSHA != "" && (req.TestReport.DesktopScreenshot != "" || req.TestReport.MobileScreenshot != "") {
		b.WriteString("\n## Rendered Screenshots\n\n")
		base := strings.TrimRight(c.webBaseURL, "/")
		if req.TestReport.DesktopScreenshot != "" {
			rel := filepath.ToSlash(strings.TrimLeft(req.TestReport.DesktopScreenshot, "/"))
			fmt.Fprintf(&b, "### Desktop (`1280x800`)\n![Desktop preview](%s/%s/%s/blob/%s/%s?raw=true)\n\n",
				base, c.owner, c.repo, req.CommitSHA, rel)
		}
		if req.TestReport.MobileScreenshot != "" {
			rel := filepath.ToSlash(strings.TrimLeft(req.TestReport.MobileScreenshot, "/"))
			fmt.Fprintf(&b, "### Mobile (`375x812`)\n![Mobile preview](%s/%s/%s/blob/%s/%s?raw=true)\n",
				base, c.owner, c.repo, req.CommitSHA, rel)
		}
	}

	return strings.TrimSpace(b.String()) + "\n"
}

func formatTransitionComment(req activities.IssueTransition) string {
	if len(req.Questions) > 0 {
		var b strings.Builder
		b.WriteString(ClarificationMarker + "\n")
		b.WriteString("### Clarification Needed\n\n")
		if strings.TrimSpace(req.CommentBody) != "" {
			b.WriteString(strings.TrimSpace(req.CommentBody) + "\n\n")
		} else {
			b.WriteString("Before building your website, the PM/UX Agent needs clarification on:\n\n")
		}
		for i, q := range req.Questions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(q))
		}
		b.WriteString("\nReply on this issue with your answers (or update the issue description and re-add `factory:ready`) to resume the workflow.")
		return b.String()
	}
	if strings.TrimSpace(req.CommentBody) == "" {
		return ""
	}
	return FactoryRunMarker + "\n" + strings.TrimSpace(req.CommentBody)
}

func hasLabel(iss *github.Issue, target string) bool {
	for _, lbl := range iss.Labels {
		if strings.EqualFold(lbl.GetName(), target) {
			return true
		}
	}
	return false
}

func isHTTPStatus(err error, code int) bool {
	var ghErr *github.ErrorResponse
	return errors.As(err, &ghErr) && ghErr.Response != nil && ghErr.Response.StatusCode == code
}

func ptr[T any](v T) *T {
	return &v
}
