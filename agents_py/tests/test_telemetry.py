"""Unit tests for OpenTelemetry + OpenInference tracing, privacy redaction, and correlation."""

from __future__ import annotations

import json

import pytest
from agents_py.agents import build_litellm_model
from agents_py.security import clean_exec_env
from agents_py.telemetry import current_trace_id, resolve_traces_endpoint, setup_tracing
from opentelemetry import propagate, trace
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from opentelemetry.trace import StatusCode
from pydantic import BaseModel
from pydantic_ai import Agent
from pydantic_ai.messages import ModelMessage, ModelResponse, TextPart, ToolCallPart
from pydantic_ai.models.function import AgentInfo, FunctionModel


def test_resolve_traces_endpoint_prevents_path_doubling() -> None:
    assert (
        resolve_traces_endpoint("http://phoenix.localhost:8080")
        == "http://phoenix.localhost:8080/v1/traces"
    )
    assert (
        resolve_traces_endpoint("http://phoenix.localhost:8080/")
        == "http://phoenix.localhost:8080/v1/traces"
    )
    assert (
        resolve_traces_endpoint("http://phoenix.localhost:8080/v1/traces")
        == "http://phoenix.localhost:8080/v1/traces"
    )
    assert (
        resolve_traces_endpoint("http://phoenix.localhost:8080/v1/traces/")
        == "http://phoenix.localhost:8080/v1/traces"
    )
    with pytest.raises(ValueError, match="invalid OTLP HTTP endpoint"):
        resolve_traces_endpoint("phoenix.localhost:8080")


def test_w3c_traceparent_cross_language_propagation_and_no_baggage() -> None:
    exporter = InMemorySpanExporter()
    provider, shutdown = setup_tracing(
        enabled=True,
        project_name="ai-factory",
        capture_content=False,
        exporter=exporter,
        sync_export=True,
    )
    try:
        go_trace_id = "4bf92f3577b34da6a3ce929d0e0e4736"
        go_span_id = "00f067aa0ba902b7"
        carrier = {
            "traceparent": f"00-{go_trace_id}-{go_span_id}-01",
            "baggage": "secret_canary=sk-should-never-propagate",
        }
        extracted_ctx = propagate.extract(carrier)

        tracer = provider.get_tracer("test.activity")
        with tracer.start_as_current_span("RunPMAgentActivity", context=extracted_ctx):
            assert current_trace_id() == go_trace_id
            outbound: dict[str, str] = {}
            propagate.inject(outbound)
            assert outbound.get("traceparent", "").startswith(f"00-{go_trace_id}-")
            assert "baggage" not in outbound

        spans = exporter.get_finished_spans()
        assert len(spans) == 1
        assert f"{spans[0].context.trace_id:032x}" == go_trace_id
        assert spans[0].parent is not None
        assert f"{spans[0].parent.span_id:016x}" == go_span_id
        assert spans[0].resource.attributes.get("openinference.project.name") == "ai-factory"
    finally:
        Agent.instrument_all(False)
        shutdown()


async def test_privacy_default_omits_prompt_and_tool_content_until_opted_in() -> None:
    canary_prompt = "CANARY_UNTRUSTED_ISSUE_TEXT_9911"
    canary_secret = "sk-canary-virtual-key-secret-8822"
    canary_reply = "CANARY_COMPLETION_OUTPUT_7733"

    def _echo_model(_: list[ModelMessage], _info: AgentInfo) -> ModelResponse:
        return ModelResponse(parts=[TextPart(content=canary_reply)])

    # 1. Default metadata-only mode (capture_content=False)
    redacted_exporter = InMemorySpanExporter()
    _, shutdown_redacted = setup_tracing(
        enabled=True,
        capture_content=False,
        exporter=redacted_exporter,
        sync_export=True,
    )
    try:
        agent = Agent(model=FunctionModel(_echo_model), system_prompt="System canary prompt")

        @agent.tool_plain
        def inspect_canary(arg_text: str) -> str:
            return f"processed:{arg_text}"

        res = await agent.run(f"{canary_prompt} {canary_secret}")
        assert res.output == canary_reply

        spans = redacted_exporter.get_finished_spans()
        assert len(spans) >= 2
        serialized = json.dumps(
            [
                {
                    "name": s.name,
                    "attributes": dict(s.attributes or {}),
                    "events": [
                        {"name": ev.name, "attributes": dict(ev.attributes or {})}
                        for ev in s.events
                    ],
                }
                for s in spans
            ],
            default=str,
        )
        assert canary_prompt not in serialized
        assert canary_secret not in serialized
        assert canary_reply not in serialized
        assert "openinference.span.kind" in serialized
    finally:
        Agent.instrument_all(False)
        shutdown_redacted()

    # 2. Explicit local debug opt-in (capture_content=True)
    debug_exporter = InMemorySpanExporter()
    _, shutdown_debug = setup_tracing(
        enabled=True,
        capture_content=True,
        exporter=debug_exporter,
        sync_export=True,
    )
    try:
        debug_agent = Agent(model=FunctionModel(_echo_model))
        await debug_agent.run(canary_prompt)
        debug_serialized = json.dumps(
            [dict(s.attributes or {}) for s in debug_exporter.get_finished_spans()],
            default=str,
        )
        assert canary_prompt in debug_serialized
    finally:
        Agent.instrument_all(False)
        shutdown_debug()


async def test_unreachable_otlp_exporter_never_fails_agent_execution() -> None:
    def _ok_model(_: list[ModelMessage], _info: AgentInfo) -> ModelResponse:
        return ModelResponse(parts=[TextPart(content="site-ready")])

    _, shutdown = setup_tracing(
        enabled=True,
        base_endpoint="http://127.0.0.1:1",
        capture_content=False,
        set_global=False,
    )
    try:
        agent = Agent(model=FunctionModel(_ok_model))
        res = await agent.run("build static page")
        assert res.output == "site-ready"
    finally:
        Agent.instrument_all(False)
        shutdown()


def test_clean_exec_env_strips_otel_and_phoenix_variables(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://phoenix.localhost:8080")
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer secret")
    monkeypatch.setenv("PHOENIX_API_KEY", "px-secret")
    monkeypatch.setenv("PHOENIX_COLLECTOR_ENDPOINT", "http://phoenix.localhost:8080")

    cleaned = clean_exec_env({"SITE_SLUG": "demo-cafe"})
    assert cleaned["SITE_SLUG"] == "demo-cafe"
    for forbidden in (
        "OTEL_EXPORTER_OTLP_ENDPOINT",
        "OTEL_EXPORTER_OTLP_HEADERS",
        "PHOENIX_API_KEY",
        "PHOENIX_COLLECTOR_ENDPOINT",
    ):
        assert forbidden not in cleaned


def test_build_litellm_model_attaches_correlation_headers() -> None:
    model = build_litellm_model(
        "http://litellm.localhost:8080/v1",
        "sk-dev-test",
        "dev-agent",
        extra_headers={
            "x-litellm-tags": "issue-42,role:dev-agent,attempt-1",
            "x-litellm-trace-id": "4bf92f3577b34da6a3ce929d0e0e4736",
        },
    )
    assert model.model_name == "dev-agent"
    headers = model.client.default_headers
    assert headers["x-litellm-tags"] == "issue-42,role:dev-agent,attempt-1"
    assert headers["x-litellm-trace-id"] == "4bf92f3577b34da6a3ce929d0e0e4736"

    # Global TracerProvider remains functional after test cleanup
    assert trace.get_tracer("post.test") is not None


async def test_structured_output_final_result_redacted_span_stays_ok() -> None:
    class _StructuredOut(BaseModel):
        status: str

    def _structured_model(_: list[ModelMessage], info: AgentInfo) -> ModelResponse:
        tool_name = info.output_tools[0].name
        return ModelResponse(
            parts=[ToolCallPart(tool_name=tool_name, args='{"status":"SECRET_STATUS_CANARY"}')]
        )

    exporter = InMemorySpanExporter()
    _, shutdown = setup_tracing(
        enabled=True,
        capture_content=False,
        exporter=exporter,
        sync_export=True,
    )
    try:
        agent: Agent[None, _StructuredOut] = Agent(
            model=FunctionModel(_structured_model),
            output_type=_StructuredOut,
        )
        res = await agent.run("untrusted prompt canary")
        assert res.output.status == "SECRET_STATUS_CANARY"

        spans = exporter.get_finished_spans()
        assert len(spans) >= 2
        for sp in spans:
            assert sp.status.status_code != StatusCode.ERROR
        dumped = json.dumps([dict(s.attributes or {}) for s in spans], default=str)
        assert "SECRET_STATUS_CANARY" not in dumped
        assert "__redacted__" not in dumped
    finally:
        Agent.instrument_all(False)
        shutdown()
