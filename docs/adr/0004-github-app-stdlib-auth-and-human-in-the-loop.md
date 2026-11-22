# 0004. GitHub App stdlib auth and human-in-the-loop

## Status

Accepted

## Date

2026-11-21

## Context

Relying on personal `gh` tokens at runtime conflates human and bot actions and exposes overly broad account permissions. Exposing local webhook ports via public tunnels adds unnecessary operational surface.

## Decision

- Authenticate the factory via a dedicated GitHub App installed on `ai-factory-websites`, keeping the PEM private key in macOS Keychain (`ai-factory-github-app-pem`) and signing RS256 JWTs + caching 1-hour installation tokens with pure Go standard library (`crypto/rsa`, `crypto/x509`, `net/http`) paired with `google/go-github/v93` (no third-party `ghinstallation` wrapper).
- Inject installation tokens into `git push` via an ephemeral in-memory `GIT_ASKPASS` helper.
- Poll open `factory:ready` and `factory:waiting` issues every 15s and bridge human replies to paused `IssueFactoryWorkflow` instances via Temporal `human-reply` signals.
- Enforce a public-repository GitHub actor allowlist (`FACTORY_ALLOWED_GITHUB_USERS`, default `barbarosydev`) on both issue intake (`PollOpenFactoryIssues`, `GetIssue`) and comment signal extraction (`ExtractClarifications`), and deduplicate processed comment IDs inside `IssueFactoryWorkflow` state across worker restarts.

## Consequences

- Zero secrets touch files, git config, Temporal history, or LLM prompts.
- Human clarifications resume paused workflows durably without public inbound tunnels.
