# Observability, Configurability & Agent Performance

`ai-factory` combines Go runtime telemetry, LiteLLM proxy logging, Temporal event histories, and deterministic Playwright evaluation so operators can measure both **platform reliability** and **agent quality/speed**.

## 1. Runtime Probes & Prometheus `/metrics`

When `ai-factory run` or `ai-factory once` starts, a loopback HTTP server runs on `FACTORY_METRICS_ADDR` (default `127.0.0.1:9091`; set `off` to disable):

| Endpoint | Purpose | Kubernetes / AKS Use |
| --- | --- | --- |
| `GET /healthz` | Process liveness (`200 OK`) | `livenessProbe` |
| `GET /readyz` | Temporal worker readiness (`200` when polling, `503` otherwise) | `readinessProbe` |
| `GET /metrics` | OpenMetrics/Prometheus text metrics | `ServiceMonitor` / Azure Managed Prometheus scrape |

### Exposed Metrics

- `ai_factory_ready` — Worker readiness gauge (`1` or `0`)
- `ai_factory_workflows_total{final_state="..."}` — Completed workflows (`review`, `done`, `rejected`, `failed`)
- `ai_factory_workflow_duration_seconds_sum` / `_count` — End-to-end issue duration
- `ai_factory_repair_attempts_total` — Total build/verify attempts consumed
- `ai_factory_agent_calls_total{role,model,status}` — LLM gateway call count per role (`pm`, `dev`, `tester`, `reviewer`)
- `ai_factory_agent_tokens_total{role,model,kind="prompt|completion"}` — Prompt and completion tokens consumed per agent
- `ai_factory_agent_latency_seconds_sum` / `_count{role}` — Inference latency breakdown per agent role
- `ai_factory_playwright_runs_total{passed}` & `ai_factory_playwright_duration_seconds_*` — Ground-truth verification pass/fail rate and runtime

Enable structured JSON logs (`log/slog`) for Kubernetes log shippers by setting `FACTORY_LOG_FORMAT=json`.

---

## 2. Measuring Agent Performance (`evals` & `LiteLLM_SpendLogs`)

### Four Core KPIs
1. **First-Pass Playwright Rate**: Fraction of builds passing all 8 gates on `attempt = 1` vs requiring bounded repair (`attempt = 2..3`).
2. **Tokens per Shipped Site**: `ai_factory_agent_tokens_total` grouped by `role` (`pm`, `dev`, `tester`, `reviewer`) to spot thinking-token loops early.
3. **Per-Stage Latency**: PM planning (`gemma4:12b`), Dev scaffolding (`gemma4:e4b`), Playwright verification, Reviewer audit, and CI polling.
4. **Routing Accuracy**: Proportion of clean rejections (`factory:rejected`) vs genuine clarifications (`factory:waiting`) vs exhausted failures (`factory:failed`).

### Querying LiteLLM Postgres Spend Logs (`../ai-factory-infra`)
Every LLM call sends `X-Litellm-Tag: issue-<N>,role-<role>`. Query per-agent token consumption and durations from the `litellm` database inside `ai-factory-infra`:

```sql
SELECT
  model,
  COUNT(*) AS calls,
  ROUND(AVG(EXTRACT(EPOCH FROM ("endTime" - "startTime")))::numeric, 2) AS avg_sec,
  SUM(prompt_tokens) AS prompt_toks,
  SUM(completion_tokens) AS completion_toks
FROM "LiteLLM_SpendLogs"
GROUP BY model
ORDER BY calls DESC;
```

You can also filter calls interactively in the LiteLLM UI (`http://litellm.localhost:8080/ui`) and inspect durable workflow histories in the Temporal Web UI (`http://temporal.localhost:8080`).

---

## 3. Configurable Policy & Runtime Environment Variables

All repair caps, wait timeouts, activity timeouts, heartbeat intervals, and worker concurrency are externally configurable via `FACTORY_*` environment variables ([internal/config/config.go](../internal/config/config.go)):

| Variable | Default | Meaning |
| --- | --- | --- |
| `FACTORY_MAX_REPAIR_ATTEMPTS` | `3` | Maximum Dev -> Test -> Review iterations per issue |
| `FACTORY_MAX_CLARIFICATION_ROUNDS` | `2` | Maximum human clarification pauses before failing |
| `FACTORY_HUMAN_WAIT_TIMEOUT` | `168h` | Durable Temporal timer before timing out `factory:waiting` |
| `FACTORY_HEARTBEAT_INTERVAL` | `15s` | Background heartbeat ticker interval inside agent activities |
| `FACTORY_HEARTBEAT_TIMEOUT` | `15m` | Temporal worker heartbeat timeout per activity |
| `FACTORY_PM_TIMEOUT` / `FACTORY_DEV_TIMEOUT` | `45m` / `60m` | Activity `StartToCloseTimeout` for PM and Developer agents |
| `FACTORY_TEST_TIMEOUT` / `FACTORY_REVIEW_TIMEOUT` / `FACTORY_MERGE_TIMEOUT` | `30m` | Activity `StartToCloseTimeout` for Tester, Reviewer, and CI merge wait |
| `FACTORY_AGENT_MAX_RETRIES` / `FACTORY_GITHUB_MAX_RETRIES` | `2` / `4` | Temporal `RetryPolicy.MaximumAttempts` for infra/network faults |
| `FACTORY_MAX_CONCURRENT_ACTIVITIES` | `1` | Worker activity concurrency (`1` for 36 GB Mac; raise on multi-GPU / AKS) |
| `FACTORY_METRICS_ADDR` | `127.0.0.1:9091` | HTTP bind address for `/healthz`, `/readyz`, `/metrics` (`off` to disable) |
| `FACTORY_LOG_FORMAT` | `text` | Structured `log/slog` format (`text` or `json`) |
| `FACTORY_ALLOWED_GITHUB_USERS` | `barbarosydev` | Comma-separated GitHub usernames permitted to trigger issues or send clarification replies |
| `FACTORY_AGENT_RUNTIME` | `pydanticai` | Agent activity worker runtime (`pydanticai` Python worker or `go` in-process baseline) |
| `FACTORY_AGENT_TASK_QUEUE` | `ai-factory-agents` | Temporal task queue for PydanticAI agent activities when `FACTORY_AGENT_RUNTIME=pydanticai` |
