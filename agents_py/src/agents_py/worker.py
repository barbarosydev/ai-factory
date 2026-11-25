"""Temporal Python Activity Worker for PydanticAI agents."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import os
import signal
from collections.abc import AsyncIterator, Callable
from typing import Any

from pydantic_ai.models import Model
from temporalio import activity
from temporalio.client import Client, Interceptor
from temporalio.contrib.opentelemetry import TracingInterceptor
from temporalio.worker import Worker

from agents_py.agents import (
    FactoryModelConfig,
    build_litellm_model,
    run_developer_agent,
    run_pm_agent,
    run_reviewer_agent,
    run_tester_agent,
)
from agents_py.contracts import (
    CustomerIssueInput,
    DeveloperActivityInput,
    ImplementationResult,
    PMDecisionOutput,
    ReviewerActivityInput,
    ReviewResult,
    TesterActivityInput,
    TestReport,
)
from agents_py.telemetry import current_trace_id, is_truthy_env, setup_tracing

LOGGER = logging.getLogger("agents_py.worker")


ModelFactory = Callable[[str, str, str], Model]

_REQUIRED_KEY_ENV_VARS = (
    "LITELLM_PM_KEY",
    "LITELLM_DEV_KEY",
    "LITELLM_TEST_KEY",
    "LITELLM_SEC_KEY",
)


def load_model_config_from_env() -> FactoryModelConfig:
    """Load LiteLLM model endpoints and scoped virtual keys from environment variables."""
    missing = [name for name in _REQUIRED_KEY_ENV_VARS if not os.environ.get(name, "").strip()]
    if missing:
        raise RuntimeError(
            f"missing required LiteLLM environment variable(s): {', '.join(missing)}"
        )
    return FactoryModelConfig(
        base_url=os.environ.get("FACTORY_LITELLM_URL", "http://litellm.localhost:8080/v1"),
        pm_key=os.environ["LITELLM_PM_KEY"],
        dev_key=os.environ["LITELLM_DEV_KEY"],
        test_key=os.environ["LITELLM_TEST_KEY"],
        sec_key=os.environ["LITELLM_SEC_KEY"],
        websites_repo_path=os.path.abspath(
            os.environ.get("FACTORY_WEBSITES_REPO", "../ai-factory-websites")
        ),
    )


@contextlib.asynccontextmanager
async def heartbeat_loop(detail: str, interval_s: float = 15.0) -> AsyncIterator[None]:
    """Emit periodic Temporal activity heartbeats while long local LLM calls run."""
    activity.heartbeat(detail)
    stop = asyncio.Event()

    async def _ticker() -> None:
        while not stop.is_set():
            try:
                await asyncio.wait_for(stop.wait(), timeout=interval_s)
            except TimeoutError:
                activity.heartbeat(detail)

    task = asyncio.create_task(_ticker())
    try:
        yield
    finally:
        stop.set()
        await task


class PydanticAgentActivities:
    """Temporal activities backed by PydanticAI agents and workspace tools."""

    def __init__(
        self,
        cfg: FactoryModelConfig,
        model_factory: ModelFactory | None = None,
    ) -> None:
        self.cfg = cfg
        self._model_factory = model_factory

    def _build_role_model(
        self,
        api_key: str,
        role: str,
        *,
        issue_number: int,
        attempt: int | None = None,
        site_slug: str = "",
    ) -> Model:
        trace_id = current_trace_id()
        tags = [f"issue-{issue_number}", f"role:{role}"]
        if attempt is not None:
            tags.append(f"attempt-{attempt}")
        if site_slug:
            tags.append(f"site:{site_slug}")
        headers: dict[str, str] = {"x-litellm-tags": ",".join(tags)}
        if trace_id:
            headers["x-litellm-trace-id"] = trace_id

        wf_id = ""
        with contextlib.suppress(RuntimeError):
            wf_id = activity.info().workflow_id or ""

        LOGGER.info(
            "running %s (workflow_id=%s issue=#%d site=%s attempt=%s trace_id=%s)",
            role,
            wf_id or "-",
            issue_number,
            site_slug or "-",
            attempt if attempt is not None else "-",
            trace_id or "-",
        )
        if self._model_factory is not None:
            return self._model_factory(self.cfg.base_url, api_key, role)
        return build_litellm_model(self.cfg.base_url, api_key, role, extra_headers=headers)

    @activity.defn(name="RunPMAgentActivity")
    async def run_pm_agent_activity(self, issue_data: dict[str, Any]) -> dict[str, Any]:
        issue = CustomerIssueInput.model_validate(issue_data)
        model = self._build_role_model(
            self.cfg.pm_key,
            "pm-agent",
            issue_number=issue.issue_number,
            site_slug=issue.target_site_slug,
        )
        async with heartbeat_loop(f"pm analyze issue #{issue.issue_number}"):
            out: PMDecisionOutput = await run_pm_agent(issue, self.cfg.websites_repo_path, model)
        return out.model_dump()

    @activity.defn(name="RunDeveloperAgentActivity")
    async def run_developer_agent_activity(self, raw_in: dict[str, Any]) -> dict[str, Any]:
        act_in = DeveloperActivityInput.model_validate(raw_in)
        model = self._build_role_model(
            self.cfg.dev_key,
            "dev-agent",
            issue_number=act_in.task.issue_number,
            attempt=act_in.task.attempt,
            site_slug=act_in.task.spec.site_slug,
        )
        async with heartbeat_loop(
            f"dev implement {act_in.task.spec.site_slug} (attempt {act_in.task.attempt})"
        ):
            out: ImplementationResult = await run_developer_agent(
                act_in.worktree_dir, act_in.task, self.cfg.websites_repo_path, model
            )
        return out.model_dump()

    @activity.defn(name="RunTesterAgentActivity")
    async def run_tester_agent_activity(self, raw_in: dict[str, Any]) -> dict[str, Any]:
        act_in = TesterActivityInput.model_validate(raw_in)
        model = self._build_role_model(
            self.cfg.test_key,
            "test-agent",
            issue_number=act_in.input.issue_number,
            attempt=act_in.input.attempt,
            site_slug=act_in.input.spec.site_slug,
        )
        async with heartbeat_loop(
            f"tester verify {act_in.input.spec.site_slug} (attempt {act_in.input.attempt})"
        ):
            out: TestReport = await run_tester_agent(
                act_in.worktree_dir, act_in.input, self.cfg.websites_repo_path, model
            )
        return out.model_dump()

    @activity.defn(name="RunReviewerActivity")
    async def run_reviewer_agent_activity(self, raw_in: dict[str, Any]) -> dict[str, Any]:
        act_in = ReviewerActivityInput.model_validate(raw_in)
        model = self._build_role_model(
            self.cfg.sec_key,
            "sec-agent",
            issue_number=act_in.input.issue.issue_number,
            attempt=act_in.input.test_report.attempt,
            site_slug=act_in.input.spec.site_slug,
        )
        async with heartbeat_loop(f"reviewing {act_in.input.spec.site_slug}"):
            out: ReviewResult = await run_reviewer_agent(
                act_in.worktree_dir, act_in.input, self.cfg.websites_repo_path, model
            )
        return out.model_dump()


async def main() -> None:
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s"
    )
    temporal_addr = os.environ.get("FACTORY_TEMPORAL_ADDRESS", "temporal.localhost:7233")
    namespace = os.environ.get("FACTORY_TEMPORAL_NAMESPACE", "default")
    task_queue = os.environ.get("FACTORY_AGENT_TASK_QUEUE", "ai-factory-agents")
    max_concurrent = int(os.environ.get("FACTORY_MAX_CONCURRENT_ACTIVITIES", "1"))

    tracing_enabled = not is_truthy_env("OTEL_SDK_DISABLED", False)
    _, shutdown_tracing = setup_tracing(enabled=tracing_enabled)

    cfg = load_model_config_from_env()
    acts = PydanticAgentActivities(cfg)

    interceptors: list[Interceptor] = [TracingInterceptor()] if tracing_enabled else []
    try:
        client = await Client.connect(
            temporal_addr,
            namespace=namespace,
            interceptors=interceptors,
        )
        worker = Worker(
            client,
            task_queue=task_queue,
            activities=[
                acts.run_pm_agent_activity,
                acts.run_developer_agent_activity,
                acts.run_tester_agent_activity,
                acts.run_reviewer_agent_activity,
            ],
            max_concurrent_activities=max_concurrent,
        )

        stop_event = asyncio.Event()
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGINT, signal.SIGTERM):
            loop.add_signal_handler(sig, stop_event.set)

        LOGGER.info(
            "starting PydanticAI Temporal worker on %s (namespace=%s queue=%s concurrency=%d tracing=%s)",
            temporal_addr,
            namespace,
            task_queue,
            max_concurrent,
            tracing_enabled,
        )
        async with worker:
            await stop_event.wait()
    finally:
        shutdown_tracing()


if __name__ == "__main__":
    asyncio.run(main())
