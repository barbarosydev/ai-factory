"""Tests for Pydantic v2 contracts mirroring internal/contracts/contracts.go."""

import pytest
from agents_py.contracts import (
    PMDecisionOutput,
    SectionSpec,
    WebsiteSpecification,
    validate_site_slug,
)


def _sample_spec() -> WebsiteSpecification:
    return WebsiteSpecification(
        site_slug="kaffeekultur-berlin",
        page_title="Kaffeekultur Berlin",
        sections=[
            SectionSpec(id="hero", heading="Kaffeekultur", purpose="Welcome"),
            SectionSpec(id="menu", heading="Menu", purpose="Drinks"),
            SectionSpec(id="hours", heading="Hours", purpose="Schedule"),
        ],
    )


def test_validate_site_slug_rejects_reserved_and_traversal() -> None:
    assert validate_site_slug("kaffeekultur-berlin") == "kaffeekultur-berlin"
    for bad in ("", "_template", "../etc", "Cafe_Berlin", "a/b"):
        with pytest.raises(ValueError):
            validate_site_slug(bad)


def test_pm_decision_invariants() -> None:
    ready = PMDecisionOutput(status="ready", spec=_sample_spec())
    assert ready.spec is not None

    with pytest.raises(ValueError):
        PMDecisionOutput(status="ready", spec=None)

    with pytest.raises(ValueError):
        PMDecisionOutput(status="needs_clarification", clarification_questions=[])

    with pytest.raises(ValueError):
        PMDecisionOutput(status="rejected", rejection_reason="   ")
