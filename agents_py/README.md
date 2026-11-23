# `agents_py` — PydanticAI Temporal Activity Worker

Python Temporal Activity Worker for `ai-factory`, executing PM, Developer, Tester, and Reviewer activities on the `ai-factory-agents` task queue. Each role uses direct PydanticAI tools; no MCP server is part of the runtime.

## Quickstart

```bash
uv sync --locked
uv run mypy src tests
uv run pytest
```
