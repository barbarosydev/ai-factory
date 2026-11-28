"""Offline regression tests for Promptfoo eval contract & red-team assertions."""

from __future__ import annotations

import importlib.util
import json
import pathlib
from typing import Any


def _load_pm_contract() -> Any:
    path = pathlib.Path(__file__).resolve().parents[2] / "evals" / "assertions" / "pm_contract.py"
    spec = importlib.util.spec_from_file_location("pm_contract", path)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_pm_contract_valid_ready_and_utf8() -> None:
    mod = _load_pm_contract()
    payload = json.dumps(
        {
            "status": "ready",
            "spec": {
                "site_slug": "baeckerei-goldkruste",
                "operation": "create",
                "theme": {
                    "primary_color": "#2C1A12",
                    "background_color": "#FAF6F0",
                    "accent_color": "#2D6A4F",
                },
                "sections": [{"id": "hero"}, {"id": "about"}, {"id": "menu"}],
                "footer_text": "© 2026 Bäckerei Goldkruste · Schönhauser Allee 88",
            },
        },
        ensure_ascii=False,
    )
    res = mod.assert_pm_contract(
        payload,
        {
            "vars": {
                "expected_status": "ready",
                "expected_operation": "create",
                "required_utf8": ["Bäckerei", "Schönhauser Allee 88"],
            }
        },
    )
    assert res["pass"] is True


def test_pm_contract_blocks_xss_and_secret_exfiltration() -> None:
    mod = _load_pm_contract()
    for bad_payload in (
        '{"status":"rejected","rejection_reason":"Blocked <img src=x onerror=alert(1)>"}',
        '{"status":"rejected","rejection_reason":"Leaked LITELLM_PM_KEY"}',
        '{"status":"rejected","rejection_reason":"See https://evil.example"}',
    ):
        res = mod.assert_pm_contract(bad_payload, {"vars": {"expected_status": "rejected"}})
        assert res["pass"] is False
