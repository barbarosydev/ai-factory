#!/usr/bin/env bash
# Run the ai-factory Temporal worker and FIFO GitHub Issue poller with runtime credentials in env only.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

require_cmd go git node
"$REPO_ROOT/scripts/build.sh" >/dev/null
load_factory_runtime_env

runtime="${FACTORY_AGENT_RUNTIME:-pydanticai}"
py_pid=""
cleanup_py_worker() {
  if [[ -n "$py_pid" ]] && kill -0 "$py_pid" 2>/dev/null; then
    kill "$py_pid" 2>/dev/null || true
    wait "$py_pid" 2>/dev/null || true
  fi
}

if [[ "$runtime" == "pydanticai" ]]; then
  require_cmd uv
  trap cleanup_py_worker EXIT INT TERM
  info "starting PydanticAI + MCP Temporal worker (queue=${FACTORY_AGENT_TASK_QUEUE:-ai-factory-agents})"
  uv run --project "$REPO_ROOT/agents_py" --locked python -m agents_py.worker &
  py_pid="$!"
fi

"$REPO_ROOT/bin/factory" run "$@"
