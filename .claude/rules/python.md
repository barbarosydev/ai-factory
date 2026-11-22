---
paths:
  - "agents_py/**"
---

# Python Agent Worker conventions

- Manage dependencies strictly with `uv` (`agents_py/pyproject.toml` + `agents_py/uv.lock`); never add unpinned runtime dependencies.
- Keep `agents_py/src/agents_py/contracts.py` field-for-field compatible with `internal/contracts/contracts.go` and `internal/activities/activities.go`.
- Use `SiteSandbox` (`agents_py/src/agents_py/security.py`) for all site file reads/writes (never raw `open()` outside `sites/<slug>/`), use `clean_exec_env()` before spawning subprocesses (`npx playwright`), and wrap GitHub inputs in `wrap_untrusted_input()`.
- `ruff check`, `ruff format --check`, and `uv run --project agents_py --locked pytest` must pass cleanly (`task ci`).
