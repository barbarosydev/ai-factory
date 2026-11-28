"""Offline tests for the zero-GitHub-App local_cli pipeline."""

from __future__ import annotations

import json
import pathlib

from agents_py.local_cli import run_local_pipeline
from agents_py.workspace import WorkspaceDeps
from pydantic_ai.messages import ModelMessage, ModelResponse, TextPart, ToolCallPart
from pydantic_ai.models import Model
from pydantic_ai.models.function import AgentInfo, FunctionModel


def _make_role_models() -> dict[str, Model]:
    def _pm(_: list[ModelMessage], info: AgentInfo) -> ModelResponse:
        tool_name = info.output_tools[0].name
        return ModelResponse(
            parts=[
                ToolCallPart(
                    tool_name=tool_name,
                    args=json.dumps(
                        {
                            "status": "ready",
                            "spec": {
                                "site_slug": "local-roastery",
                                "operation": "create",
                                "category": "Coffee Shop",
                                "page_title": "Local Roastery",
                                "tagline": "Fresh coffee daily.",
                                "theme": {
                                    "primary_color": "#2C1A12",
                                    "background_color": "#FAF6F0",
                                    "accent_color": "#2D6A4F",
                                    "font_pairing": "Georgia, serif + system-ui, sans-serif",
                                },
                                "navigation": [
                                    {"label": "About", "href": "#about"},
                                    {"label": "Menu", "href": "#menu"},
                                ],
                                "sections": [
                                    {
                                        "id": "hero",
                                        "heading": "Local Roastery",
                                        "purpose": "Welcome coffee lovers",
                                        "required_keywords": ["Roastery"],
                                        "required_elements": ["h1", "p"],
                                    },
                                    {
                                        "id": "about",
                                        "heading": "About",
                                        "purpose": "Roasting story",
                                        "required_keywords": ["Beans"],
                                        "required_elements": ["h2", "p"],
                                    },
                                    {
                                        "id": "menu",
                                        "heading": "Menu",
                                        "purpose": "Coffee list",
                                        "required_keywords": ["Espresso"],
                                        "required_elements": ["h2", "p"],
                                    },
                                ],
                                "footer_text": "© 2026 Local Roastery",
                            },
                        }
                    ),
                )
            ]
        )

    def _dev(_: list[ModelMessage], __: AgentInfo) -> ModelResponse:
        html = """<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Local Roastery</title><link rel="stylesheet" href="styles.css"></head>
<body>
  <header><nav><a href="#about">About</a><a href="#menu">Menu</a></nav></header>
  <main>
    <section id="hero"><h1>Local Roastery</h1><p>Neighborhood Roastery.</p></section>
    <section id="about"><h2>About</h2><p>Direct trade Beans roasted weekly.</p></section>
    <section id="menu"><h2>Menu</h2><p>Rich Espresso shots daily.</p></section>
  </main>
  <footer>© 2026 Local Roastery</footer>
</body>
</html>"""
        css = (
            ":root { --primary: #2C1A12; --bg: #FAF6F0; --accent: #2D6A4F; }\n"
            "body { margin: 0; color: var(--primary); background: var(--bg); font-family: system-ui, sans-serif; }\n"
            "header, main, footer { max-width: 64rem; margin: 0 auto; padding: 1.5rem; }\n"
            "nav a { color: var(--primary); margin-right: 1rem; }\n"
        )
        return ModelResponse(
            parts=[
                ToolCallPart(
                    tool_name="write_site_bundle",
                    args=json.dumps({"index_html": html, "styles_css": css, "summary": "Built."}),
                )
            ]
        )

    test_step = 0

    def _test(_: list[ModelMessage], __: AgentInfo) -> ModelResponse:
        nonlocal test_step
        test_step += 1
        if test_step == 1:
            return ModelResponse(parts=[ToolCallPart(tool_name="run_playwright_suite", args="{}")])
        if test_step == 2:
            return ModelResponse(
                parts=[
                    ToolCallPart(
                        tool_name="submit_test_report",
                        args=json.dumps(
                            {
                                "semantic_quality_ok": True,
                                "tester_summary": "All 11 gates clean.",
                                "failures": [],
                            }
                        ),
                    )
                ]
            )
        return ModelResponse(parts=[TextPart("Verified.")])

    sec_done = False

    def _sec(_: list[ModelMessage], __: AgentInfo) -> ModelResponse:
        nonlocal sec_done
        if sec_done:
            return ModelResponse(parts=[TextPart("Approved.")])
        sec_done = True
        return ModelResponse(
            parts=[
                ToolCallPart(
                    tool_name="submit_review",
                    args=json.dumps(
                        {
                            "approved": True,
                            "spec_fidelity_ok": True,
                            "summary_markdown": "Clean isolated build.",
                            "findings": [],
                        }
                    ),
                )
            ]
        )

    return {
        "pm-agent": FunctionModel(_pm),
        "dev-agent": FunctionModel(_dev),
        "test-agent": FunctionModel(_test),
        "sec-agent": FunctionModel(_sec),
    }


async def test_local_pipeline_spec_only_and_full_verified_build(tmp_path: pathlib.Path) -> None:
    websites_repo = tmp_path / "websites"
    (websites_repo / "sites" / "_template").mkdir(parents=True)
    out_root = tmp_path / "out"

    models_spec = _make_role_models()
    spec_outcome = await run_local_pipeline(
        prompt="Neighborhood coffee roastery",
        slug_hint="local-roastery",
        output_root=str(out_root),
        websites_repo_path=str(websites_repo),
        base_url="http://localhost:8080/v1",
        pm_key="k1",
        dev_key="k2",
        test_key="k3",
        sec_key="k4",
        spec_only=True,
        model_factory=lambda _b, _k, role: models_spec[role],
    )
    assert spec_outcome.status == "ready"
    assert spec_outcome.site_slug == "local-roastery"
    assert not (out_root / "sites" / "local-roastery" / "index.html").exists()

    async def _fake_pw(_: WorkspaceDeps) -> dict[str, object]:
        return {
            "passed": True,
            "duration_ms": 25,
            "checks": [{"name": "gate-01", "passed": True, "detail": ""}],
            "failures": [],
            "desktop_screenshot_path": "sites/local-roastery/evidence/desktop.png",
            "mobile_screenshot_path": "sites/local-roastery/evidence/mobile.png",
        }

    models_full = _make_role_models()
    full_outcome = await run_local_pipeline(
        prompt="Neighborhood coffee roastery",
        slug_hint="local-roastery",
        output_root=str(out_root),
        websites_repo_path=str(websites_repo),
        base_url="http://localhost:8080/v1",
        pm_key="k1",
        dev_key="k2",
        test_key="k3",
        sec_key="k4",
        model_factory=lambda _b, _k, role: models_full[role],
        playwright_runner=_fake_pw,
        visual_qa=False,
    )
    assert full_outcome.status == "verified"
    assert full_outcome.attempts == 1
    assert (out_root / "sites" / "local-roastery" / "index.html").is_file()
    assert (out_root / "sites" / "local-roastery" / "spec.json").is_file()
