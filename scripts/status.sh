#!/usr/bin/env bash
# Show Temporal IssueFactoryWorkflow executions, loaded Ollama models, and open factory:* GitHub issues.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

require_cmd go
"$REPO_ROOT/scripts/build.sh" >/dev/null
load_factory_runtime_env

exec "$REPO_ROOT/bin/factory" status "$@"
