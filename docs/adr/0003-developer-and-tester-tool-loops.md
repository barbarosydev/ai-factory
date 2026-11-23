# 0003. Developer and Tester tool loops

## Status

Accepted (amended by [ADR 0007](0007-gemma4-structured-json-and-high-velocity-skills.md) and [ADR 0009](0009-single-pydanticai-agent-runtime.md))

## Date

2026-11-21

## Context

Live benchmarks on `gemma4:12b` and `gemma4:e4b` showed that small `max_tokens` caps (such as `1024`) cause `gemma4`'s thinking tokens to truncate JSON or tool payloads, whereas generous budgets (`4096–8192` tokens) produce complete output. Giving an LLM a host shell tool creates unnecessary security risk.

## Decision

- Expose a path-jailed PydanticAI tool interface to the Developer Agent (`write_site_bundle`, `list_files`, `read_file`, `write_file`, `check_site_structure`), anchored to `sites/<slug>/` via `SiteSandbox` with zero shell execution.
- Expose a read-only verification interface to the Tester Agent (`run_playwright_suite`, `inspect_dom_snapshot`, `read_site_file`, `submit_test_report`).
- Configure `32768` context tokens and `4096–8192` `max_tokens` so models complete reasoning and tool calls without truncation.

## Consequences

- Path traversal is rejected and arbitrary command execution is unavailable to local models.
- `check_site_structure` catches missing section IDs, required keywords, and external URLs inside the Developer loop before Playwright launches.
