# 0006. Agent harness

## Status

Accepted

## Date

2026-11-21

## Context

Coding agents contribute to `ai-factory`, which handles Keychain credentials, LiteLLM virtual keys, Temporal workflows, and filesystem sandboxing.

## Decision

- Ship the Day-0 Agent Harness (`AGENTS.md`, `CLAUDE.md`, `.claude/` permissions, `keychain-kube-guard.py`, `branch-guard.sh`, `secret-guard.sh`, `agent-guard.sh`, `gofmt.sh`, `session-context.sh`, skills, read-only reviewer agents, and Conftest policies).
- Test every hook in `tests/hooks/run.sh` inside `task ci`.
- Scrub all secret environment variables (`LITELLM_*`, `GITHUB_APP_*`, `*_KEY`, `*_TOKEN`, `*_SECRET`) via `cleanExecEnv()` before spawning verification subprocesses (`npx playwright test`).
- Enforce public open-source governance files (`LICENSE`, `SECURITY.md`, `.github/CODEOWNERS`), `.golangci.yml`, and ADR index synchronization (`docs/adr/README.md`) in `scripts/lint.sh`.

## Consequences

- Secrets, `main` commits, and unsafe harness edits are blocked deterministically before entering git.
