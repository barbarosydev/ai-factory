"""Workspace tools and local Playwright execution for PydanticAI agents."""

from __future__ import annotations

import asyncio
import json
import pathlib
import re
import socket
import subprocess
import time
from dataclasses import dataclass, field
from typing import Any

from opentelemetry import trace

from agents_py.contracts import FailureEvidence, SingleCheck, WebsiteSpecification
from agents_py.sandbox_runner import build_playwright_command, resolve_sandbox_config
from agents_py.security import (
    SiteSandbox,
    check_site_structure,
    clean_exec_env,
    site_dir_for_worktree,
)

ANSI_ESCAPE_RE = re.compile(r"\x1b\[[0-9;]*m")


@dataclass
class WorkspaceDeps:
    """Dependency-injected workspace context passed into PydanticAI RunContext."""

    worktree_dir: str
    websites_repo_path: str
    spec: WebsiteSpecification
    changed_files: set[str] = field(default_factory=set)
    tool_trace: list[str] = field(default_factory=list)
    last_pw_result: dict[str, Any] | None = None

    def sandbox(self) -> SiteSandbox:
        return SiteSandbox(site_dir_for_worktree(self.worktree_dir, self.spec.site_slug))


def read_template_files(websites_repo_path: str) -> dict[str, str]:
    """Read sites/_template/{index.html,styles.css} as a read-only structural reference."""
    tpl_dir = pathlib.Path(websites_repo_path) / "sites" / "_template"
    out: dict[str, str] = {}
    for name in ("index.html", "styles.css"):
        p = tpl_dir / name
        if p.is_file():
            out[name] = p.read_text(encoding="utf-8")[:16384]
    return out


def _free_loopback_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def _clean_ansi(s: str) -> str:
    return ANSI_ESCAPE_RE.sub("", s).strip()


def parse_playwright_json(raw: str, elapsed_ms: int, desktop_png: str, mobile_png: str) -> dict:
    """Parse Playwright JSON reporter output into structured checks and failures."""
    start = raw.find("{")
    if start < 0:
        raise ValueError("no JSON object found in playwright output")
    report = json.loads(raw[start:])

    checks: list[SingleCheck] = []
    failures: list[FailureEvidence] = []

    def walk_suite(suite: dict) -> None:
        for spec_item in suite.get("specs") or []:
            title = spec_item.get("title", "")
            ok = bool(spec_item.get("ok", False))
            chk = SingleCheck(name=title, passed=ok)
            if not ok:
                msgs: list[str] = []
                for t in spec_item.get("tests") or []:
                    for r in t.get("results") or []:
                        if (err := r.get("error")) and (msg := err.get("message")):
                            msgs.append(_clean_ansi(msg))
                        for e in r.get("errors") or []:
                            if msg := e.get("message"):
                                msgs.append(_clean_ansi(msg))
                detail = " | ".join(msgs) or "Playwright assertion failed"
                chk.detail = detail
                failures.append(
                    FailureEvidence(
                        check_name=title,
                        expected="All Playwright verification gates pass",
                        actual=detail,
                        remediation="Fix the failing assertion in index.html or styles.css and re-verify.",
                    )
                )
            checks.append(chk)
        for child in suite.get("suites") or []:
            walk_suite(child)

    for s in report.get("suites") or []:
        walk_suite(s)

    for top_err in report.get("errors") or []:
        msg = _clean_ansi(top_err.get("message", ""))
        failures.append(
            FailureEvidence(
                check_name="playwright-suite-error",
                expected="Playwright suite loads spec.json and runs cleanly",
                actual=msg,
                remediation=msg,
            )
        )

    stats = report.get("stats") or {}
    passed = (
        stats.get("unexpected", 0) == 0
        and len(report.get("errors") or []) == 0
        and len(failures) == 0
        and len(checks) > 0
    )
    return {
        "passed": passed,
        "duration_ms": elapsed_ms,
        "checks": [c.model_dump() for c in checks],
        "failures": [f.model_dump() for f in failures],
        "desktop_screenshot_path": desktop_png,
        "mobile_screenshot_path": mobile_png,
    }


async def run_playwright_suite(deps: WorkspaceDeps) -> dict[str, Any]:
    """Write spec.json into the worktree and run Playwright with clean_exec_env()."""
    tracer = trace.get_tracer("ai-factory.workspace")
    with tracer.start_as_current_span(
        "playwright.verify_site",
        attributes={
            "site.slug": deps.spec.site_slug,
            "openinference.span.kind": "EVALUATOR",
        },
    ) as span:
        site_dir = site_dir_for_worktree(deps.worktree_dir, deps.spec.site_slug)
        site_dir.mkdir(parents=True, exist_ok=True)
        spec_path = site_dir / "spec.json"
        spec_path.write_text(deps.spec.model_dump_json(indent=2) + "\n", encoding="utf-8")

        port = _free_loopback_port()
        env = clean_exec_env(
            {
                "SITE_SLUG": deps.spec.site_slug,
                "SITE_WORKTREE_ROOT": deps.worktree_dir,
                "SITE_SERVER_PORT": str(port),
                "PW_REPORTER": "json",
                "WRITE_EVIDENCE": "1",
                "STRICT_COPY": "1",
            }
        )

        sandbox_cfg = resolve_sandbox_config()
        span.set_attribute("sandbox.mode", sandbox_cfg.mode)
        argv, run_cwd, run_env = build_playwright_command(
            cfg=sandbox_cfg,
            websites_repo_path=deps.websites_repo_path,
            worktree_dir=deps.worktree_dir,
            site_slug=deps.spec.site_slug,
            port=port,
            clean_env=env,
        )

        start = time.monotonic()
        proc = await asyncio.create_subprocess_exec(
            *argv,
            cwd=run_cwd,
            env=run_env,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout_b, stderr_b = await proc.communicate()
        elapsed_ms = int((time.monotonic() - start) * 1000)

        desktop_png = f"sites/{deps.spec.site_slug}/evidence/desktop.png"
        mobile_png = f"sites/{deps.spec.site_slug}/evidence/mobile.png"

        try:
            parsed = parse_playwright_json(
                stdout_b.decode("utf-8", errors="replace"), elapsed_ms, desktop_png, mobile_png
            )
        except Exception as exc:
            if proc.returncode != 0:
                raise RuntimeError(
                    f"playwright invocation failed ({proc.returncode}): "
                    f"{stderr_b.decode('utf-8', errors='replace').strip()}"
                ) from exc
            raise RuntimeError(f"parse playwright json: {exc}") from exc

        if proc.returncode != 0 and parsed["passed"]:
            parsed["passed"] = False
            parsed["failures"].append(
                FailureEvidence(
                    check_name="playwright-exit-code",
                    expected="exit 0",
                    actual=f"exit {proc.returncode}",
                    remediation=stderr_b.decode("utf-8", errors="replace").strip(),
                ).model_dump()
            )
        span.set_attribute("playwright.passed", bool(parsed["passed"]))
        span.set_attribute("playwright.duration_ms", elapsed_ms)
        span.set_attribute("playwright.checks_count", len(parsed["checks"]))
        span.set_attribute("playwright.failures_count", len(parsed["failures"]))
        deps.last_pw_result = parsed
        return parsed


def inspect_git_diff(worktree_dir: str, slug: str) -> dict[str, Any]:
    """Verify via git status --porcelain that only files inside sites/<slug>/ changed."""
    clean_env = {k: v for k, v in clean_exec_env().items() if not k.startswith("GIT_")}
    status_proc = subprocess.run(
        ["git", "-C", worktree_dir, "status", "--porcelain", "-uall"],
        env=clean_env,
        capture_output=True,
        text=True,
        check=True,
    )
    expected_prefix = f"sites/{slug}/"
    changed: list[str] = []
    path_ok = True
    for line in status_proc.stdout.splitlines():
        if len(line) < 4:
            continue
        file_path = line[3:].strip()
        if " -> " in file_path:
            file_path = file_path.split(" -> ", 1)[1]
        file_path = file_path.strip('"')
        changed.append(file_path)
        if not file_path.startswith(expected_prefix):
            path_ok = False
    changed.sort()

    stat_proc = subprocess.run(
        ["git", "-C", worktree_dir, "diff", "--stat"],
        env=clean_env,
        capture_output=True,
        text=True,
        check=False,
    )
    return {
        "changed_files": changed,
        "path_isolation_ok": path_ok and len(changed) > 0,
        "diff_stat": stat_proc.stdout.strip(),
    }


def create_site_structure_result_dict(deps: WorkspaceDeps) -> dict[str, Any]:
    res = check_site_structure(deps.sandbox(), deps.spec)
    return {"passed": res.passed, "errors": res.errors}
