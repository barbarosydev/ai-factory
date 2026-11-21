#!/usr/bin/env bash
# Compile the ./bin/factory binary.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(dirname "$0")/lib/common.sh"
cd "$REPO_ROOT"

require_cmd go
mkdir -p "$REPO_ROOT/bin"
go build -o "$REPO_ROOT/bin/factory" ./cmd/factory
info "built $REPO_ROOT/bin/factory"
