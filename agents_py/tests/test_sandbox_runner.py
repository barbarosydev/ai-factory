"""Unit tests for dual-mode SandboxRunner (host and container isolation flags)."""

from __future__ import annotations

import pathlib

import pytest
from agents_py.sandbox_runner import (
    SandboxConfig,
    build_playwright_command,
    resolve_sandbox_config,
)


def test_resolve_sandbox_config_defaults_and_validation(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("FACTORY_SANDBOX_MODE", raising=False)
    cfg = resolve_sandbox_config()
    assert cfg.mode == "host"

    monkeypatch.setenv("FACTORY_SANDBOX_MODE", "container")
    cfg_ctr = resolve_sandbox_config()
    assert cfg_ctr.mode == "container"

    monkeypatch.setenv("FACTORY_SANDBOX_MODE", "invalid-mode")
    with pytest.raises(ValueError, match="FACTORY_SANDBOX_MODE"):
        resolve_sandbox_config()


def test_container_sandbox_command_enforces_zero_network_and_least_privilege(
    tmp_path: pathlib.Path,
) -> None:
    repo = tmp_path / "websites"
    wt = tmp_path / "worktree"
    repo.mkdir()
    (wt / "sites" / "demo-cafe").mkdir(parents=True)

    argv, cwd, env = build_playwright_command(
        cfg=SandboxConfig(mode="container", image="playwright-sandbox:v1"),
        websites_repo_path=str(repo),
        worktree_dir=str(wt),
        site_slug="demo-cafe",
        port=41234,
        clean_env={"PATH": "/usr/bin"},
    )
    assert cwd is None
    assert env == {"PATH": "/usr/bin"}
    assert argv[:4] == ["docker", "run", "--rm", "--network=none"]
    assert "--cap-drop=ALL" in argv
    assert "--security-opt=no-new-privileges" in argv
    assert "--pids-limit=256" in argv
    assert "--read-only" in argv
    assert f"{repo.resolve()}:/repo:ro" in argv
    assert f"{(wt / 'sites' / 'demo-cafe').resolve()}:/worktree/sites/demo-cafe:rw" in argv
    assert argv[-4:] == ["npx", "playwright", "test", "--reporter=json"]
