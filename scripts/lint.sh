#!/usr/bin/env bash
# Lint Go, Temporal workflow determinism, shell scripts, Python helpers, JSON/YAML, Conftest policies, GitHub Actions, and doc links.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

failed=0
run() {
  local name="$1"
  shift
  if "$@"; then
    printf '%sPASS%s  %s\n' "$C_GREEN" "$C_RESET" "$name"
  else
    printf '%sFAIL%s  %s\n' "$C_RED" "$C_RESET" "$name"
    failed=1
  fi
}

tracked() {
  local path
  while IFS= read -r path; do
    [[ -e "$REPO_ROOT/$path" ]] && printf '%s\n' "$path"
  done < <(git ls-files --cached --others --exclude-standard -- "$@")
}

require_cmd go golangci-lint shellcheck ruff jq yq conftest actionlint

# shellcheck disable=SC2317,SC2329
gofmt_ok() {
  local bad
  bad="$(gofmt -l .)"
  [[ -z "$bad" ]] || { echo "gofmt needed on: $bad" >&2; return 1; }
}
run "gofmt" gofmt_ok
run "go vet" go vet ./...
run "golangci-lint" golangci-lint run ./...
if go list -m all | grep -q 'go.temporal.io/sdk'; then
  run "temporal workflowcheck" go tool workflowcheck ./...
fi

shell_files="$(tracked '*.sh')"
if [[ -n "$shell_files" ]]; then
  # shellcheck disable=SC2086
  run "shellcheck ($(wc -l <<<"$shell_files" | tr -d ' ') files)" shellcheck -x $shell_files
fi

py_files="$(tracked '*.py')"
if [[ -n "$py_files" ]]; then
  # shellcheck disable=SC2086
  run "ruff check ($(wc -l <<<"$py_files" | tr -d ' ') files)" ruff check --quiet --config "$REPO_ROOT/agents_py/pyproject.toml" $py_files
  # shellcheck disable=SC2086
  run "ruff format --check" ruff format --check --quiet --config "$REPO_ROOT/agents_py/pyproject.toml" $py_files
fi

if [[ -f "$REPO_ROOT/agents_py/pyproject.toml" ]]; then
  require_cmd uv
  run "mypy (agents_py)" uv run --directory "$REPO_ROOT/agents_py" --locked mypy src tests
fi

# shellcheck disable=SC2317,SC2329
yaml_ok() { yq eval 'true' "$1" >/dev/null; }
while IFS= read -r f; do
  [[ -n "$f" ]] && run "json $f" jq empty "$f"
done <<<"$(tracked '*.json')"
while IFS= read -r f; do
  [[ -n "$f" ]] && run "yaml $f" yaml_ok "$f"
done <<<"$(tracked '*.yml' '*.yaml')"

run "conftest verify (policy unit tests)" conftest verify --no-color --policy policy
# shellcheck disable=SC2046
run "policy: workflows read-only and SHA-pinned" conftest test --no-color --policy policy --namespace github $(tracked '.github/workflows/*.yml')
run "policy: Claude Code deny rules and guard hooks" conftest test --no-color --policy policy --namespace claude .claude/settings.json
run "actionlint" actionlint -no-color

# shellcheck disable=SC2317,SC2329
public_oss_governance() {
  local f
  for f in LICENSE SECURITY.md .github/CODEOWNERS; do
    [[ -s "$REPO_ROOT/$f" ]] || {
      echo "missing or empty required public OSS governance file: $f" >&2
      return 1
    }
  done
}

run "public OSS governance (LICENSE, SECURITY.md, .github/CODEOWNERS)" public_oss_governance
run "doc links and paths" "$REPO_ROOT/scripts/lib/check_links.py"
run "task commands in AGENTS.md and docs/runbook.md" "$REPO_ROOT/scripts/lib/check_commands.py"

exit "$failed"
