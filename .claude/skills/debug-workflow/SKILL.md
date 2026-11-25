---
name: debug-workflow
description: Inspect failed Temporal IssueFactoryWorkflow runs, Arize Phoenix OpenInference traces, LiteLLM usage/logs, and archived failed snapshots.
---

# Debug a failed factory run

1. Run `task status` or `temporal workflow show --address temporal.localhost:7233 --namespace default --workflow-id factory-issue-<N>`.
2. Open Arize Phoenix at `http://phoenix.localhost:8080` (`ai-factory` project) and inspect the end-to-end trace waterfall using the `trace_id` logged by `task run` or printed in the PR summary (set `FACTORY_OTEL_CAPTURE_CONTENT=true` locally when prompt/tool payload inspection is needed).
3. Check LiteLLM usage & logs at `http://litellm.localhost:8080/ui/usage/` and `http://litellm.localhost:8080/ui/logs/` (filter by `x-litellm-tags` `issue-<N>` or `x-litellm-trace-id`).
4. Inspect Go control-plane and Python activity-worker logs from the `task run` terminal for activity failures and heartbeat details.
5. Inspect the archived site snapshot in `../ai-factory-websites/.worktrees/issue-<N>-failed-<timestamp>/` (including `spec.json` and `test-report-attempt-*.json`).
