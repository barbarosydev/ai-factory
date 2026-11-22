# Prerequisites

Set up a Mac for `ai-factory` in four steps.

## 1. Platform & target repositories

Ensure the sibling repositories live next to `ai-factory`:

- `../ai-factory-infra` — running (`task up` + `task doctor` green)
- `../ai-factory-websites` — cloned with Playwright Chromium installed (`npm ci && npx playwright install chromium`)

## 2. Homebrew packages

Install `mise` and the GitHub CLI (`gh`) first using the root `Brewfile`:

```bash
brew bundle --no-upgrade
```

## 3. Pinned CLI tools (`mise`)

`mise.toml` pins the exact latest stable versions of Go, `uv`, `golangci-lint`, Temporal CLI, Task, Conftest, ShellCheck, actionlint, jq, yq, pre-commit, and gitleaks:

```bash
mise install
uv sync --directory agents_py --locked
```

## 4. Commit hook and verification

Install the pre-commit hook and run the offline + live checks:

```bash
pre-commit install
task ci
task doctor
```

For GitHub App creation and macOS Keychain setup (required before running live GitHub polling), see [github-app-setup.md](github-app-setup.md).
