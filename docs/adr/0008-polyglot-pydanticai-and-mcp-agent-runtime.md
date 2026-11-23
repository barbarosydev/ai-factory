# 0008. Polyglot PydanticAI + MCP agent runtime and retiring prompt-leaky scaffolding

## Status

Accepted, superseded for runtime topology by [ADR 0009](0009-single-pydanticai-agent-runtime.md)

## Date

2026-11-22

## Context

Live inspection of generated websites (`kaffeekultur-berlin`, `konditorei-flora-berlin`) revealed that `scaffold_site_from_spec` ([ADR 0007](0007-gemma4-structured-json-and-high-velocity-skills.md)) bypassed real HTML/CSS authoring by rendering a fixed Go template that copied `WebsiteSpecification.sections[].purpose` instructions verbatim into visible `<p>` tags and duplicated `required_keywords` as stub cards. The Python Temporal worker uses PydanticAI with direct `@agent.tool` functions.

## Decision

- **Go Control Plane + Python PydanticAI Worker**: Go owns `IssueFactoryWorkflow`, GitHub polling, Git worktrees, and PR publishing on `FACTORY_TASK_QUEUE` (`ai-factory-v1`). The Python Temporal Activity Worker (`agents_py/`, managed via `uv` and `uv.lock`) executes the four named agent activities on `FACTORY_AGENT_TASK_QUEUE` (`ai-factory-agents`) using PydanticAI.
- **Retire Prompt-Leaky Scaffolding**: Replace `scaffold_site_from_spec` with direct PydanticAI tools (`write_site_bundle`, `write_file`, and `read_template`), pass existing HTML/CSS context on `operation = "modify"`, compact older file-write tool payloads, and enforce an anti-prompt-leak check in `check_site_structure` and Playwright (`STRICT_COPY=1`).
- **Untrusted Input Framing**: Wrap customer issue payloads and comments inside `<untrusted_github_input>` delimiters before agent analysis.
- **Tool protocol**: The agents use PydanticAI's native `@agent.tool` functions; no MCP server is required by the implementation.

## Consequences

- `ai-factory` combines a Go control plane with typed PydanticAI agents over Temporal without overlapping workflow state engines. The single-runtime and unused-MCP removal are recorded in ADR 0009.
- `dev-agent` must author genuine domain copy and responsive CSS; leaked `section.purpose` instructions fail validation deterministically before PR creation.
