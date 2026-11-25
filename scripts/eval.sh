#!/usr/bin/env bash
# Run Promptfoo golden-dataset & red-team evaluations against the local LiteLLM gateway.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

require_cmd promptfoo curl
load_factory_runtime_env

[[ -n "${LITELLM_PM_KEY:-}" ]] || die "LITELLM_PM_KEY is missing: ensure ../ai-factory-infra is running"
[[ -n "${LITELLM_SEC_KEY:-}" ]] || die "LITELLM_SEC_KEY is missing: ensure ../ai-factory-infra is running"

litellm_base="${FACTORY_LITELLM_URL:-http://litellm.localhost:8080/v1}"
litellm_base="${litellm_base%/}"
if ! curl -fsS --noproxy '*' --max-time 5 -H "Authorization: Bearer ${LITELLM_PM_KEY}" "${litellm_base}/models" >/dev/null; then
  die "LiteLLM gateway unreachable or pm-agent key rejected at ${litellm_base}"
fi

export PROMPTFOO_DISABLE_TELEMETRY=1
export PROMPTFOO_DISABLE_UPDATE=1
export PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION=true
export PROMPTFOO_PYTHON="$REPO_ROOT/agents_py/.venv/bin/python"
export PYDANTIC_AI_NO_BANNER=1

info "running Promptfoo PM golden dataset & red-team evals against ${litellm_base}"
exec promptfoo eval \
  -c "$REPO_ROOT/evals/promptfooconfig.yaml" \
  --no-cache \
  --no-progress-bar \
  "$@"
