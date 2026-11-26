package gitworktree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/barbarosydev/ai-factory/internal/contracts"
)

const maxFailedSnapshots = 5

// DiffAudit summarizes changed paths and diff stats inside the worktree.
type DiffAudit struct {
	ChangedFiles    []string `json:"changed_files"`
	PathIsolationOK bool     `json:"path_isolation_ok"`
	DiffStat        string   `json:"diff_stat"`
}

// TokenProvider supplies a short-lived GitHub App installation token for git push/fetch.
type TokenProvider func(ctx context.Context) (string, error)

// TargetWorkspace abstracts isolated git worktree lifecycle operations for factory activities.
type TargetWorkspace interface {
	PrepareWorktree(ctx context.Context, issueNumber int, siteSlug string) (string, error)
	CommitAndPush(ctx context.Context, worktreeDir string, spec contracts.WebsiteSpecification, issueNumber int) (string, error)
	CleanupWorktree(ctx context.Context, worktreeDir string, issueNumber int, siteSlug string, failed bool, reports []contracts.TestReport) error
	ListExistingSites() ([]string, error)
}

var _ TargetWorkspace = (*Manager)(nil)

// Manager manages isolated Git worktrees, site spec persistence, diff audits, and failed snapshots.
type Manager struct {
	repoPath     string
	appID        int64
	botName      string
	tokenFn      TokenProvider
	disableFetch bool
}

// NewManager constructs a Git worktree manager for the target websites repository.
func NewManager(repoPath string, appID int64, tokenFn TokenProvider) *Manager {
	return &Manager{
		repoPath: repoPath,
		appID:    appID,
		botName:  "ai-factory[bot]",
		tokenFn:  tokenFn,
	}
}

// WorktreeDir returns the isolated worktree path for an issue.
func (m *Manager) WorktreeDir(issueNumber int) string {
	return filepath.Join(m.repoPath, ".worktrees", fmt.Sprintf("issue-%d", issueNumber))
}

// BranchName returns the deterministic branch name for an issue.
func BranchName(issueNumber int) string {
	return fmt.Sprintf("factory/issue-%d", issueNumber)
}

// SiteDir returns the target site directory inside a worktree.
func SiteDir(worktreeDir, slug string) string {
	return filepath.Join(worktreeDir, "sites", slug)
}

// ListExistingSites lists all non-template site slugs under sites/ in the main checkout.
func (m *Manager) ListExistingSites() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(m.repoPath, "sites"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sites dir: %w", err)
	}
	var slugs []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), "_") && !strings.HasPrefix(entry.Name(), ".") {
			slugs = append(slugs, entry.Name())
		}
	}
	sort.Strings(slugs)
	return slugs, nil
}

// ReadExistingSpec reads sites/<slug>/spec.json from the main checkout for modify requests.
func (m *Manager) ReadExistingSpec(slug string) (string, error) {
	if err := contracts.ValidateSiteSlug(slug); err != nil {
		return "", err
	}
	specPath := filepath.Join(m.repoPath, "sites", slug, "spec.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return "", fmt.Errorf("read existing spec %s: %w", specPath, err)
	}
	return string(data), nil
}

// PrepareWorktree fetches origin/main (when available) and creates a clean isolated worktree on factory/issue-<N>.
func (m *Manager) PrepareWorktree(ctx context.Context, issueNumber int, slug string) (string, error) {
	if err := contracts.ValidateSiteSlug(slug); err != nil {
		return "", err
	}

	baseRef := "main"
	if !m.disableFetch {
		if err := m.gitWithAuth(ctx, m.repoPath, "fetch", "origin", "main"); err == nil {
			baseRef = "origin/main"
		}
	}
	if baseRef == "main" {
		if _, err := m.gitOutput(ctx, m.repoPath, "rev-parse", "--verify", "origin/main"); err == nil {
			baseRef = "origin/main"
		}
	}

	wtDir := m.WorktreeDir(issueNumber)
	branch := BranchName(issueNumber)

	_ = m.gitRun(ctx, m.repoPath, "worktree", "prune")
	if _, err := os.Stat(wtDir); err == nil {
		_ = m.gitRun(ctx, m.repoPath, "worktree", "remove", "--force", wtDir)
		_ = os.RemoveAll(wtDir)
	}

	if err := os.MkdirAll(filepath.Dir(wtDir), 0o755); err != nil {
		return "", fmt.Errorf("create .worktrees parent: %w", err)
	}

	if err := m.gitRun(ctx, m.repoPath, "worktree", "add", "-B", branch, wtDir, baseRef); err != nil {
		return "", fmt.Errorf("git worktree add %s: %w", wtDir, err)
	}

	if err := os.MkdirAll(SiteDir(wtDir, slug), 0o755); err != nil {
		return "", fmt.Errorf("create site dir: %w", err)
	}
	return wtDir, nil
}

// InspectGitDiff verifies that only files under sites/<slug>/ have changed in worktreeDir.
func (m *Manager) InspectGitDiff(ctx context.Context, worktreeDir, slug string) (DiffAudit, error) {
	if err := contracts.ValidateSiteSlug(slug); err != nil {
		return DiffAudit{}, err
	}
	statusOut, err := m.gitOutput(ctx, worktreeDir, "status", "--porcelain", "-uall")
	if err != nil {
		return DiffAudit{}, fmt.Errorf("git status: %w", err)
	}

	expectedPrefix := fmt.Sprintf("sites/%s/", slug)
	var changed []string
	pathOK := true

	for _, line := range strings.Split(strings.TrimRight(statusOut, "\r\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		filePath := strings.TrimSpace(line[3:])
		if idx := strings.Index(filePath, " -> "); idx >= 0 {
			filePath = filePath[idx+4:]
		}
		filePath = strings.Trim(filePath, `"`)
		changed = append(changed, filePath)
		if !strings.HasPrefix(filePath, expectedPrefix) {
			pathOK = false
		}
	}
	sort.Strings(changed)

	diffStat, _ := m.gitOutput(ctx, worktreeDir, "diff", "--stat")
	return DiffAudit{
		ChangedFiles:    changed,
		PathIsolationOK: pathOK && len(changed) > 0,
		DiffStat:        strings.TrimSpace(diffStat),
	}, nil
}

// WriteSpec writes WebsiteSpecification to sites/<slug>/spec.json inside the worktree.
func WriteSpec(worktreeDir string, spec contracts.WebsiteSpecification) error {
	if err := contracts.ValidateSiteSlug(spec.SiteSlug); err != nil {
		return err
	}
	target := filepath.Join(SiteDir(worktreeDir, spec.SiteSlug), "spec.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(target, append(b, '\n'), 0o644)
}

// CommitAndPush writes spec.json, verifies path isolation, commits under the bot identity, and pushes the branch.
func (m *Manager) CommitAndPush(ctx context.Context, worktreeDir string, spec contracts.WebsiteSpecification, issueNumber int) (string, error) {
	if err := WriteSpec(worktreeDir, spec); err != nil {
		return "", fmt.Errorf("write spec.json: %w", err)
	}

	audit, err := m.InspectGitDiff(ctx, worktreeDir, spec.SiteSlug)
	if err != nil {
		return "", err
	}
	if !audit.PathIsolationOK {
		return "", fmt.Errorf("refusing to commit: path isolation check failed (%v)", audit.ChangedFiles)
	}

	siteRel := filepath.Join("sites", spec.SiteSlug)
	if err := m.gitRun(ctx, worktreeDir, "add", siteRel); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	botEmail := fmt.Sprintf("%d+%s@users.noreply.github.com", m.appID, m.botName)
	if m.appID <= 0 {
		botEmail = "ai-factory[bot]@users.noreply.github.com"
	}
	commitMsg := fmt.Sprintf("feat(%s): implement website for issue #%d", spec.SiteSlug, issueNumber)
	if spec.Operation == "modify" {
		commitMsg = fmt.Sprintf("feat(%s): update website for issue #%d", spec.SiteSlug, issueNumber)
	}

	if err := m.gitRun(ctx, worktreeDir,
		"-c", "user.name="+m.botName,
		"-c", "user.email="+botEmail,
		"commit", "--no-verify", "-m", commitMsg,
	); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}

	shaOut, err := m.gitOutput(ctx, worktreeDir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %w", err)
	}

	branch := BranchName(issueNumber)
	if err := m.gitWithAuth(ctx, worktreeDir, "push", "--force-with-lease", "-u", "origin", branch); err != nil {
		return "", fmt.Errorf("git push %s: %w", branch, err)
	}

	return strings.TrimSpace(shaOut), nil
}

// CleanupWorktree snapshots failed artifacts (when failed=true) and unregisters the active git worktree.
func (m *Manager) CleanupWorktree(ctx context.Context, worktreeDir string, issueNumber int, slug string, failed bool, reports []contracts.TestReport) error {
	if failed && worktreeDir != "" {
		if err := m.archiveFailedSnapshot(worktreeDir, issueNumber, slug, reports); err != nil {
			return fmt.Errorf("archive failed snapshot: %w", err)
		}
	}

	if worktreeDir != "" {
		_ = m.gitRun(ctx, m.repoPath, "worktree", "remove", "--force", worktreeDir)
		_ = os.RemoveAll(worktreeDir)
		_ = m.gitRun(ctx, m.repoPath, "worktree", "prune")
	}

	if failed {
		_ = m.gitRun(ctx, m.repoPath, "branch", "-D", BranchName(issueNumber))
	}
	return nil
}

// CleanFailedSnapshots removes all archived .worktrees/issue-*-failed-* folders and prunes stale worktrees.
func (m *Manager) CleanFailedSnapshots(ctx context.Context) (int, error) {
	wtRoot := filepath.Join(m.repoPath, ".worktrees")
	entries, err := os.ReadDir(wtRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.Contains(entry.Name(), "-failed-") {
			if err := os.RemoveAll(filepath.Join(wtRoot, entry.Name())); err != nil {
				return removed, err
			}
			removed++
		}
	}
	_ = m.gitRun(ctx, m.repoPath, "worktree", "prune")
	return removed, nil
}

func (m *Manager) archiveFailedSnapshot(worktreeDir string, issueNumber int, slug string, reports []contracts.TestReport) error {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	destDir := filepath.Join(m.repoPath, ".worktrees", fmt.Sprintf("issue-%d-failed-%s", issueNumber, stamp))
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	if slug != "" {
		srcSite := SiteDir(worktreeDir, slug)
		if _, err := os.Stat(srcSite); err == nil {
			if err := copyDir(srcSite, filepath.Join(destDir, slug)); err != nil {
				return err
			}
		}
	}

	for _, r := range reports {
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			continue
		}
		reportPath := filepath.Join(destDir, fmt.Sprintf("test-report-attempt-%d.json", r.Attempt))
		_ = os.WriteFile(reportPath, append(b, '\n'), 0o644)
	}

	return m.pruneOldFailedSnapshots()
}

func (m *Manager) pruneOldFailedSnapshots() error {
	wtRoot := filepath.Join(m.repoPath, ".worktrees")
	entries, err := os.ReadDir(wtRoot)
	if err != nil {
		return err
	}
	var failedDirs []string
	for _, entry := range entries {
		if entry.IsDir() && strings.Contains(entry.Name(), "-failed-") {
			failedDirs = append(failedDirs, filepath.Join(wtRoot, entry.Name()))
		}
	}
	if len(failedDirs) <= maxFailedSnapshots {
		return nil
	}
	sort.Slice(failedDirs, func(i, j int) bool {
		ii, _ := os.Stat(failedDirs[i])
		jj, _ := os.Stat(failedDirs[j])
		if ii == nil || jj == nil {
			return failedDirs[i] < failedDirs[j]
		}
		return ii.ModTime().Before(jj.ModTime())
	})
	for i := 0; i < len(failedDirs)-maxFailedSnapshots; i++ {
		_ = os.RemoveAll(failedDirs[i])
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

func (m *Manager) gitRun(ctx context.Context, dir string, args ...string) error {
	_, err := m.gitOutput(ctx, dir, args...)
	return err
}

func cleanGitEnv(extra ...string) []string {
	source := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			source[key] = value
		}
	}

	var env []string
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SYSTEMROOT"} {
		if value, ok := source[key]; ok {
			env = append(env, key+"="+value)
		}
	}

	if count, err := strconv.Atoi(source["GIT_CONFIG_COUNT"]); err == nil && count > 0 && count <= 100 {
		configCount := 0
		for i := 0; i < count; i++ {
			index := strconv.Itoa(i)
			key := source["GIT_CONFIG_KEY_"+index]
			value := source["GIT_CONFIG_VALUE_"+index]
			if strings.EqualFold(key, "safe.bareRepository") && strings.EqualFold(value, "all") {
				env = append(env,
					"GIT_CONFIG_KEY_"+strconv.Itoa(configCount)+"="+key,
					"GIT_CONFIG_VALUE_"+strconv.Itoa(configCount)+"="+value,
				)
				configCount++
			}
		}
		if configCount > 0 {
			env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(configCount))
		}
	}

	return append(env, extra...)
}

func (m *Manager) gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = cleanGitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %v: %w (%s)", args, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (m *Manager) gitWithAuth(ctx context.Context, dir string, args ...string) error {
	if m.tokenFn == nil {
		return m.gitRun(ctx, dir, args...)
	}
	token, err := m.tokenFn(ctx)
	if err != nil {
		return fmt.Errorf("obtain git installation token: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "ai-factory-askpass-*")
	if err != nil {
		return fmt.Errorf("create askpass dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	askpassPath := filepath.Join(tmpDir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n  *Username*) printf '%s\\n' 'x-access-token' ;;\n  *) printf '%s\\n' \"$AI_FACTORY_GIT_TOKEN\" ;;\nesac\n"
	if err := os.WriteFile(askpassPath, []byte(script), 0o700); err != nil {
		return fmt.Errorf("write askpass helper: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = cleanGitEnv(
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS="+askpassPath,
		"AI_FACTORY_GIT_TOKEN="+token,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %v: %w (%s)", args, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
