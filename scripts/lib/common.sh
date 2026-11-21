# shellcheck shell=bash
# Shared helpers for scripts/. Source it; do not run it.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export REPO_ROOT

if command -v mise >/dev/null 2>&1 && [[ -f "$REPO_ROOT/mise.toml" ]]; then
  eval "$(mise env -s bash -C "$REPO_ROOT" 2>/dev/null || true)" # mise env absent: keep PATH
fi

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  C_GREEN=$'\033[32m' C_YELLOW=$'\033[33m' C_RED=$'\033[31m' C_RESET=$'\033[0m'
else
  C_GREEN="" C_YELLOW="" C_RED="" C_RESET=""
fi
export C_GREEN C_YELLOW C_RED C_RESET

info() { printf '==> %s\n' "$*" >&2; }
warn() { printf '%sWARN%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
die() { printf '%sFAIL%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }

require_cmd() {
  local cmd
  for cmd in "$@"; do
    command -v "$cmd" >/dev/null 2>&1 || die "$cmd is not installed: run brew bundle --no-upgrade && mise install"
  done
}

# Inject runtime credentials solely into the child process environment without printing values.
load_factory_runtime_env() {
  local env_file="${AI_FACTORY_ENV_FILE:-$HOME/.config/ai-factory/env}"
  if [[ -f "$env_file" ]]; then
    # shellcheck source=/dev/null
    source "$env_file"
  fi

  local infra_repo="${FACTORY_INFRA_REPO:-$REPO_ROOT/../ai-factory-infra}"
  local infra_kube="${infra_repo}/.kube/config"
  if [[ -f "$infra_kube" ]] && command -v kubectl >/dev/null 2>&1; then
    local pair var_name secret_name raw_val
    for pair in \
      "LITELLM_PM_KEY:litellm-pm-agent-key" \
      "LITELLM_DEV_KEY:litellm-dev-agent-key" \
      "LITELLM_TEST_KEY:litellm-test-agent-key" \
      "LITELLM_SEC_KEY:litellm-sec-agent-key"; do
      var_name="${pair%%:*}"
      secret_name="${pair#*:}"
      if [[ -z "${!var_name:-}" ]]; then
        raw_val="$(kubectl --kubeconfig "$infra_kube" -n ai-platform get secret "$secret_name" \
          -o jsonpath='{.data.api_key}' 2>/dev/null | base64 -d || true)" # absent if cluster down: validated in Go
        if [[ -n "$raw_val" ]]; then
          export "${var_name}=${raw_val}"
        fi
      fi
    done
  fi

  if [[ -z "${GITHUB_APP_PRIVATE_KEY_BASE64:-}" ]] && command -v security >/dev/null 2>&1; then
    local pem_b64
    pem_b64="$(security find-generic-password -a "$USER" -s "ai-factory-github-app-pem" -w 2>/dev/null || true)" # optional until GitHub App is configured
    if [[ -n "$pem_b64" ]]; then
      export GITHUB_APP_PRIVATE_KEY_BASE64="$pem_b64"
    fi
  fi
}
