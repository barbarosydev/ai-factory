# `agents_py` — PydanticAI Temporal Activity Worker

Python Temporal Activity Worker for `ai-factory`, executing PM, Developer, Tester, and Reviewer activities on the `ai-factory-agents` task queue. Each role uses direct PydanticAI tools inside `SiteSandbox`; no MCP server is part of the runtime.

## Observability

- `agents_py.telemetry.setup_tracing()` configures strict W3C `TraceContext` propagation, `temporalio.contrib.opentelemetry.TracingInterceptor`, `OpenInferenceSpanProcessor`, and `Agent.instrument_all(...)` exporting OTLP HTTP/protobuf spans directly to Arize Phoenix (`http://phoenix.localhost:8080/v1/traces`).
- Prompt, completion, and tool-argument content is redacted by default (`FACTORY_OTEL_CAPTURE_CONTENT=false`); set `FACTORY_OTEL_CAPTURE_CONTENT=true` only for local prompt debugging.
- Every LiteLLM request attaches `x-litellm-tags` (`issue-<N>,role:<role>,attempt-<A>,site:<slug>`) and `x-litellm-trace-id` for correlation in `http://litellm.localhost:8080/ui/usage/` and `/ui/logs/`.

## Quickstart

```bash
uv sync --locked
uv run mypy src tests
uv run pytest
```
