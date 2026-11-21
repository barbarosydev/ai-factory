# 0002. Agent roles and structured contracts

## Status

Accepted

## Date

2026-11-21

## Context

Multi-agent systems often suffer from chatty free-form hand-offs, redundant agent frameworks, and ungrounded LLM self-grading.

## Decision

- Use four independent agents mapped 1:1 to `ai-factory-infra` LiteLLM routes and scoped virtual keys:
  1. **PM / UX Agent (`pm-agent`)** — `gemma4:12b`, structured JSON Schema `WebsiteSpecification`.
  2. **Developer Agent (`dev-agent`)** — `gemma4:e4b`, sandboxed 5-tool filesystem loop.
  3. **Tester Agent (`test-agent`)** — `gemma4:e4b`, independent Playwright + DOM inspection tools + root-cause `FailureEvidence` synthesis.
  4. **Reviewer Agent (`sec-agent`)** — `gemma4:e4b`, git diff path isolation + external URL scan + PR composition.
- Hand off versioned JSON structs (`CustomerIssueInput`, `PMDecisionOutput`, `ImplementationResult`, `TestReport`, `ReviewResult`) through Temporal without introducing LangGraph, CrewAI, or AutoGen.

## Consequences

- Developer and Tester are strictly independent; deterministic Playwright checks ground every pass/fail verdict.
- Every prompt, tool call, and cost metric is attributable per agent route in LiteLLM.
