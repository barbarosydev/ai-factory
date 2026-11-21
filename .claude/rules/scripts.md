---
paths:
  - "scripts/**"
  - "tests/**"
  - ".claude/hooks/**"
---

# Script, test and hook conventions

- Bash with `set -euo pipefail`; source `scripts/lib/common.sh`.
- Safe to repeat: running a script again gives the same result.
- Exit non-zero on any real failure. An `|| true` needs a short comment saying why absence is fine.
- shellcheck must pass (`task lint`).
- Logic lives in `scripts/`; the Taskfile only calls them.
- Never print Keychain secrets or Kubernetes Secret values; inject them only into process env vars at `exec` time.
- Change a hook only together with its tests in `tests/hooks/run.sh`.
