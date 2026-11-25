# 0009. Single PydanticAI agent runtime and direct tools

## Status

Accepted

## Date

2026-11-23

## Context

Go and Python implementations duplicated the same four agent roles, tooling, prompts, and security logic. PydanticAI provides native `@agent.tool` functions, so the separate MCP server in `mcp_tools.py` was not used by the agents.

## Decision

- Keep Go as the durable control plane: `IssueFactoryWorkflow`, polling and GitHub App access, worktree management, PR publishing/merging, and diagnostics remain in Go.
- Run all four agent roles (PM, Developer, Tester, Reviewer) exclusively in the `agents_py/` PydanticAI Temporal worker on `FACTORY_AGENT_TASK_QUEUE`.
- Remove Go agent implementations, Go-only LLM/evaluator code, `FACTORY_AGENT_RUNTIME`, embedded prompts, and metrics that only observed the deleted in-process agents. Keep Markdown prompts for the Python worker and keep the shared Go/Python Temporal contracts.
- Remove the unused MCP server, its entry point and test, and the `mcp` dependency. Continue using the existing direct PydanticAI tools and shared workspace helpers.
- Keep Temporal activity type names and workflow scheduling behavior unchanged so the runtime removal does not alter existing workflow command histories.
- Enforce site path isolation, subprocess environment scrubbing, and fail-closed Tester/Reviewer gates in the retained Python worker. GitHub actor authorization and clarification-comment filtering remain in the Go poller/client.

## Consequences

- There is one implementation of each agent role and one agent-tooling/security boundary to maintain.
- Temporal remains polyglot only at the Go control-plane/Python activity-worker boundary; Go agent execution is not a supported fallback.
- Agent call/token/Playwright metrics formerly emitted by the Go process are not available on its `/metrics` endpoint. LiteLLM spend logs, Temporal histories, and test reports remain available.
- Cross-language tracing across Temporal, the Python worker, and LiteLLM, including Phoenix trace provenance, was deferred at the time of this ADR and subsequently implemented in [ADR 0010](0010-opentelemetry-and-phoenix-observability.md).
