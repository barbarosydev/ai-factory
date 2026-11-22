"""Tests for Temporal worker config validation and PydanticAgentActivities DI."""

import json
import pathlib

import pytest
from agents_py.agents import FactoryModelConfig
from agents_py.contracts import CustomerIssueInput
from agents_py.worker import PydanticAgentActivities, load_model_config_from_env
from pydantic_ai.messages import ModelMessage, ModelResponse, ToolCallPart
from pydantic_ai.models.function import AgentInfo, FunctionModel


def test_load_model_config_from_env_reports_missing_keys(monkeypatch: pytest.MonkeyPatch) -> None:
    for var in ("LITELLM_PM_KEY", "LITELLM_DEV_KEY", "LITELLM_TEST_KEY", "LITELLM_SEC_KEY"):
        monkeypatch.delenv(var, raising=False)

    with pytest.raises(RuntimeError, match="LITELLM_PM_KEY"):
        load_model_config_from_env()


async def test_pm_activity_uses_injected_model_factory_and_heartbeats(
    tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr("agents_py.worker.activity.heartbeat", lambda _detail: None)

    def _pm_fn(_: list[ModelMessage], info: AgentInfo) -> ModelResponse:
        tool_name = info.output_tools[0].name
        payload = {
            "status": "needs_clarification",
            "clarification_questions": ["What are your opening hours?"],
        }
        return ModelResponse(parts=[ToolCallPart(tool_name=tool_name, args=json.dumps(payload))])

    cfg = FactoryModelConfig(
        base_url="http://localhost:8080/v1",
        pm_key="test-pm-key",
        dev_key="test-dev-key",
        test_key="test-test-key",
        sec_key="test-sec-key",
        websites_repo_path=str(tmp_path),
    )
    acts = PydanticAgentActivities(
        cfg,
        model_factory=lambda _base, _key, _name: FunctionModel(_pm_fn),
    )

    issue = CustomerIssueInput(issue_number=12, issue_title="Bakery", issue_body="Need a website")
    out = await acts.run_pm_agent_activity(issue.model_dump())
    assert out["status"] == "needs_clarification"
    assert out["clarification_questions"] == ["What are your opening hours?"]
