"""Promptfoo custom provider invoking the real PydanticAI run_pm_agent pipeline."""

from __future__ import annotations

import asyncio
import json
import os
import pathlib
import sys
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
AGENTS_SRC = REPO_ROOT / "agents_py" / "src"
if str(AGENTS_SRC) not in sys.path:
    sys.path.insert(0, str(AGENTS_SRC))

from agents_py.agents import build_litellm_model, run_pm_agent  # noqa: E402
from agents_py.contracts import CustomerIssueInput  # noqa: E402


def call_api(
    prompt: str,
    options: dict[str, Any],
    context: dict[str, Any],
) -> dict[str, Any]:
    """Run the production PydanticAI PM agent against local LiteLLM and return JSON."""
    v = context.get("vars") or {}
    issue = CustomerIssueInput(
        issue_number=int(v.get("issue_number", 999)),
        issue_title=str(v.get("issue_title", "")),
        issue_body=str(v.get("issue_body", "")),
        request_type=str(v.get("request_type", "new_website")),
        target_site_slug=str(v.get("target_site_slug", "")),
        existing_sites=list(
            v.get(
                "existing_sites",
                ["kaffeekultur-berlin", "konditorei-flora-berlin", "buchhandlung-morgenrot"],
            )
        ),
    )

    base_url = os.environ.get("FACTORY_LITELLM_URL", "http://litellm.localhost:8080/v1")
    api_key = os.environ["LITELLM_PM_KEY"]
    model = build_litellm_model(
        base_url,
        api_key,
        "pm-agent",
        extra_headers={"x-litellm-tags": f"eval,role:pm-agent,case-{issue.issue_number}"},
    )

    websites_repo = os.path.abspath(
        os.environ.get("FACTORY_WEBSITES_REPO", str(REPO_ROOT.parent / "ai-factory-websites"))
    )

    try:
        out = asyncio.run(run_pm_agent(issue, websites_repo, model))
    except Exception as exc:
        return {"error": f"run_pm_agent failed: {exc}"}

    return {"output": json.dumps(out.model_dump(), ensure_ascii=False)}
