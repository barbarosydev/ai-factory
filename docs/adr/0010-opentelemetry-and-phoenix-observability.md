# 0010. End-to-end OpenTelemetry and OpenInference tracing to Arize Phoenix

## Status

Accepted (amends [0009](0009-single-pydanticai-agent-runtime.md))

## Date

2026-11-25

## Context

After [ADR 0009](0009-single-pydanticai-agent-runtime.md) consolidated agent execution into the Python PydanticAI worker (`agents_py/`), debugging a multi-step website build still required manually stitching Go poller logs, Temporal workflow histories, Python worker stdout, and LiteLLM proxy spend rows without a shared trace ID or LLM/tool span waterfall.

## Decision

- **Direct OTLP HTTP/protobuf export to Arize Phoenix**: Both the Go control plane (`service.name = "ai-factory-controlplane"`) and Python PydanticAI worker (`service.name = "ai-factory-agents"`) export spans directly to `http://phoenix.localhost:8080/v1/traces` (`OTEL_EXPORTER_OTLP_ENDPOINT`) under one Phoenix project (`openinference.project.name = "ai-factory"`). No intermediate OpenTelemetry Collector or LiteLLM OTLP callback is deployed.
- **Cross-language Temporal trace propagation**: Configure strict W3C `TraceContext` propagation only (no W3C Baggage) and register Temporal's official OpenTelemetry interceptors (`go.temporal.io/sdk/contrib/opentelemetry` in Go and `temporalio.contrib.opentelemetry.TracingInterceptor` in Python) so `_tracer-data` headers stitch one per-issue root span (`factory.issue` -> `IssueFactoryWorkflow` -> named agent/control activities -> PydanticAI role/model/tool spans -> `playwright.verify_site`).
- **Deterministic workflow boundary**: Keep `internal/workflow/` completely free of telemetry SDK imports and I/O (`go tool workflowcheck ./...` clean); all span creation and header propagation happen in Temporal client/worker interceptors and activity code.
- **Metadata-only privacy default**: Default `FACTORY_OTEL_CAPTURE_CONTENT=false` (`InstrumentationSettings(include_content=False, include_binary_content=False)` with `_SafeOpenInferenceSpanProcessor` wrapping `OpenInferenceSpanProcessor`), omitting raw customer issue bodies, system prompts, completions, and file-write tool arguments unless an operator explicitly sets `FACTORY_OTEL_CAPTURE_CONTENT=true` for local debugging. Note that in `openinference-instrumentation-pydantic-ai` `0.1.40`, structured-output `final_result` tool-call parts omit `"arguments"` when `include_content=False`, which otherwise raises `KeyError: 'arguments'` inside `semantic_conventions.py` and marks healthy LLM spans `ERROR`; `_SafeOpenInferenceSpanProcessor` patches missing redacted tool-call arguments during attribute extraction and strips the placeholder afterward. Scrub `OTEL_*` and `PHOENIX_*` variables in `clean_exec_env()` before spawning Playwright or git subprocesses.
- **Resilience vs diagnostics**: Bounded async `BatchSpanProcessor` queues with short export timeouts log rate-limited warnings on Phoenix outages without failing workflows or activities, while `task doctor` fails fast unless `OTEL_SDK_DISABLED=true`.
- **LiteLLM `/ui/usage/` & PR provenance**: Attach per-activity `x-litellm-tags` (`issue-<N>,role:<role>,attempt-<A>,site:<slug>`) and `x-litellm-trace-id` headers on LiteLLM calls, and record `Temporal Workflow`, `Run ID`, and `OpenTelemetry Trace ID` in generated GitHub Pull Request descriptions.

## Consequences

- Every issue run produces one connected waterfall in Arize Phoenix (`http://phoenix.localhost:8080`), correlated by `trace_id` with LiteLLM `/ui/logs/` and `/ui/usage/` and linked from the resulting GitHub PR summary.
- Temporal workflow replay determinism and zero-secret boundaries remain intact, verified by offline unit and canary tests in Go and Python.
