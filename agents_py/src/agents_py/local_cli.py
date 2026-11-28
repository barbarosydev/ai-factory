"""Zero-GitHub-App local CLI running the full PydanticAI multi-agent factory pipeline."""

from __future__ import annotations

import argparse
import asyncio
import os
import pathlib
import subprocess
import sys
import time
from dataclasses import dataclass
from typing import Any

from opentelemetry import trace
from opentelemetry.trace import Status, StatusCode
from pydantic_ai.models import Model

from agents_py.agents import (
    DiffInspector,
    PlaywrightRunner,
    build_litellm_model,
    run_developer_agent,
    run_pm_agent,
    run_reviewer_agent,
    run_tester_agent,
)
from agents_py.contracts import (
    CustomerIssueInput,
    DeveloperTaskInput,
    FailureEvidence,
    PMDecisionOutput,
    ReviewInput,
    ReviewResult,
    TestReport,
    TestRunInput,
)
from agents_py.security import SiteSandbox, site_dir_for_worktree
from agents_py.telemetry import current_trace_id, is_truthy_env, setup_tracing
from agents_py.worker import ModelFactory, load_model_config_from_env
from agents_py.workspace import run_playwright_suite


@dataclass
class LocalBuildOutcome:
    status: str  # "ready" | "verified" | "needs_clarification" | "rejected" | "failed"
    site_slug: str = ""
    output_dir: str = ""
    attempts: int = 0
    trace_id: str = ""
    pm_decision: PMDecisionOutput | None = None
    test_report: TestReport | None = None
    review: ReviewResult | None = None
    duration_ms: int = 0


def _local_diff_inspector(worktree_dir: str, slug: str) -> dict[str, Any]:
    """Deterministic file listing audit for local non-git scratch builds."""
    sb = SiteSandbox(site_dir_for_worktree(worktree_dir, slug))
    files = [f"sites/{slug}/{rel}" for rel in sb.list_files()]
    return {
        "changed_files": files,
        "path_isolation_ok": len(files) > 0,
        "diff_stat": f"{len(files)} file(s) generated in local workspace",
    }


async def run_local_pipeline(
    *,
    prompt: str,
    title: str = "",
    category: str = "Local Business",
    slug_hint: str = "",
    output_root: str,
    websites_repo_path: str,
    base_url: str,
    pm_key: str,
    dev_key: str,
    test_key: str,
    sec_key: str,
    spec_only: bool = False,
    max_attempts: int = 3,
    model_factory: ModelFactory | None = None,
    playwright_runner: PlaywrightRunner = run_playwright_suite,
    diff_inspector: DiffInspector = _local_diff_inspector,
    visual_qa: bool | None = None,
) -> LocalBuildOutcome:
    """Execute PM -> Developer -> Tester (Playwright + Visual QA) -> Reviewer locally."""
    start = time.monotonic()
    tracer = trace.get_tracer("ai-factory.local_cli")
    clean_title = title.strip() or f"[New Site]: {slug_hint or prompt.strip()[:48]}"
    issue_body = (
        f"### Business / Site Category\n{category}\n\n"
        f"### What should the website include?\n{prompt.strip()}\n"
    )
    issue = CustomerIssueInput(
        issue_number=0,
        issue_title=clean_title,
        issue_body=issue_body,
        request_type="new_website",
        target_site_slug=slug_hint.strip(),
    )

    def _make_model(api_key: str, role: str, attempt: int | None = None, slug: str = "") -> Model:
        if model_factory is not None:
            return model_factory(base_url, api_key, role)
        tid = current_trace_id()
        tags = ["local-cli", f"role:{role}"]
        if attempt is not None:
            tags.append(f"attempt-{attempt}")
        if slug:
            tags.append(f"site:{slug}")
        headers: dict[str, str] = {"x-litellm-tags": ",".join(tags)}
        if tid:
            headers["x-litellm-trace-id"] = tid
        return build_litellm_model(base_url, api_key, role, extra_headers=headers)

    with tracer.start_as_current_span(
        "factory.local_build",
        attributes={
            "openinference.span.kind": "CHAIN",
            "factory.mode": "local_cli",
            "factory.spec_only": spec_only,
        },
    ) as root_span:
        tid = current_trace_id()
        pm_out = await run_pm_agent(issue, websites_repo_path, _make_model(pm_key, "pm-agent"))
        if pm_out.status != "ready" or pm_out.spec is None:
            root_span.set_status(Status(StatusCode.OK))
            return LocalBuildOutcome(
                status=pm_out.status,
                trace_id=tid,
                pm_decision=pm_out,
                duration_ms=int((time.monotonic() - start) * 1000),
            )

        spec = pm_out.spec
        root_span.set_attribute("session.id", f"local-{spec.site_slug}")
        root_span.set_attribute("site.slug", spec.site_slug)

        if spec_only:
            root_span.set_status(Status(StatusCode.OK))
            return LocalBuildOutcome(
                status="ready",
                site_slug=spec.site_slug,
                trace_id=tid,
                pm_decision=pm_out,
                duration_ms=int((time.monotonic() - start) * 1000),
            )

        worktree_root = pathlib.Path(output_root).resolve()
        site_dir = site_dir_for_worktree(str(worktree_root), spec.site_slug)
        site_dir.mkdir(parents=True, exist_ok=True)
        (site_dir / "spec.json").write_text(spec.model_dump_json(indent=2) + "\n", encoding="utf-8")

        prior_failures: list[FailureEvidence] = []
        last_report: TestReport | None = None
        last_review: ReviewResult | None = None
        bounded_attempts = max(1, min(max_attempts, 3))

        for attempt in range(1, bounded_attempts + 1):
            dev_res = await run_developer_agent(
                str(worktree_root),
                DeveloperTaskInput(
                    issue_number=0,
                    attempt=attempt,
                    spec=spec,
                    prior_failures=prior_failures,
                ),
                websites_repo_path,
                _make_model(dev_key, "dev-agent", attempt=attempt, slug=spec.site_slug),
            )
            last_report = await run_tester_agent(
                str(worktree_root),
                TestRunInput(issue_number=0, attempt=attempt, spec=spec),
                websites_repo_path,
                _make_model(test_key, "test-agent", attempt=attempt, slug=spec.site_slug),
                playwright_runner=playwright_runner,
                visual_qa=visual_qa,
            )
            if not last_report.passed:
                prior_failures = last_report.failures
                continue

            last_review = await run_reviewer_agent(
                str(worktree_root),
                ReviewInput(
                    issue=issue,
                    spec=spec,
                    test_report=last_report,
                    changed_files=dev_res.files_changed,
                ),
                websites_repo_path,
                _make_model(sec_key, "sec-agent", attempt=attempt, slug=spec.site_slug),
                diff_inspector=diff_inspector,
            )
            if last_review.approved:
                root_span.set_status(Status(StatusCode.OK))
                return LocalBuildOutcome(
                    status="verified",
                    site_slug=spec.site_slug,
                    output_dir=str(site_dir),
                    attempts=attempt,
                    trace_id=tid,
                    pm_decision=pm_out,
                    test_report=last_report,
                    review=last_review,
                    duration_ms=int((time.monotonic() - start) * 1000),
                )
            prior_failures = [
                FailureEvidence(
                    check_name="reviewer-finding",
                    expected="Clean path isolation, zero external assets, and spec fidelity",
                    actual=finding,
                    remediation=finding,
                )
                for finding in last_review.findings
            ]

        root_span.set_status(Status(StatusCode.ERROR, "exhausted repair attempts"))
        return LocalBuildOutcome(
            status="failed",
            site_slug=spec.site_slug,
            output_dir=str(site_dir),
            attempts=bounded_attempts,
            trace_id=tid,
            pm_decision=pm_out,
            test_report=last_report,
            review=last_review,
            duration_ms=int((time.monotonic() - start) * 1000),
        )


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="agents_py.local_cli",
        description="Build and verify a static website locally through the 4-agent PydanticAI pipeline without GitHub App setup.",
    )
    p.add_argument("--prompt", required=True, help="Plain-language website description")
    p.add_argument("--title", default="", help="Optional short website title")
    p.add_argument("--category", default="Local Business", help="Business category")
    p.add_argument("--slug", default="", help="Optional lowercase kebab-case site slug")
    p.add_argument(
        "--output-dir",
        default="./.local-builds",
        help="Workspace directory where sites/<slug>/ will be written (default: ./.local-builds)",
    )
    p.add_argument(
        "--spec-only",
        action="store_true",
        help="Run only pm-agent and print the normalized WebsiteSpecification JSON",
    )
    p.add_argument(
        "--max-attempts",
        type=int,
        default=3,
        help="Maximum bounded repair attempts (1-3, default: 3)",
    )
    p.add_argument(
        "--no-visual-qa",
        action="store_true",
        help="Disable Multimodal Visual QA screenshot inspection in test-agent",
    )
    p.add_argument(
        "--preview",
        action="store_true",
        help="Launch the local loopback preview server after a verified build",
    )
    return p


def _print_outcome(outcome: LocalBuildOutcome) -> None:
    print(f"\n=== ai-factory Local Build ({outcome.status.upper()}) ===")
    if outcome.site_slug:
        print(f"  Site Slug:  {outcome.site_slug}")
    if outcome.output_dir:
        print(f"  Output Dir: {outcome.output_dir}")
    print(f"  Duration:   {outcome.duration_ms} ms")
    if outcome.trace_id:
        phoenix_url = os.environ.get("FACTORY_PHOENIX_URL", "http://phoenix.localhost:8080")
        print(f"  Trace ID:   {outcome.trace_id} ({phoenix_url})")

    if outcome.pm_decision and outcome.status == "needs_clarification":
        print("\nClarification Questions:")
        for i, q in enumerate(outcome.pm_decision.clarification_questions, start=1):
            print(f"  {i}. {q}")
    elif outcome.pm_decision and outcome.status == "rejected":
        print(f"\nRejection Reason: {outcome.pm_decision.rejection_reason}")
    elif outcome.pm_decision and outcome.pm_decision.spec and outcome.status == "ready":
        print("\nNormalized WebsiteSpecification:")
        print(outcome.pm_decision.spec.model_dump_json(indent=2))

    if outcome.test_report:
        passed_cnt = sum(1 for c in outcome.test_report.checks if c.passed)
        total_cnt = len(outcome.test_report.checks)
        print(
            f"\nVerification: {passed_cnt}/{total_cnt} Playwright gates passed "
            f"(attempts={outcome.attempts}, semantic_qa={outcome.test_report.semantic_quality_ok})"
        )
        if outcome.test_report.tester_summary:
            print(f"  Tester:   {outcome.test_report.tester_summary}")
        for f in outcome.test_report.failures:
            print(f"  FAIL [{f.check_name}]: {f.actual}")
    if outcome.review and outcome.review.summary_markdown:
        print(f"  Reviewer: {outcome.review.summary_markdown}")


def main(argv: list[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    cfg = load_model_config_from_env()
    tracing_enabled = not is_truthy_env("OTEL_SDK_DISABLED", False)
    _, shutdown_tracing = setup_tracing(
        enabled=tracing_enabled, service_name="ai-factory-local-cli"
    )
    try:
        outcome = asyncio.run(
            run_local_pipeline(
                prompt=args.prompt,
                title=args.title,
                category=args.category,
                slug_hint=args.slug,
                output_root=args.output_dir,
                websites_repo_path=cfg.websites_repo_path,
                base_url=cfg.base_url,
                pm_key=cfg.pm_key,
                dev_key=cfg.dev_key,
                test_key=cfg.test_key,
                sec_key=cfg.sec_key,
                spec_only=args.spec_only,
                max_attempts=args.max_attempts,
                visual_qa=False if args.no_visual_qa else None,
            )
        )
    finally:
        shutdown_tracing()

    _print_outcome(outcome)

    if args.preview and outcome.status == "verified" and outcome.site_slug:
        serve_script = pathlib.Path(cfg.websites_repo_path) / "scripts" / "serve.mjs"
        if serve_script.is_file():
            env = {
                **os.environ,
                "SITE_SLUG": outcome.site_slug,
                "SITE_WORKTREE_ROOT": str(pathlib.Path(args.output_dir).resolve()),
            }
            print("\nStarting local preview server (Ctrl+C to stop)...")
            subprocess.run(
                ["node", str(serve_script), "--open"],
                env=env,
                check=False,
            )

    return 0 if outcome.status in ("ready", "verified") else 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
