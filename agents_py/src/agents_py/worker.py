"""Temporal Python Activity Worker for PydanticAI agents."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import os
import signal
from collections.abc import AsyncIterator

from temporalio import activity
from temporalio.client import Client
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

LOGGER = logging.getLogger("agents_py.worker")


def load_model_config_from_env() -> FactoryModelConfig:
    return FactoryModelConfig(
        base_url=os.environ.get("FACTORY_LITELLM_URL", "http://litellm.localhost:8080/v1"),
        pm_key=os.environ["LITELLM_PM_KEY"],
        dev_key=os.environ["LITELLM_DEV_KEY"],
        test_key=os.environ["LITELLM_TEST_KEY"],
        sec_key=os.environ["LITELLM_SEC_KEY"],
        websites_repo_path=os.path.abspath(
            os.environ.get("FACTORY_WEBSITES_REPO", "../ai-factory-websites")
        ),
        num_ctx=int(os.environ.get("FACTORY_NUM_CTX", "32768")),
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
    """Temporal activities backed by PydanticAI + MCP workspace tools."""

    def __init__(self, cfg: FactoryModelConfig) -> None:
        self.cfg = cfg

    @activity.defn(name="RunPMAgentActivity")
    async def run_pm_agent_activity(self, issue_data: dict) -> dict:
        issue = CustomerIssueInput.model_validate(issue_data)
        model = build_litellm_model(self.cfg.base_url, self.cfg.pm_key, "pm-agent")
        async with heartbeat_loop(f"pm analyze issue #{issue.issue_number}"):
            out: PMDecisionOutput = await run_pm_agent(
                issue, self.cfg.websites_repo_path, model, self.cfg.num_ctx
            )
        return out.model_dump()

    @activity.defn(name="RunDeveloperAgentActivity")
    async def run_developer_agent_activity(self, raw_in: dict) -> dict:
        act_in = DeveloperActivityInput.model_validate(raw_in)
        model = build_litellm_model(self.cfg.base_url, self.cfg.dev_key, "dev-agent")
        async with heartbeat_loop(
            f"dev implement {act_in.task.spec.site_slug} (attempt {act_in.task.attempt})"
        ):
            out: ImplementationResult = await run_developer_agent(
                act_in.worktree_dir, act_in.task, self.cfg.websites_repo_path, model
            )
        return out.model_dump()

    @activity.defn(name="RunTesterAgentActivity")
    async def run_tester_agent_activity(self, raw_in: dict) -> dict:
        act_in = TesterActivityInput.model_validate(raw_in)
        model = build_litellm_model(self.cfg.base_url, self.cfg.test_key, "test-agent")
        async with heartbeat_loop(
            f"tester verify {act_in.input.spec.site_slug} (attempt {act_in.input.attempt})"
        ):
            out: TestReport = await run_tester_agent(
                act_in.worktree_dir, act_in.input, self.cfg.websites_repo_path, model
            )
        return out.model_dump()

    @activity.defn(name="RunReviewerActivity")
    async def run_reviewer_agent_activity(self, raw_in: dict) -> dict:
        act_in = ReviewerActivityInput.model_validate(raw_in)
        model = build_litellm_model(self.cfg.base_url, self.cfg.sec_key, "sec-agent")
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

    cfg = load_model_config_from_env()
    acts = PydanticAgentActivities(cfg)

    client = await Client.connect(temporal_addr, namespace=namespace)
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
        "starting PydanticAI Temporal worker on %s (namespace=%s queue=%s concurrency=%d)",
        temporal_addr,
        namespace,
        task_queue,
        max_concurrent,
    )
    async with worker:
        await stop_event.wait()


if __name__ == "__main__":
    asyncio.run(main())
