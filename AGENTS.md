# ai-factory

Local-first asynchronous multi-agent AI Software Factory powered by Temporal, LiteLLM, Ollama (`gemma4`), and Go.

## Architecture

- Control plane & Temporal worker written in Go (`1.27`), running natively on the Mac host (`temporal.localhost:7233`, `http://temporal.localhost:8080`, `http://litellm.localhost:8080/v1`).
- Strictly 1 concurrent workflow / activity execution (`MaxConcurrentActivityExecutionSize = 1`) to fit comfortably inside the Mac's 36 GB unified memory.
- Four independent agents with dedicated LiteLLM routes and virtual keys (`pm-agent`, `dev-agent`, `test-agent`, `sec-agent`), connected via typed JSON contracts in `internal/contracts/`.
- Target websites are built in isolated git worktrees under `../ai-factory-websites/.worktrees/issue-<N>/` on branch `factory/issue-<N>`.

## Commands

Use `task` for everything; logic lives in `scripts/`.

| Command | Use |
| --- | --- |
| `task build` | Build `./bin/factory` |
| `task doctor` | Verify local prerequisites, `ai-factory-infra`, Keychain PEM, and `ai-factory-websites` |
| `task run` | Start worker + poller in `review` mode |
| `task run:auto` | Start worker + poller in `auto` mode (checks + squash merge) |
| `task run:once` | Process a single issue and exit |
| `task status` | Show Temporal workflow state and loaded Ollama models |
| `task clean:failed` | Remove archived `.worktrees/issue-*-failed-*` folders |
| `task lint` | `gofmt`, `go vet`, `golangci-lint`, `workflowcheck`, `shellcheck`, policies, `actionlint` |
| `task test` | Go unit, Temporal workflow replay, sandbox escape, and worktree tests |
| `task ci` | `task lint`, `task test`, and `gitleaks` |

## Hard rules

1. Never commit or push directly on `main`. Work happens on feature branches and stops at an open PR.
2. Zero secrets in files, git, `.env`, Temporal payload history, logs, or LLM prompts. LiteLLM keys are read at runtime from `ai-factory-infra` K8s Secrets; the GitHub App PEM lives in macOS Keychain (`ai-factory-github-app-pem`).
3. Every Developer Agent filesystem tool must stay strictly jailed to `sites/<slug>/` inside the active issue worktree. Never give the LLM a shell tool.
4. Keep Temporal workflows deterministic (`workflowcheck` clean): no wall-clock `time.Now()`, random, or direct I/O inside workflow code.
5. Technical retries belong in Temporal `RetryPolicy`; semantic repair belongs in the bounded 3-attempt workflow loop.
6. Scripts follow `.claude/rules/scripts.md`: bash with `set -euo pipefail`, shellcheck clean.

## Review roles

| Role | Use it | Checklist |
| --- | --- | --- |
| Workflow reviewer | After editing `internal/workflow/` or `internal/activities/` | `.claude/agents/workflow-reviewer.md` |
| Security auditor | After editing `internal/agents/dev/`, `internal/githubapp/`, or `scripts/` | `.claude/agents/security-auditor.md` |
| Factory verifier | Before committing or opening a pull request | `.claude/agents/factory-verifier.md` |

## Definition of done

- `task ci` passes cleanly (0 failures, `gitleaks` clean).
- Docs/ADRs are updated when architecture, pins, or commands change.
- Stop at an open pull request; never merge your own PR.
