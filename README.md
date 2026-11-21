# ai-factory

Local-first, asynchronous multi-agent AI Software Factory powered by Temporal, LiteLLM, Ollama (`gemma4`), and Go.

## Architecture

- **Orchestrator**: Temporal Go SDK (`go.temporal.io/sdk`) worker running on the Mac host against `temporal.localhost:7233` (`MaxConcurrentActivityExecutionSize = 1`).
- **Four Independent Agents**:
  - `pm-agent` (`gemma4:12b`): Translates GitHub Issues into structured `WebsiteSpecification` JSON Schemas, rejects out-of-scope requests (`factory:rejected`), or pauses durably for human clarification (`factory:waiting`).
  - `dev-agent` (`gemma4:e4b`): Implements or modifies plain HTML5/CSS/JS websites inside an isolated git worktree (`.worktrees/issue-<N>/sites/<slug>/`) via a bounded 5-tool loop.
  - `test-agent` (`gemma4:e4b`): Independently executes the shared `@playwright/test` suite (`tests/site.spec.mjs`), inspects rendered DOM/screenshots, and produces structured `FailureEvidence` for bounded repair (up to 3 attempts).
  - `sec-agent` (`gemma4:e4b`): Audits git diff path isolation, verifies external network isolation, checks specification fidelity, and composes the portfolio Pull Request report.
- **Target Repo**: [`barbarosydev/ai-factory-websites`](https://github.com/barbarosydev/ai-factory-websites)
- **Platform Infra**: [`barbarosydev/ai-factory-infra`](https://github.com/barbarosydev/ai-factory-infra)

## Prerequisites

See [docs/prerequisites.md](docs/prerequisites.md) and [docs/github-app-setup.md](docs/github-app-setup.md). From a fresh clone:

```bash
brew bundle --no-upgrade
mise install
pre-commit install
```

## Commands

- `task doctor`: Verify Go toolchain, `ai-factory-infra` connectivity, macOS Keychain GitHub App PEM, and `ai-factory-websites` Playwright readiness.
- `task build`: Build `./bin/factory`.
- `task run`: Run the Temporal worker + GitHub Issue Poller in human-review mode (`factory:review`).
- `task run:auto`: Run in autonomous mode (waits for PR CI checks via GitHub Checks API, then squash-merges and labels `factory:done`).
- `task run:once`: Process at most one eligible `factory:ready` issue and exit cleanly.
- `task status`: Inspect active Temporal workflows, loaded Ollama models, and open `factory:*` issues.
- `task ci`: Run all linters (`gofmt`, `go vet`, `golangci-lint`, `workflowcheck`, `shellcheck`, Conftest `policy/`, `actionlint`), unit/workflow/sandbox tests, and `gitleaks`.
