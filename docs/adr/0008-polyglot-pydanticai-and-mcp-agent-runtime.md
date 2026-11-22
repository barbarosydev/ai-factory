# 0008. Polyglot PydanticAI + MCP agent runtime and retiring prompt-leaky scaffolding

## Status

Accepted (amends [ADR 0002](0002-agent-roles-and-structured-contracts.md), [ADR 0003](0003-developer-and-tester-tool-loops.md), and [ADR 0007](0007-gemma4-structured-json-and-high-velocity-skills.md))

## Date

2026-11-22

## Context

Live inspection of generated websites (`kaffeekultur-berlin`, `konditorei-flora-berlin`) revealed that `scaffold_site_from_spec` ([ADR 0007](0007-gemma4-structured-json-and-high-velocity-skills.md)) bypassed real HTML/CSS authoring by rendering a fixed Go template that copied `WebsiteSpecification.sections[].purpose` instructions verbatim into visible `<p>` tags and duplicated `required_keywords` as stub cards. Meanwhile, Temporal natively supports polyglot activity workers over gRPC, enabling typed Python agents (**PydanticAI**) and standardized **Model Context Protocol (MCP)** tool servers alongside the Go control plane.

## Decision

- **Dual-Selectable Agent Runtime (`FACTORY_AGENT_RUNTIME=pydanticai|go`, default `pydanticai`)**:
  - The Go control plane (`internal/controlplane`, `internal/workflow/issue_workflow.go`, `internal/githubapp`, `internal/gitworktree`) continues to own `IssueFactoryWorkflow`, GitHub polling, Git worktrees, and PR publishing on `FACTORY_TASK_QUEUE` (`ai-factory-v1`).
  - A Python Temporal Activity Worker (`agents_py/`, managed via `uv` and `uv.lock`) executes `RunPMAgentActivity`, `RunDeveloperAgentActivity`, `RunTesterAgentActivity`, and `RunReviewerActivity` on `FACTORY_AGENT_TASK_QUEUE` (`ai-factory-agents`) using **PydanticAI** (`pydantic-ai-slim`) and **MCP** (`mcp`). Setting `FACTORY_AGENT_RUNTIME=go` routes those activities back to the Go agent baseline for side-by-side benchmarking.
- **Retire Prompt-Leaky Scaffolding**: Remove `scaffold_site_from_spec` from the LLM tool list in both runtimes (`write_site_bundle`, `write_file`, and `read_template` replace it), pass existing HTML/CSS context on `operation = "modify"`, compact older file-write tool payloads, and enforce an anti-prompt-leak check in both `CheckSiteStructure` and Playwright (`STRICT_COPY=1`).
- **Untrusted Input Framing**: Wrap customer issue payloads and comments inside `<untrusted_github_input>` delimiters in both Go and Python agent prompts.

## Consequences

- `ai-factory` combines a high-assurance Go control plane with typed PydanticAI agents and MCP tool servers over Temporal without overlapping workflow state engines.
- `dev-agent` must author genuine domain copy and responsive CSS; leaked `section.purpose` instructions fail validation deterministically before PR creation.
