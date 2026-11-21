---
paths:
  - "internal/**"
  - "cmd/**"
---

# Go & Temporal conventions

- Workflow code under `internal/workflow/` must be 100% deterministic (`go tool workflowcheck ./...` must pass): never call `time.Now()`, `rand`, or iterate unsafely over Go maps when generating workflow commands; use `workflow.Now(ctx)`, `workflow.SideEffect`, and `workflow.ExecuteActivity`.
- Never place API keys, virtual keys, or GitHub tokens inside Temporal workflow or activity input/output structs (they are persisted in PostgreSQL history).
- Filesystem tools in `internal/agents/dev/` must use `os.OpenRoot` (Go 1.24+) or strict canonical prefix verification inside `sites/<slug>/`. Never expose a shell tool to an agent.
