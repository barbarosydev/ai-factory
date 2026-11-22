# 0007. Gemma 4 structured JSON reliability, background heartbeats, and high-velocity skills

## Status

Accepted (amended by [ADR 0008](0008-polyglot-pydanticai-and-mcp-agent-runtime.md))

## Date

2026-11-21

## Context

Live end-to-end testing against Ollama (`gemma4:12b` and `gemma4:e4b`) and Temporal uncovered three runtime bottlenecks:

1. **GBNF Grammar Loop on `gemma4:12b`**: Passing `response_format: { type: "json_schema", strict: true }` at `temperature: 0` triggered a thinking-token repetition loop in llama.cpp (`8,192` completion tokens, `204s` elapsed, empty `assistant.content: ""`), causing Temporal `TIMEOUT_TYPE_HEARTBEAT` retries on `RunPMAgentActivity`.
2. **Temporal SDK Worker Constraint & Single-Turn LLM Latency**: Setting `MaxConcurrentWorkflowTaskExecutionSize: 1` caused a Temporal Go SDK panic (`cannot set MaxConcurrentWorkflowTaskExecutionSize to 1` because sticky workflow caching requires >= 2 slots), while calling `activity.RecordHeartbeat` only between turns risked heartbeat timeouts on >3-minute single LLM inference calls.
3. **Slow Multi-File Tool Loops**: Writing full HTML/CSS/JS across 3–5 separate file turns wasted LLM latency when `WebsiteSpecification` already captured layout, colors, navigation, sections, keywords, and required elements.

## Decision

1. **Example-Guided JSON + Deterministic Normalization (`internal/agents/pm/pm.go`)**: Drop strict GBNF `response_format` on `pm-agent`, use `temperature: 0.2`, embed the canonical JSON contract in `prompts/pm_system.md`, extract JSON from `content` or `reasoning_content` fallback, and normalize slugs/headings/colors deterministically before `PMDecisionOutput.Validate()`.
2. **Background 15s Activity Heartbeats (`internal/activities/activities.go`) & SDK Concurrency (`internal/controlplane/runner.go`)**: Run a 15-second background ticker (`startHeartbeatLoop`) around every long-running agent activity, set generous `StartToCloseTimeout` (`30m–60m`) and `HeartbeatTimeout` (`10m–15m`), and enforce single-task model execution strictly via `MaxConcurrentActivityExecutionSize: 1` (leaving workflow task concurrency at the SDK default).
3. **High-Velocity Sandboxed Skills (`internal/agents/dev/scaffold.go`, `tester/agent.go`, `reviewer/agent.go`)**:
   - Give the Developer Agent `scaffold_site_from_spec` (generating accessible semantic HTML5 + responsive CSS3 + vanilla JS inside `os.OpenRoot`) and `write_site_bundle`, completing most builds in 1 turn (~5s at ~180 tok/s on `gemma4:e4b`).
   - Bundle `dom_snapshot` directly inside `run_playwright_suite` tool output (~8–10s end-to-end verification).
   - Pre-supply git diff and URL scan facts to `sec-agent` (~4s review).
   - Pass `--no-verify` on automated bot commits inside isolated worktrees (`internal/gitworktree/manager.go`) because the worktree has already been validated by Tester + Reviewer and lacks a duplicate `node_modules/` tree.

## Consequences

- Measured end-to-end build time dropped from 11+ minutes (timeout) to ~2–3 minutes including remote GitHub Actions CI polling.
- Out-of-scope rejection completes in ~26s and clarification pauses trigger in ~20s.
