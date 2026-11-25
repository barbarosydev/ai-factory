"""OpenTelemetry + OpenInference tracing setup for the PydanticAI Temporal worker."""

from __future__ import annotations

import json
import logging
import os
import urllib.parse
from collections.abc import Callable
from typing import Any

os.environ.setdefault("PYDANTIC_AI_NO_BANNER", "1")

from openinference.instrumentation.pydantic_ai import OpenInferenceSpanProcessor
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.propagate import set_global_textmap
from opentelemetry.propagators.composite import CompositePropagator
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import ReadableSpan, TracerProvider
from opentelemetry.sdk.trace.export import (
    BatchSpanProcessor,
    SimpleSpanProcessor,
    SpanExporter,
)
from opentelemetry.sdk.trace.sampling import ALWAYS_OFF, ALWAYS_ON
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator
from pydantic_ai import Agent
from pydantic_ai.models.instrumented import InstrumentationSettings

LOGGER = logging.getLogger("agents_py.telemetry")

_SYNTHETIC_REDACTED_ARGS = '{"__redacted__":true}'


class _SafeOpenInferenceSpanProcessor(OpenInferenceSpanProcessor):
    """Guard OpenInferenceSpanProcessor when PydanticAI redacts tool arguments.

    In `openinference-instrumentation-pydantic-ai` 0.1.40, structured-output spans
    (`name == "final_result"`) with `include_content=False` omit `"arguments"` from
    `gen_ai.output.messages`, causing `semantic_conventions.py` to raise
    `KeyError: 'arguments'` and mark healthy LLM spans as `ERROR`. Filling a synthetic
    placeholder before `super().on_end(span)` and stripping the synthetic `output.value`
    keeps `LLM` span classification and `OK` status intact without leaking content.
    """

    def on_end(self, span: ReadableSpan) -> None:
        patched_final_result = False
        orig_out: Any = None
        orig_in: Any = None
        if span.attributes:
            orig_out = span.attributes.get("gen_ai.output.messages")
            orig_in = span.attributes.get("gen_ai.input.messages")
            patched_attrs = dict(span.attributes)
            if isinstance(orig_out, str):
                patched_out, changed_out = _patch_redacted_tool_calls(orig_out)
                if changed_out:
                    patched_attrs["gen_ai.output.messages"] = patched_out
                    patched_final_result = True
            if isinstance(orig_in, str):
                patched_in, changed_in = _patch_redacted_tool_calls(orig_in)
                if changed_in:
                    patched_attrs["gen_ai.input.messages"] = patched_in
                    patched_final_result = True
            if patched_final_result:
                span._attributes = patched_attrs

        super().on_end(span)

        if patched_final_result and span.attributes:
            cur = dict(span.attributes)
            if isinstance(orig_out, str):
                cur["gen_ai.output.messages"] = orig_out
            if isinstance(orig_in, str):
                cur["gen_ai.input.messages"] = orig_in
            had_synthetic_out = "__redacted__" in str(cur.get("output.value", ""))
            cleaned: dict[str, Any] = {
                k: v
                for k, v in cur.items()
                if "__redacted__" not in str(v)
                and not (k == "output.mime_type" and had_synthetic_out)
            }
            span._attributes = cleaned


def _patch_redacted_tool_calls(raw_json: str) -> tuple[str, bool]:
    try:
        messages = json.loads(raw_json)
    except json.JSONDecodeError:
        return raw_json, False
    if not isinstance(messages, list):
        return raw_json, False

    changed = False
    for msg in messages:
        if not isinstance(msg, dict):
            continue
        parts = msg.get("parts")
        if not isinstance(parts, list):
            continue
        for part in parts:
            if (
                isinstance(part, dict)
                and part.get("type") == "tool_call"
                and "arguments" not in part
            ):
                part["arguments"] = _SYNTHETIC_REDACTED_ARGS
                changed = True
    if not changed:
        return raw_json, False
    return json.dumps(messages), True


def is_truthy_env(name: str, default: bool = False) -> bool:
    """Return True when environment variable is set to '1' or 'true' (case-insensitive)."""
    raw = os.environ.get(name, "").strip().lower()
    if not raw:
        return default
    return raw in ("1", "true", "yes")


def resolve_traces_endpoint(raw: str) -> str:
    """Normalize an OTLP HTTP base URL or /v1/traces URL without doubling /v1/traces."""
    trimmed = raw.strip()
    if not trimmed:
        raise ValueError("OTLP HTTP endpoint must not be empty")
    parsed = urllib.parse.urlparse(trimmed)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise ValueError(f"invalid OTLP HTTP endpoint {raw!r}: expected http(s)://host[:port]")
    clean_path = parsed.path.rstrip("/")
    if not clean_path.endswith("/v1/traces"):
        clean_path = f"{clean_path}/v1/traces"
    return urllib.parse.urlunparse((parsed.scheme, parsed.netloc, clean_path, "", "", ""))


def setup_tracing(
    *,
    enabled: bool | None = None,
    base_endpoint: str | None = None,
    service_name: str = "ai-factory-agents",
    project_name: str | None = None,
    capture_content: bool | None = None,
    exporter: SpanExporter | None = None,
    sync_export: bool = False,
    set_global: bool = True,
) -> tuple[TracerProvider, Callable[[], None]]:
    """Configure W3C TraceContext propagation, OpenInference mapping, and PydanticAI spans.

    Prompt, completion, and tool-argument bodies are redacted by default
    (`FACTORY_OTEL_CAPTURE_CONTENT=false`) so untrusted customer text and workspace payloads
    are not exported unless explicitly opted in for local debugging.
    """
    if set_global:
        # Strict W3C traceparent/tracestate only; never propagate W3C Baggage.
        set_global_textmap(CompositePropagator([TraceContextTextMapPropagator()]))

    resolved_enabled = not is_truthy_env("OTEL_SDK_DISABLED", False) if enabled is None else enabled
    if not resolved_enabled:
        noop_provider = TracerProvider(sampler=ALWAYS_OFF)
        Agent.instrument_all(False)
        return noop_provider, lambda: None

    resolved_endpoint = (
        base_endpoint
        if base_endpoint is not None
        else os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "http://phoenix.localhost:8080")
    )
    resolved_project = (
        project_name
        if project_name is not None
        else os.environ.get("FACTORY_OTEL_PROJECT_NAME", "ai-factory")
    ).strip() or "ai-factory"
    resolved_capture = (
        is_truthy_env("FACTORY_OTEL_CAPTURE_CONTENT", False)
        if capture_content is None
        else capture_content
    )

    resource = Resource.create(
        {
            "service.name": service_name.strip() or "ai-factory-agents",
            "service.version": "0.3.0",
            "deployment.environment": "local",
            "openinference.project.name": resolved_project,
        }
    )
    provider = TracerProvider(resource=resource, sampler=ALWAYS_ON)

    # OpenInferenceSpanProcessor mutates spans in on_end, so it must be registered BEFORE export.
    provider.add_span_processor(_SafeOpenInferenceSpanProcessor())

    span_exporter = exporter or OTLPSpanExporter(
        endpoint=resolve_traces_endpoint(resolved_endpoint),
        timeout=3,
    )
    if sync_export:
        provider.add_span_processor(SimpleSpanProcessor(span_exporter))
    else:
        provider.add_span_processor(
            BatchSpanProcessor(
                span_exporter,
                max_queue_size=512,
                max_export_batch_size=128,
                export_timeout_millis=3000,
            )
        )

    if set_global:
        trace.set_tracer_provider(provider)

    Agent.instrument_all(
        InstrumentationSettings(
            tracer_provider=provider,
            include_content=resolved_capture,
            include_binary_content=False,
        )
    )
    LOGGER.info(
        "initialized OpenTelemetry + OpenInference tracing (project=%s capture_content=%s)",
        resolved_project,
        resolved_capture,
    )
    return provider, provider.shutdown


def current_trace_id() -> str:
    """Return the active 32-char lowercase hex trace ID when valid and sampled, else empty."""
    ctx = trace.get_current_span().get_span_context()
    if not ctx.is_valid or not ctx.trace_flags.sampled:
        return ""
    return f"{ctx.trace_id:032x}"
