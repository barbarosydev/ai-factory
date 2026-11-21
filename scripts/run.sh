#!/usr/bin/env bash
# Run the ai-factory Temporal worker and FIFO GitHub Issue poller with runtime credentials in env only.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

require_cmd go git node
"$REPO_ROOT/scripts/build.sh" >/dev/null
load_factory_runtime_env

exec "$REPO_ROOT/bin/factory" run "$@"
