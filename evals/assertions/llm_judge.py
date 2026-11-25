"""Structured PydanticAI LLM-as-a-judge assertion backed by local LiteLLM sec-agent."""

from __future__ import annotations

import asyncio
import os
import pathlib
import sys
from typing import Any

from pydantic import BaseModel

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
AGENTS_SRC = REPO_ROOT / "agents_py" / "src"
if str(AGENTS_SRC) not in sys.path:
    sys.path.insert(0, str(AGENTS_SRC))

from agents_py.agents import build_litellm_model  # noqa: E402
from pydantic_ai import Agent  # noqa: E402
from pydantic_ai.settings import ModelSettings  # noqa: E402


class _JudgeVerdict(BaseModel):
    passed: bool
    reason: str


def judge_output(output: str, context: dict[str, Any]) -> dict[str, Any]:
    """Evaluate PMDecisionOutput JSON against the test case rubric using sec-agent."""
    v = context.get("vars") or {}
    rubric = str(v.get("rubric", "")).strip()
    if not rubric:
        return {"pass": True, "score": 1.0, "reason": "no rubric specified"}

    base_url = os.environ.get("FACTORY_LITELLM_URL", "http://litellm.localhost:8080/v1")
    api_key = os.environ["LITELLM_SEC_KEY"]
    model = build_litellm_model(base_url, api_key, "sec-agent")

    judge_agent: Agent[None, _JudgeVerdict] = Agent(
        model=model,
        output_type=_JudgeVerdict,
        system_prompt=(
            "You are an impartial QA evaluator for an AI Software Factory PM Agent. "
            "Given a customer issue, a rubric, and the PM Agent's JSON output, return "
            "passed=true if the output satisfies the rubric, or passed=false with a concise reason."
        ),
        retries=1,
        model_settings=ModelSettings(temperature=0.0, max_tokens=2048),
    )

    prompt = (
        f"Issue Title: {v.get('issue_title', '')}\n"
        f"Rubric: {rubric}\n\n"
        f"PM Agent JSON Output:\n{output[:6000]}"
    )
    res = asyncio.run(judge_agent.run(prompt))
    return {
        "pass": bool(res.output.passed),
        "score": 1.0 if res.output.passed else 0.0,
        "reason": res.output.reason.strip()
        or ("rubric satisfied" if res.output.passed else "rubric failed"),
    }
