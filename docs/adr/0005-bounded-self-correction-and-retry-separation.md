# 0005. Bounded self-correction and retry separation

## Status

Accepted

## Date

2026-11-21

## Context

Transient infrastructure failures (HTTP timeouts, GitHub API rate limits) require different handling from semantic implementation defects (missing section ID, mobile viewport overflow).

## Decision

- Configure Temporal `RetryPolicy` on activities for infrastructure/network retries.
- Implement semantic self-correction explicitly inside `IssueFactoryWorkflow` as a bounded loop (max 3 implementation attempts) driven by `TestReport.Failures` and `ReviewResult.Findings`.
- When all 3 attempts fail, snapshot the generated `sites/<slug>/` + reports into `.worktrees/issue-<N>-failed-<timestamp>/`, remove the git worktree, and label the issue `factory:failed` with diagnostic evidence.

## Consequences

- Infinite agent loops are impossible; transient blips never burn semantic repair attempts.
