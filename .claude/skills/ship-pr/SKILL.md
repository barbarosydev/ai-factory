---
name: ship-pr
description: Verify branch hygiene, task ci, and open a clean PR for ai-factory.
---

# Verify and ship a pull request

1. Run `git status -sb` and verify you are not on `main`.
2. Run `task ci` (`gofmt`, `go vet`, `golangci-lint`, `workflowcheck`, `shellcheck`, Conftest policies, `actionlint`, hook tests, unit/workflow/sandbox tests, `gitleaks`).
3. If `ai-factory-infra` is running, run `task doctor`.
4. Commit with a conventional commit message, push your branch, and open a PR with `gh pr create --base main --head <branch>`.
