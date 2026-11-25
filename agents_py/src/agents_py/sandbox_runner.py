"""Dual-mode SandboxRunner (host vs rootless container) for Playwright verification."""

from __future__ import annotations

import os
import pathlib
from dataclasses import dataclass

DEFAULT_SANDBOX_MODE = "host"
DEFAULT_SANDBOX_IMAGE = "mcr.microsoft.com/playwright:v1.52.0-noble"
VALID_SANDBOX_MODES = ("host", "container")


@dataclass(frozen=True)
class SandboxConfig:
    """Resolved execution mode and container hardening parameters."""

    mode: str
    image: str = DEFAULT_SANDBOX_IMAGE


def resolve_sandbox_config() -> SandboxConfig:
    """Resolve FACTORY_SANDBOX_MODE (host|container) and FACTORY_SANDBOX_IMAGE from env."""
    raw_mode = os.environ.get("FACTORY_SANDBOX_MODE", DEFAULT_SANDBOX_MODE).strip().lower()
    if raw_mode not in VALID_SANDBOX_MODES:
        raise ValueError(
            f"invalid FACTORY_SANDBOX_MODE {raw_mode!r}: must be one of {VALID_SANDBOX_MODES}"
        )
    image = os.environ.get("FACTORY_SANDBOX_IMAGE", DEFAULT_SANDBOX_IMAGE).strip()
    if raw_mode == "container" and not image:
        raise ValueError(
            "FACTORY_SANDBOX_IMAGE must not be empty when FACTORY_SANDBOX_MODE=container"
        )
    return SandboxConfig(mode=raw_mode, image=image)


def build_playwright_command(
    *,
    cfg: SandboxConfig,
    websites_repo_path: str,
    worktree_dir: str,
    site_slug: str,
    port: int,
    clean_env: dict[str, str],
) -> tuple[list[str], str | None, dict[str, str]]:
    """Return (argv, cwd, env) for either host or hardened --network=none container execution."""
    if cfg.mode == "host":
        return (
            ["npx", "playwright", "test", "--reporter=json"],
            websites_repo_path,
            clean_env,
        )

    repo_abs = str(pathlib.Path(websites_repo_path).resolve())
    site_abs = str(pathlib.Path(worktree_dir).resolve() / "sites" / site_slug)
    container_worktree = "/worktree"
    container_site = f"{container_worktree}/sites/{site_slug}"

    argv = [
        "docker",
        "run",
        "--rm",
        "--network=none",
        "--cap-drop=ALL",
        "--security-opt=no-new-privileges",
        "--pids-limit=256",
        "--read-only",
        "--tmpfs",
        "/tmp:rw,noexec,nosuid,size=256m",
        "-v",
        f"{repo_abs}:/repo:ro",
        "-v",
        f"{site_abs}:{container_site}:rw",
        "-w",
        "/repo",
        "-e",
        f"SITE_SLUG={site_slug}",
        "-e",
        f"SITE_WORKTREE_ROOT={container_worktree}",
        "-e",
        f"SITE_SERVER_PORT={port}",
        "-e",
        "PW_REPORTER=json",
        "-e",
        "WRITE_EVIDENCE=1",
        "-e",
        "STRICT_COPY=1",
        cfg.image,
        "npx",
        "playwright",
        "test",
        "--reporter=json",
    ]
    return argv, None, clean_env
