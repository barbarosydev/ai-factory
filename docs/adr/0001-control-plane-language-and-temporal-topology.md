# 0001. Control plane language and Temporal topology

## Status

Accepted

## Date

2026-11-21

## Context

`ai-factory` needs to run durably on a 36 GB Apple Silicon Mac, talk to Temporal (`temporal.localhost:7233`) and LiteLLM (`http://litellm.localhost:8080/v1`), manage isolated Git worktrees, execute local Playwright tests, and read secrets from Kubernetes and macOS Keychain at runtime.

## Decision

- Implement the control plane and Temporal worker in **Go (`1.27`)** using `go.temporal.io/sdk`.
- Run the worker natively on the macOS host rather than inside k3d pods, so it accesses Git worktrees, Playwright/Chromium, and macOS Keychain directly without mounting host filesystems into containers.
- Enforce single-issue sequential execution (`MaxConcurrentActivityExecutionSize = 1`) so Ollama never faces concurrent model requests on the 36 GB Mac.

## Consequences

- Single static binary (`bin/factory`) with compile-time workflow determinism checks (`go tool workflowcheck`).
- Only three direct third-party Go modules: `go.temporal.io/sdk`, `github.com/google/go-github/v93`, and `golang.org/x/net`.
