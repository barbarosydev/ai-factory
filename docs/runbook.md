# Operations Runbook

## Commands

| Command | Use |
| --- | --- |
| `task build` | Build `./bin/factory` |
| `task doctor` | Verify local prerequisites, `ai-factory-infra`, Keychain PEM, and `ai-factory-websites` |
| `task run` | Start worker + poller in `review` mode |
| `task run:auto` | Start worker + poller in `auto` mode (checks + squash merge) |
| `task run:once` | Process a single issue and exit (`task run:once -- --issue 42`) |
| `task status` | Show Temporal workflow state, loaded Ollama models, and open `factory:*` issues |
| `task clean:failed` | Remove archived `.worktrees/issue-*-failed-*` folders |
| `task lint` | `gofmt`, `go vet`, `golangci-lint`, `workflowcheck`, `shellcheck`, `ruff`, policies, `actionlint`, doc checks |
| `task test` | Go unit, Temporal workflow replay, sandbox escape, and worktree tests |
| `task ci` | Run `task lint`, `task test`, and `gitleaks` |

## Daily Startup

1. Ensure the platform infrastructure is up in `../ai-factory-infra`.
2. Verify factory prerequisites, connectivity, Keychain PEM, and Playwright setup:

   ```bash
   task build
   task doctor
   ```

3. Start the factory worker + FIFO GitHub poller:
   - Human-review mode (`task run`)
   - Autonomous mode (`task run:auto`)
   - Single-issue mode (`task run:once -- --issue 42`)

## Monitoring

Run `task status` to view active Temporal `IssueFactoryWorkflow` executions (`Stage`, `Attempt`, `SiteSlug`), currently loaded Ollama models, and open `factory:*` issues in `barbarosydev/ai-factory-websites`.

Temporal Web UI is also available locally at `http://temporal.localhost:8080`.

## Human-in-the-Loop Clarification (`factory:waiting`)

When the PM Agent detects missing facts or contradictory requirements:

1. The factory swaps `factory:running` -> `factory:waiting`, posts numbered questions tagged with `<!-- factory:clarification-request:v1 -->`, and sleeps durably in Temporal (0 RAM / 0 Ollama usage).
2. Reply directly on the GitHub issue with your answers (or edit the issue body and re-add `factory:ready`).
3. On the next poll tick (~15s), the poller forwards `HumanReplySignal` (`human-reply`) to `factory-issue-<N>` and resumes execution seamlessly.

## Debugging Failed Builds (`factory:failed`)

- Every failed run archives `sites/<slug>/` and per-attempt `test-report-attempt-<k>.json` files into `../ai-factory-websites/.worktrees/issue-<N>-failed-<timestamp>/` (capped at 5 snapshots), removes the active worktree, and deletes the local failed branch.
- Inspect those files to see why all 3 repair attempts failed.
- Clean up old snapshots anytime with `task clean:failed`.
- To retry a failed issue after fixing the GitHub issue body, swap `factory:failed` -> `factory:ready` on GitHub.

## GitHub Actions CI Note (`auto` mode)

When running `task run:auto -- --skip-remote-ci`, the factory skips polling GitHub Checks API and squash-merges immediately after local Playwright + Reviewer approval. Use `--skip-remote-ci` only when remote GitHub Actions runners are unavailable or intentionally bypassed.
