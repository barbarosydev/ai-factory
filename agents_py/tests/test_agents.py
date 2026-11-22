"""Tests for PydanticAI agents, context compaction, fail-closed QA, and MCP tools."""

import asyncio
import json
import pathlib
import subprocess

from agents_py.agents import (
    compact_tool_history,
    run_developer_agent,
    run_pm_agent,
    run_reviewer_agent,
    run_tester_agent,
)
from agents_py.contracts import (
    CustomerIssueInput,
    DeveloperTaskInput,
    ReviewInput,
    SectionSpec,
    TestReport,
    TestRunInput,
    WebsiteSpecification,
)
from agents_py.mcp_tools import WorkspaceDeps, build_workspace_mcp_server
from pydantic_ai.messages import (
    ModelMessage,
    ModelRequest,
    ModelResponse,
    TextPart,
    ToolCallPart,
    ToolReturnPart,
    UserPromptPart,
)
from pydantic_ai.models.function import AgentInfo, FunctionModel


def _sample_spec() -> WebsiteSpecification:
    return WebsiteSpecification(
        site_slug="kaffeekultur-berlin",
        page_title="Kaffeekultur Berlin",
        sections=[
            SectionSpec(
                id="hero",
                heading="Kaffeekultur Berlin",
                purpose="Welcome visitors with the roastery tagline and primary call to action.",
                required_keywords=["Kreuzberg"],
            ),
            SectionSpec(
                id="menu", heading="Menu", purpose="Drinks", required_keywords=["Espresso"]
            ),
            SectionSpec(id="hours", heading="Hours", purpose="Schedule", required_keywords=["Mon"]),
        ],
    )


async def test_pm_agent_structured_output_and_normalization(tmp_path: pathlib.Path) -> None:
    (tmp_path / "sites" / "bakery-mitte").mkdir(parents=True)

    def _pm_model(messages: list[ModelMessage], info: AgentInfo) -> ModelResponse:
        # Verify untrusted input boundary framing was applied
        prompt_text = str(messages[-1])
        assert "<untrusted_github_input" in prompt_text
        assert info.output_tools
        tool_name = info.output_tools[0].name
        payload = {
            "status": "ready",
            "spec": {
                "site_slug": "Kaffeekultur Berlin!!",
                "page_title": "Kaffeekultur Berlin",
                "sections": [
                    {"id": "hero", "heading": "Kaffeekultur", "required_keywords": ["Kreuzberg"]},
                    {"id": "menu", "heading": "Menu", "required_keywords": ["Espresso"]},
                    {"id": "hours", "heading": "Hours", "required_keywords": ["Mon"]},
                ],
            },
        }
        return ModelResponse(parts=[ToolCallPart(tool_name=tool_name, args=json.dumps(payload))])

    out = await run_pm_agent(
        CustomerIssueInput(issue_number=1, issue_title="Coffee", issue_body="Coffee in Kreuzberg"),
        str(tmp_path),
        FunctionModel(_pm_model),
    )
    assert out.status == "ready"
    assert out.spec is not None
    assert out.spec.site_slug == "kaffeekultur-berlin"
    assert len(out.spec.navigation) == 3


async def test_developer_agent_write_site_bundle(tmp_path: pathlib.Path) -> None:
    wt = tmp_path / "wt"
    repo = tmp_path / "repo"
    (repo / "sites" / "_template").mkdir(parents=True)

    call_count = 0

    def _dev_model(messages: list[ModelMessage], info: AgentInfo) -> ModelResponse:
        nonlocal call_count
        call_count += 1
        if call_count == 1:
            html = """<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Kaffeekultur</title><link rel="stylesheet" href="styles.css"></head>
<body>
  <header><nav><a href="#menu">Menu</a></nav></header>
  <main>
    <section id="hero"><h1>Kaffeekultur</h1><p>Freshly roasted in Kreuzberg.</p></section>
    <section id="menu"><h2>Menu</h2><p>Single-origin Espresso daily.</p></section>
    <section id="hours"><h2>Hours</h2><p>Mon-Fri 08:00-18:00</p></section>
  </main>
  <footer>Kaffeekultur Berlin</footer>
</body>
</html>"""
            args = json.dumps(
                {
                    "index_html": html,
                    "styles_css": "body { margin: 0; font-family: system-ui, sans-serif; }",
                    "summary": "Built authentic coffee shop page",
                }
            )
            return ModelResponse(parts=[ToolCallPart(tool_name="write_site_bundle", args=args)])
        return ModelResponse(parts=[TextPart("Completed authentic website implementation.")])

    res = await run_developer_agent(
        str(wt),
        DeveloperTaskInput(issue_number=5, attempt=1, spec=_sample_spec()),
        str(repo),
        FunctionModel(_dev_model),
    )
    assert res.files_changed == ["index.html", "styles.css"]
    assert "write_site_bundle" in res.tool_trace


def test_compact_tool_history_trims_old_writes() -> None:
    big_args = json.dumps({"index_html": "x" * 1000})
    msgs: list[ModelMessage] = [
        ModelRequest(parts=[UserPromptPart(content="sys+user")]),
        ModelResponse(parts=[TextPart("ok")]),
        ModelResponse(
            parts=[ToolCallPart(tool_name="write_site_bundle", args=big_args, tool_call_id="1")]
        ),
        ModelRequest(
            parts=[
                ToolReturnPart(tool_name="write_site_bundle", content="y" * 2000, tool_call_id="1")
            ]
        ),
        ModelResponse(parts=[TextPart("turn2")]),
        ModelRequest(parts=[UserPromptPart(content="recent1")]),
        ModelResponse(parts=[TextPart("recent2")]),
        ModelRequest(parts=[UserPromptPart(content="recent3")]),
    ]
    compacted = compact_tool_history(msgs)
    old_call = compacted[2]
    assert isinstance(old_call, ModelResponse)
    part = old_call.parts[0]
    assert isinstance(part, ToolCallPart)
    assert "compacted" in str(part.args)


async def test_reviewer_blocks_path_escape_and_fails_closed(tmp_path: pathlib.Path) -> None:
    wt = tmp_path / "wt"
    wt.mkdir()
    await asyncio.to_thread(subprocess.run, ["git", "init", "-q", str(wt)], check=True)
    (wt / "README.md").write_text("escape", encoding="utf-8")

    called = False

    def _never_called(_: list[ModelMessage], __: AgentInfo) -> ModelResponse:
        nonlocal called
        called = True
        return ModelResponse(parts=[TextPart("should not run")])

    res = await run_reviewer_agent(
        str(wt),
        ReviewInput(
            issue=CustomerIssueInput(issue_number=1, issue_title="t", issue_body="b"),
            spec=_sample_spec(),
            test_report=TestReport(attempt=1, passed=True, semantic_quality_ok=True),
        ),
        str(tmp_path),
        FunctionModel(_never_called),
    )
    assert not called
    assert not res.approved
    assert not res.path_isolation_ok

    # Clean isolation, but LLM never calls submit_review -> must fail closed
    (wt / "README.md").unlink()
    site_dir = wt / "sites" / "kaffeekultur-berlin"
    site_dir.mkdir(parents=True, exist_ok=True)
    (site_dir / "index.html").write_text("<h1>Hi</h1>", encoding="utf-8")

    def _noop_llm(_: list[ModelMessage], __: AgentInfo) -> ModelResponse:
        return ModelResponse(parts=[TextPart("Looks okay in plain text")])

    res2 = await run_reviewer_agent(
        str(wt),
        ReviewInput(
            issue=CustomerIssueInput(issue_number=1, issue_title="t", issue_body="b"),
            spec=_sample_spec(),
            test_report=TestReport(attempt=1, passed=True, semantic_quality_ok=True),
        ),
        str(tmp_path),
        FunctionModel(_noop_llm),
    )
    assert not res2.approved
    assert res2.findings


async def test_mcp_server_registers_workspace_tools(tmp_path: pathlib.Path) -> None:
    mcp = build_workspace_mcp_server(str(tmp_path), "kaffeekultur-berlin", str(tmp_path))
    tools = await mcp.list_tools()
    names = {t.name for t in tools}
    assert {
        "mcp_read_template",
        "mcp_list_files",
        "mcp_read_file",
        "mcp_inspect_dom",
        "mcp_scan_external_urls",
        "mcp_inspect_git_diff",
    }.issubset(names)


async def test_tester_agent_passes_and_fails_closed_with_injected_runner(
    tmp_path: pathlib.Path,
) -> None:
    wt = tmp_path / "wt"
    site_dir = wt / "sites" / "kaffeekultur-berlin"
    site_dir.mkdir(parents=True)
    (site_dir / "index.html").write_text("<h1>Kaffeekultur</h1>", encoding="utf-8")

    async def _passing_pw(_: WorkspaceDeps) -> dict[str, object]:
        return {
            "passed": True,
            "duration_ms": 42,
            "checks": [{"name": "gate-1", "passed": True, "detail": ""}],
            "failures": [],
            "desktop_screenshot_path": "sites/kaffeekultur-berlin/evidence/desktop.png",
            "mobile_screenshot_path": "sites/kaffeekultur-berlin/evidence/mobile.png",
        }

    step = 0

    def _tester_happy(messages: list[ModelMessage], info: AgentInfo) -> ModelResponse:
        nonlocal step
        step += 1
        if step == 1:
            return ModelResponse(
                parts=[ToolCallPart(tool_name="run_playwright_suite_tool", args="{}")]
            )
        if step == 2:
            args = json.dumps(
                {
                    "semantic_quality_ok": True,
                    "tester_summary": "All gates passed with authentic copy.",
                    "failures": [],
                }
            )
            return ModelResponse(parts=[ToolCallPart(tool_name="submit_test_report", args=args)])
        return ModelResponse(parts=[TextPart("Verified.")])

    report = await run_tester_agent(
        str(wt),
        TestRunInput(issue_number=7, attempt=1, spec=_sample_spec()),
        str(tmp_path),
        FunctionModel(_tester_happy),
        playwright_runner=_passing_pw,
    )
    assert report.passed
    assert report.semantic_quality_ok
    assert report.duration_ms == 42

    # Fail-closed when LLM omits submit_test_report
    def _tester_forgot_submit(_: list[ModelMessage], __: AgentInfo) -> ModelResponse:
        return ModelResponse(parts=[TextPart("Looks good without calling tools.")])

    failed_report = await run_tester_agent(
        str(wt),
        TestRunInput(issue_number=7, attempt=2, spec=_sample_spec()),
        str(tmp_path),
        FunctionModel(_tester_forgot_submit),
        playwright_runner=_passing_pw,
    )
    assert not failed_report.passed
    assert not failed_report.semantic_quality_ok
    assert any(f.check_name == "tester-submit-missing" for f in failed_report.failures)
