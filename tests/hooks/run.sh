#!/usr/bin/env bash
# Unit tests for .claude/hooks/
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "$REPO_ROOT/scripts/lib/common.sh"

unset "$(git rev-parse --local-env-vars 2>/dev/null || true)" 2>/dev/null || true

failed=0
pass() { printf '%sPASS%s  %-24s %s\n' "$C_GREEN" "$C_RESET" "$1" "$2"; }
fail() { printf '%sFAIL%s  %-24s %s\n' "$C_RED" "$C_RESET" "$1" "$2"; failed=1; }

expect_hook() {
  local want="$1" hook="$2" label="$3" payload="$4"
  local rc=0
  CLAUDE_PROJECT_DIR="$REPO_ROOT" "$REPO_ROOT/.claude/hooks/$hook" <<<"$payload" >/dev/null 2>&1 || rc=$?
  if [[ "$rc" -eq "$want" ]]; then
    pass "$hook" "$label"
  else
    fail "$hook" "$label (expected exit $want, got $rc)"
  fi
}

# keychain-kube-guard.py
expect_hook 2 "keychain-kube-guard.py" "blocks security find-generic-password" '{"tool_input":{"command":"security find-generic-password -s ai-factory-github-app-pem -w"}}'
expect_hook 2 "keychain-kube-guard.py" "blocks kubectl get secret -o yaml" '{"tool_input":{"command":"kubectl get secret litellm-pm-agent-key -n ai-platform -o yaml"}}'
expect_hook 2 "keychain-kube-guard.py" "blocks ~/.kube/config access" '{"tool_input":{"command":"cat ~/.kube/config"}}'
expect_hook 0 "keychain-kube-guard.py" "allows go test" '{"tool_input":{"command":"go test ./..."}}'
expect_hook 0 "keychain-kube-guard.py" "ignores empty stdin" ''

# secret-guard.sh (assemble fake token at runtime so this test file stays gitleaks-clean)
fake_pat="ghp_$(openssl rand -hex 18)"
expect_hook 2 "secret-guard.sh" "blocks GitHub PAT" "$(jq -cn --arg c "key = \"$fake_pat\"" '{tool_input:{file_path:"x.go",content:$c}}')"
expect_hook 0 "secret-guard.sh" "allows normal Go code" '{"tool_input":{"file_path":"x.go","content":"package main\n"}}'

# agent-guard.sh
expect_hook 2 "agent-guard.sh" "blocks task run in security-auditor" '{"agent_type":"security-auditor","tool_input":{"command":"task run"}}'
expect_hook 0 "agent-guard.sh" "allows go test in workflow-reviewer" '{"agent_type":"workflow-reviewer","tool_input":{"command":"go test ./internal/workflow/..."}}'

# branch-guard.sh
expect_hook 2 "branch-guard.sh" "blocks push to main" "{\"cwd\":\"$REPO_ROOT\",\"tool_input\":{\"command\":\"git push origin main\"}}"
expect_hook 0 "branch-guard.sh" "allows git status" "{\"cwd\":\"$REPO_ROOT\",\"tool_input\":{\"command\":\"git status -sb\"}}"

exit "$failed"
