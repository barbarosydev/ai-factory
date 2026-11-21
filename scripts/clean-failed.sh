#!/usr/bin/env bash
# Remove archived .worktrees/issue-*-failed-* folders and prune stale worktrees in ai-factory-websites.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

require_cmd go git
"$REPO_ROOT/scripts/build.sh" >/dev/null

exec "$REPO_ROOT/bin/factory" clean-failed "$@"
