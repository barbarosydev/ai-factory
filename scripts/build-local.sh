#!/usr/bin/env bash
# Run the full 4-agent PydanticAI website pipeline locally with zero GitHub App setup.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

require_cmd uv node
load_factory_runtime_env

exec env -u GITHUB_APP_PRIVATE_KEY_BASE64 \
  uv run --project "$REPO_ROOT/agents_py" --locked python -m agents_py.local_cli "$@"
