# 0002. Agent roles and structured contracts

## Status

Accepted (amended by [ADR 0007](0007-gemma4-structured-json-and-high-velocity-skills.md), [ADR 0008](0008-polyglot-pydanticai-and-mcp-agent-runtime.md), and [ADR 0009](0009-single-pydanticai-agent-runtime.md))

## Date

2026-11-21

## Context

Multi-agent systems often suffer from chatty free-form hand-offs, redundant agent frameworks, and ungrounded LLM self-grading.

## Decision

- Use four independent agents mapped 1:1 to `ai-factory-infra` LiteLLM routes and scoped virtual keys:
  1. **PM / UX Agent (`pm-agent`)** — `gemma4:12b`, produces typed `PMDecisionOutput` (`ready` + `WebsiteSpecification`, `needs_clarification`, or `rejected`).
  2. **Developer Agent (`dev-agent`)** — `gemma4:e4b`, sandboxed `SiteSandbox` site-building and granular file tools.
  3. **Tester Agent (`test-agent`)** — `gemma4:e4b`, independent Playwright + DOM inspection tools + root-cause `FailureEvidence` synthesis.
  4. **Reviewer Agent (`sec-agent`)** — `gemma4:e4b`, git diff path isolation + external URL scan + PR composition.
- Hand off versioned JSON contracts (`CustomerIssueInput`, `PMDecisionOutput`, `ImplementationResult`, `TestReport`, `ReviewResult`) through Temporal without introducing overlapping agent frameworks.

## Consequences

- Developer and Tester are strictly independent; deterministic Playwright checks ground every pass/fail verdict.
- Agent roles retain dedicated LiteLLM routes; LiteLLM spend logs provide model-call and token accounting.
