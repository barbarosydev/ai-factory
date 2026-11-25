# ai-factory

Local-first asynchronous multi-agent AI Software Factory powered by Temporal, LiteLLM, Ollama (`gemma4`), and Go.

## Architecture

- Go owns the durable control plane, Temporal `IssueFactoryWorkflow`, GitHub App poller, git worktrees, PR publishing/merging, and diagnostics. The PydanticAI Python Temporal Activity Worker in `agents_py/` runs the PM, Developer, Tester, and Reviewer roles on `FACTORY_AGENT_TASK_QUEUE` (`ai-factory-agents`); there is no Go agent runtime or MCP server.
- Sequential activity execution (`MaxConcurrentActivityExecutionSize = 1`; leaving `MaxConcurrentWorkflowTaskExecutionSize` unset per Go SDK sticky cache requirements) fits comfortably inside the Mac's 36 GB unified memory.
- Four PydanticAI roles use dedicated LiteLLM routes and scoped virtual keys (`pm-agent`, `dev-agent`, `test-agent`, `sec-agent`), connected through compatible typed JSON contracts.
- Developer Agent edits stay jailed to `.worktrees/issue-<N>/sites/<slug>/` via Python `SiteSandbox`, using `write_site_bundle` and granular file/structure tools.
- Long-running agent activities emit periodic 15-second Temporal heartbeats (`heartbeat_loop`) and enforce bounded self-correction (max 3 attempts).
- Go and Python workers propagate W3C `traceparent` across Temporal `_tracer-data` headers and export metadata-only OpenTelemetry + OpenInference spans (`FACTORY_OTEL_CAPTURE_CONTENT=false` by default) directly to Arize Phoenix (`http://phoenix.localhost:8080`).

## Commands

Use `task` for everything; logic lives in `scripts/`.

| Command | Use |
| --- | --- |
| `task build` | Build `./bin/factory` |
| `task doctor` | Verify local prerequisites, `ai-factory-infra`, Keychain PEM, and `ai-factory-websites` |
| `task run` | Start worker + poller in `review` mode |
| `task run:auto` | Start worker + poller in `auto` mode (checks + squash merge) |
| `task run:once` | Process a single issue and exit |
| `task status` | Show Temporal workflow state, loaded Ollama models, and open `factory:*` issues |
| `task clean:failed` | Remove archived `.worktrees/issue-*-failed-*` folders |
| `task lint` | `gofmt`, `go vet`, `golangci-lint`, `workflowcheck`, `shellcheck`, `ruff`, `mypy`, policies, `actionlint`, doc checks |
| `task test` | Go unit, Temporal workflow replay, Python `pytest`, sandbox escape, and worktree tests |
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
| Security auditor | After editing `agents_py/`, `internal/githubapp/`, or `scripts/` | `.claude/agents/security-auditor.md` |
| Factory verifier | Before committing or opening a pull request | `.claude/agents/factory-verifier.md` |

## Definition of done

- `task ci` passes cleanly (0 failures, `gitleaks` clean).
- Docs/ADRs are updated when architecture, pins, or commands change (`check_links.py` & `check_commands.py` enforce sync).
- Stop at an open pull request; never merge your own PR.
