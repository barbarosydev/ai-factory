#!/usr/bin/env bash
# PostToolUse hook for Write/Edit: run gofmt on edited .go files.
set -euo pipefail

command -v jq >/dev/null 2>&1 || exit 0
command -v gofmt >/dev/null 2>&1 || exit 0

payload="$(cat)"
file_path="$(jq -r '.tool_input.file_path // empty' <<<"$payload")"
[[ "$file_path" == *.go && -f "$file_path" ]] || exit 0

gofmt -w "$file_path"
