#!/usr/bin/env bash
# Check Go toolchain, ai-factory-infra connectivity, GitHub App Keychain setup, and ai-factory-websites readiness.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

require_cmd go git node
"$REPO_ROOT/scripts/build.sh" >/dev/null
load_factory_runtime_env

exec "$REPO_ROOT/bin/factory" doctor "$@"
