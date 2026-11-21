# 0003. Developer and Tester tool loops

## Status

Accepted

## Date

2026-11-21

## Context

Live benchmarks on `gemma4:12b` and `gemma4:e4b` showed that small `max_tokens` caps (such as `1024`) cause `gemma4`'s thinking tokens to truncate JSON or tool payloads, whereas generous budgets (`8192–12288` tokens) produce complete, high-quality output. Giving an LLM a host shell tool creates unnecessary security risk.

## Decision

- Expose a minimal, path-jailed 5-tool interface to the Developer Agent (`list_files`, `read_file`, `write_file`, `check_site_structure`, `finish`), anchored to `sites/<slug>/` via `os.OpenRoot`, with zero shell execution.
- Expose a read-only 4-tool verification interface to the Tester Agent (`run_playwright_suite`, `inspect_dom_snapshot`, `read_site_file`, `submit_test_report`).
- Configure `32768` context tokens and `8192–12288` `max_tokens` so models complete reasoning and tool generation without truncation.

## Consequences

- Path traversal and arbitrary command execution by local models are impossible by construction.
- `check_site_structure` catches missing section IDs inside the Developer loop before Playwright even launches.
