---
name: debug-workflow
description: Inspect failed Temporal IssueFactoryWorkflow runs, LiteLLM logs, and archived failed snapshots.
---

# Debug a failed factory run

1. Run `task status` or `temporal workflow show --address temporal.localhost:7233 --namespace default --workflow-id factory-issue-<N>`.
2. Inspect Go control-plane and Python activity-worker logs from the `task run` terminal for activity failures and heartbeat details.
3. Check LiteLLM logs at `http://litellm.localhost:8080/ui` (filter by tag `issue-<N>`).
4. Inspect the archived site snapshot in `../ai-factory-websites/.worktrees/issue-<N>-failed-<timestamp>/` (including `spec.json` and `test-report-attempt-*.json`).
