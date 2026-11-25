# 0012. Dual-mode container SandboxRunner, TargetWorkspace, and worker Dockerfile

## Status

Accepted

## Date

2026-11-26

## Context

In v1, Playwright verification ran directly on the macOS host via `clean_exec_env()`. Preparing for zero-trust execution and Kubernetes/AKS portability requires an optional rootless container sandbox (`--network=none`, `--cap-drop=ALL`, read-only repo mount), a `TargetWorkspace` interface decoupling activities from `*gitworktree.Manager`, and a production multi-stage `Dockerfile`.

## Decision

- **Dual-mode `SandboxRunner`** ([agents_py/src/agents_py/sandbox_runner.py](../../agents_py/src/agents_py/sandbox_runner.py)): Support `FACTORY_SANDBOX_MODE=host|container` (default `host` on macOS; `container` runs `docker run --rm --network=none --cap-drop=ALL --security-opt=no-new-privileges --pids-limit=256 --read-only` mounting the websites repo `:ro` and only `sites/<slug>/` `:rw`). Record `Verification Sandbox` in every PR summary.
- **`TargetWorkspace` interface** ([internal/gitworktree/manager.go](../../internal/gitworktree/manager.go)): Abstract worktree operations behind an interface on `FactoryActivities.Git`.
- **Multi-stage `Dockerfile`** ([Dockerfile](../../Dockerfile)): Compile static Go binary `factory` and bundle `agents_py` (`uv sync --locked --no-dev`) under non-root UID `10001` with `/healthz` `HEALTHCHECK`.

## Consequences

- Operators can run host Playwright on Apple Silicon or opt into `FACTORY_SANDBOX_MODE=container` without changing workflow code, and the worker packages cleanly into a Kubernetes/AKS container image.
