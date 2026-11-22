# `agents_py` — PydanticAI + MCP Temporal Activity Worker

Python Temporal Activity Worker for `ai-factory` executing `RunPMAgentActivity`, `RunDeveloperAgentActivity`, `RunTesterAgentActivity`, and `RunReviewerActivity` on the `ai-factory-agents` task queue when `FACTORY_AGENT_RUNTIME=pydanticai`.

## Quickstart

```bash
uv sync --locked
uv run mypy src tests
uv run pytest
```
