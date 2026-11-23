# 0007. Gemma 4 structured JSON reliability, background heartbeats, and high-velocity skills

## Status

Accepted (agent runtime details amended by [ADR 0008](0008-polyglot-pydanticai-and-mcp-agent-runtime.md) and [ADR 0009](0009-single-pydanticai-agent-runtime.md))

## Date

2026-11-21

## Context

Live end-to-end testing against Ollama (`gemma4:12b` and `gemma4:e4b`) and Temporal uncovered three runtime bottlenecks:

1. **GBNF Grammar Loop on `gemma4:12b`**: Passing `response_format: { type: "json_schema", strict: true }` at `temperature: 0` triggered a thinking-token repetition loop in llama.cpp (`8,192` completion tokens, `204s` elapsed, empty `assistant.content: ""`), causing Temporal `TIMEOUT_TYPE_HEARTBEAT` retries on `RunPMAgentActivity`.
2. **Temporal SDK Worker Constraint & Single-Turn LLM Latency**: Setting `MaxConcurrentWorkflowTaskExecutionSize: 1` caused a Temporal Go SDK panic (`cannot set MaxConcurrentWorkflowTaskExecutionSize to 1` because sticky workflow caching requires >= 2 slots), while calling `activity.RecordHeartbeat` only between turns risked heartbeat timeouts on >3-minute single LLM inference calls.
3. **Slow Multi-File Tool Loops**: Writing full HTML/CSS/JS across 3–5 separate file turns wasted LLM latency when `WebsiteSpecification` already captured layout, colors, navigation, sections, keywords, and required elements.

## Decision

1. **Example-Guided JSON + Deterministic Normalization**: Drop strict GBNF `response_format` on `pm-agent`, use `temperature: 0.2`, keep the canonical contract in `prompts/pm_system.md`, and normalize PM output before contract validation.
2. **Activity Heartbeats & Go SDK Concurrency**: Python agent activities emit periodic heartbeats; the Go control worker keeps `MaxConcurrentActivityExecutionSize: 1` (leaving workflow task concurrency at the SDK default).
3. **High-Velocity Sandboxed Skills**:
   - Give the Developer Agent a `write_site_bundle` tool within Python `SiteSandbox`, completing most builds in one turn.
   - Bundle `dom_snapshot` directly inside `run_playwright_suite` tool output (~8–10s end-to-end verification).
   - Pre-supply git diff and URL scan facts to `sec-agent` (~4s review).
   - Pass `--no-verify` on automated bot commits inside isolated worktrees (`internal/gitworktree/manager.go`) because the worktree has already been validated by Tester + Reviewer and lacks a duplicate `node_modules/` tree.

## Consequences

- Measured end-to-end build time dropped from 11+ minutes (timeout) to ~2–3 minutes including remote GitHub Actions CI polling.
- Out-of-scope rejection completes in ~26s and clarification pauses trigger in ~20s.
