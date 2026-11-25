# 0011. Systematic offline AI evaluation and red-teaming with Promptfoo

## Status

Accepted

## Date

2026-11-25

## Context

Unit tests with `FunctionModel` verify Python control flow and contract normalization, but do not measure live prompt quality, `gemma4` JSON compliance, UTF-8 preservation (`ß`/`ö`/`ü`), or resistance to prompt injection against `prompts/pm_system.md` before changes ship.

## Decision

- Pin `npm:promptfoo` (`0.126.0`) in [mise.toml](../../mise.toml) and lock it in `mise.lock`.
- Add a 12-case golden dataset ([evals/datasets/golden_issues.yaml](../../evals/datasets/golden_issues.yaml)) covering `create`, `modify` (with UTF-8 preservation), `needs_clarification`, out-of-scope `rejected`, and adversarial prompt-injection / XSS / secret-exfiltration red-team cases.
- Reuse the exact production `prompts/pm_system.md` prompt and `run_pm_agent` PydanticAI pipeline via [evals/providers/pm_provider.py](../../evals/providers/pm_provider.py), evaluated with deterministic contract + safety checks ([evals/assertions/pm_contract.py](../../evals/assertions/pm_contract.py)) and a structured PydanticAI LLM-as-a-judge rubric ([evals/assertions/llm_judge.py](../../evals/assertions/llm_judge.py) using `sec-agent`) against LiteLLM (`http://litellm.localhost:8080/v1`) at `maxConcurrency: 1`.
- Expose the suite via `task eval` ([scripts/eval.sh](../../scripts/eval.sh)) with Promptfoo telemetry and remote red-team generation disabled (`PROMPTFOO_DISABLE_TELEMETRY=1`, `PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION=true`).

## Consequences

- Operators can regression-test prompt or model changes locally on the 36 GB Mac before running live GitHub issue workflows.
- `task ci` remains 100% offline-capable while `task eval` runs on demand against the local LiteLLM + Ollama stack.
