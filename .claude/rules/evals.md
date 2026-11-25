---
paths:
  - "evals/**"
---

# Promptfoo Evaluation Conventions

- Keep `evals/promptfooconfig.yaml` pointed exclusively at local LiteLLM (`http://litellm.localhost:8080/v1`) with `maxConcurrency: 1` for the 36 GB Mac.
- Never hardcode LiteLLM keys in `evals/`; `scripts/eval.sh` loads them via `load_factory_runtime_env` and passes `LITELLM_PM_KEY` / `LITELLM_SEC_KEY` by env var name (`apiKeyEnvar`).
- Reuse `prompts/pm_system.md` and `run_pm_agent` directly via `evals/providers/pm_provider.py` so evals always test the live production prompt and structured output pipeline.
- Disable Promptfoo telemetry and remote red-team generation in `scripts/eval.sh` (`PROMPTFOO_DISABLE_TELEMETRY=1`, `PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION=true`).
