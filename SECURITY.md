# Security Policy

## Scope and Posture

`ai-factory` is the local-first Go control plane and Temporal worker orchestrating multi-agent builds against [`ai-factory-websites`](https://github.com/barbarosydev/ai-factory-websites) and [`ai-factory-infra`](https://github.com/barbarosydev/ai-factory-infra).

- **Zero Secrets on Disk, in Git, or in Temporal History**: LiteLLM virtual keys are read from Kubernetes Secrets (`ai-factory-infra/.kube/config`) and the GitHub App private key PEM is read from macOS Keychain (`ai-factory-github-app-pem`) solely at process `exec` time.
- **Public Repository Actor Allowlist (`FACTORY_ALLOWED_GITHUB_USERS`)**: Only explicitly allowlisted GitHub users (default: `barbarosydev`) can trigger `factory:ready` issue workflows or resume `factory:waiting` workflows via `SignalHumanReply`.
- **Subprocess Environment Scrubbing (`cleanExecEnv`)**: Secret environment variables (`LITELLM_*`, `GITHUB_APP_*`, `*_KEY`, `*_TOKEN`, `*_SECRET`) are stripped before invoking child verification processes (`npx playwright test`).
- **OS Kernel File Jailing (`os.OpenRoot`)**: All Developer Agent file writes stay confined to `sites/<slug>/` inside an isolated git worktree (`.worktrees/issue-<N>/`) with zero shell tool exposed to LLMs.

## Automated Security Gates

Every commit and Pull Request is gated by `task ci` (`scripts/ci.sh`):

1. **Conftest (OPA Rego) Policies** (`policy/`): Enforces read-only GitHub Actions tokens, 40-char commit-SHA-pinned actions, and Claude Code deny rules/hooks (`keychain-kube-guard.py`, `secret-guard.sh`, `branch-guard.sh`, `agent-guard.sh`).
2. **Temporal Determinism & Sandbox Escape Tests** (`task test`): Runs `go tool workflowcheck ./...`, `TestIssueFactoryWorkflowReplay`, `TestSiteSandboxRejectsEscapesAndBadExtensions`, and `tests/hooks/run.sh`.
3. **Secret Scanning** (`gitleaks`): Scans staged changes in `pre-commit` and full git history in CI.

## Reporting a Vulnerability

Please report security issues privately via [GitHub Security Advisories](https://github.com/barbarosydev/ai-factory/security/advisories/new) rather than opening a public issue.
