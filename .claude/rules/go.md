---
paths:
  - "internal/**"
  - "cmd/**"
---

# Go & Temporal conventions

- Workflow code under `internal/workflow/` must be 100% deterministic (`go tool workflowcheck ./...` must pass): never call `time.Now()`, `rand`, telemetry SDK I/O, or iterate unsafely over Go maps when generating workflow commands; use `workflow.Now(ctx)`, `workflow.SideEffect`, and `workflow.ExecuteActivity` (OpenTelemetry spans belong in `temporalotel.NewTracingInterceptor` and activities).
- Never place API keys, virtual keys, or GitHub tokens inside Temporal workflow or activity input/output structs or OpenTelemetry attributes/baggage.
- Go code is limited to the control plane; agent roles and site workspace tools live in `agents_py/`.
