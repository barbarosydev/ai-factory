package gitworktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barbarosydev/ai-factory/internal/contracts"
)

func setupBareAndClone(t *testing.T) (bareDir, cloneDir string) {
	t.Helper()
	tmp := t.TempDir()
	bareDir = filepath.Join(tmp, "remote.git")
	cloneDir = filepath.Join(tmp, "ai-factory-websites")

	runGit(t, tmp, "init", "--bare", "-b", "main", bareDir)
	runGit(t, tmp, "clone", bareDir, cloneDir)
	runGit(t, cloneDir, "config", "user.name", "Test")
	runGit(t, cloneDir, "config", "user.email", "test@example.invalid")

	if err := os.MkdirAll(filepath.Join(cloneDir, "sites", "_template"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, "sites", "_template", "index.html"), []byte("<h1>Template</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, cloneDir, "add", "-A")
	runGit(t, cloneDir, "commit", "-m", "init")
	runGit(t, cloneDir, "push", "-u", "origin", "main")
	return bareDir, cloneDir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func TestCleanGitEnvKeepsOnlyRequiredEnvironment(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("LITELLM_DEV_KEY", "model-secret")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_BASE64", "github-secret")
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "all")
	t.Setenv("GIT_CONFIG_KEY_1", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_1", "leak-secret")

	env := cleanGitEnv("AI_FACTORY_GIT_TOKEN=short-lived")
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"model-secret", "github-secret", "leak-secret", "credential.helper"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("clean Git environment contains forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{"PATH=/usr/bin", "HOME=/tmp/home", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.bareRepository", "GIT_CONFIG_VALUE_0=all", "AI_FACTORY_GIT_TOKEN=short-lived"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("clean Git environment is missing %q", required)
		}
	}
}

func TestWorktreeLifecycleCommitPushAndFailedArchive(t *testing.T) {
	t.Parallel()

	bareDir, cloneDir := setupBareAndClone(t)
	mgr := NewManager(cloneDir, 12345, nil)

	ctx := context.Background()
	wtDir, err := mgr.PrepareWorktree(ctx, 42, "kaffeekultur-berlin")
	if err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}

	siteFile := filepath.Join(SiteDir(wtDir, "kaffeekultur-berlin"), "index.html")
	if err := os.WriteFile(siteFile, []byte("<h1>Kaffeekultur</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	audit, err := mgr.InspectGitDiff(ctx, wtDir, "kaffeekultur-berlin")
	if err != nil {
		t.Fatalf("InspectGitDiff: %v", err)
	}
	if !audit.PathIsolationOK {
		t.Fatalf("expected PathIsolationOK=true, got %+v", audit)
	}

	// Verify path isolation catches dirty files outside sites/<slug>/
	rogueFile := filepath.Join(wtDir, "README.md")
	if err := os.WriteFile(rogueFile, []byte("hijacked"), 0o644); err != nil {
		t.Fatal(err)
	}
	badAudit, err := mgr.InspectGitDiff(ctx, wtDir, "kaffeekultur-berlin")
	if err != nil {
		t.Fatal(err)
	}
	if badAudit.PathIsolationOK {
		t.Fatal("expected PathIsolationOK=false when README.md is dirty")
	}
	_ = os.Remove(rogueFile)

	spec := contracts.WebsiteSpecification{
		SchemaVersion: "v1",
		SiteSlug:      "kaffeekultur-berlin",
		Operation:     "create",
		Category:      "coffee-shop",
		PageTitle:     "Kaffeekultur",
	}
	sha, err := mgr.CommitAndPush(ctx, wtDir, spec, 42)
	if err != nil {
		t.Fatalf("CommitAndPush: %v", err)
	}
	if len(sha) < 7 {
		t.Fatalf("unexpected commit sha %q", sha)
	}

	// Confirm branch exists in bare remote
	refs := runGit(t, bareDir, "branch", "--list", "factory/issue-42")
	if !strings.Contains(refs, "factory/issue-42") {
		t.Fatalf("expected branch factory/issue-42 pushed to remote, got %q", refs)
	}

	// Test failed archive snapshot + clean worktree removal
	reports := []contracts.TestReport{{Attempt: 1, Passed: false, TesterSummary: "failed"}}
	if err := mgr.CleanupWorktree(ctx, wtDir, 42, "kaffeekultur-berlin", true, reports); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatalf("expected active worktree %s to be removed", wtDir)
	}

	entries, err := os.ReadDir(filepath.Join(cloneDir, ".worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	foundArchive := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "issue-42-failed-") {
			foundArchive = true
		}
	}
	if !foundArchive {
		t.Fatal("expected issue-42-failed-* archive directory")
	}

	removed, err := mgr.CleanFailedSnapshots(ctx)
	if err != nil || removed != 1 {
		t.Fatalf("CleanFailedSnapshots = (%d, %v), want (1, nil)", removed, err)
	}
}
