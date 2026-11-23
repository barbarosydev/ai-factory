# 0009. Single PydanticAI agent runtime and direct tools

## Status

Accepted

## Date

2026-11-23

## Context

Phase 2 introduced a Python PydanticAI Temporal activity worker while retaining a selectable Go implementation of the same four agent roles. The duplicate Go implementations, runtime-selection setting, embedded prompt bundle, and agent-only evaluator duplicated behavior and security logic. The Python agents already use native PydanticAI `@agent.tool` functions; the separate MCP server in `mcp_tools.py` was not connected to those agents.

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
- This decision does not implement Phase 3 OpenTelemetry/Phoenix tracing or PR trace provenance.
