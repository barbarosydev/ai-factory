#!/usr/bin/env bash
# Run hook unit tests and Go test suites.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

info "hook tests"
"$REPO_ROOT/tests/hooks/run.sh"

require_cmd go
info "go tests"
go test -race ./... "$@"

if [[ -f "$REPO_ROOT/agents_py/pyproject.toml" ]]; then
  require_cmd uv
  info "python agent tests (pytest)"
  uv run --directory "$REPO_ROOT/agents_py" --locked pytest -q
fi
