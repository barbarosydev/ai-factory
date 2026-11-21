#!/usr/bin/env python3
"""PreToolUse hook for Bash: prevent printing Keychain secrets, K8s Secrets, or touching global kubeconfig."""
import json
import re
import sys


def main() -> int:
    try:
        payload = json.load(sys.stdin)
    except Exception:
        return 0

    cmd = (payload.get("tool_input") or {}).get("command") or ""
    if not cmd:
        return 0

    # Block reading/printing Keychain passwords directly
    if re.search(r"\bsecurity\s+find-(generic|internet)-password\b", cmd):
        print(
            "Blocked by .claude/hooks/keychain-kube-guard.py: do not print macOS Keychain passwords in shell commands; scripts/run.sh injects them at runtime.",
            file=sys.stderr,
        )
        return 2

    # Block printing Kubernetes Secret values
    if re.search(r"\bkubectl\b.*\bget\s+secret", cmd) and re.search(r"(-o|--output|jsonpath|go-template|yaml|json)", cmd):
        print(
            "Blocked by .claude/hooks/keychain-kube-guard.py: never print Kubernetes Secret data.",
            file=sys.stderr,
        )
        return 2

    # Block touching global ~/.kube/config or kubectx/kubens
    if re.search(r"\b(kubectx|kubens)\b", cmd) or re.search(r"~/\.kube/config|\$HOME/\.kube/config", cmd):
        print(
            "Blocked by .claude/hooks/keychain-kube-guard.py: never access global ~/.kube/config or switch contexts.",
            file=sys.stderr,
        )
        return 2

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
