"""Typed Pydantic v2 contracts mirroring internal/contracts/contracts.go."""

from __future__ import annotations

import re
from typing import Literal

from pydantic import BaseModel, Field, model_validator

SLUG_RE = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
HEX_COLOR_RE = re.compile(r"^#[0-9A-Fa-f]{6}$")


def validate_site_slug(slug: str) -> str:
    """Validate that a site slug is lowercase kebab-case and not reserved."""
    if not slug or slug == "_template":
        raise ValueError(f"invalid site_slug {slug!r}: reserved or empty")
    if not SLUG_RE.match(slug):
        raise ValueError(f"invalid site_slug {slug!r}: must match ^[a-z0-9]+(?:-[a-z0-9]+)*$")
    return slug


class IssueComment(BaseModel):
    id: int
    author: str
    body: str
    created_at: str


class CustomerIssueInput(BaseModel):
    issue_number: int
    issue_title: str
    issue_body: str
    request_type: str = "new_website"
    target_site_slug: str = ""
    existing_sites: list[str] = Field(default_factory=list)
    clarifications: list[IssueComment] = Field(default_factory=list)


class ThemeSpec(BaseModel):
    primary_color: str = "#2C1A12"
    background_color: str = "#FAF6F0"
    accent_color: str = "#C87D44"
    font_pairing: str = "Georgia, serif + system-ui, sans-serif"


class NavLinkSpec(BaseModel):
    label: str
    href: str


class SectionSpec(BaseModel):
    id: str
    heading: str
    purpose: str = ""
    required_keywords: list[str] = Field(default_factory=list)
    required_elements: list[str] = Field(default_factory=list)


class WebsiteSpecification(BaseModel):
    schema_version: str = "v1"
    site_slug: str
    operation: Literal["create", "modify"] = "create"
    category: str = "Local Business"
    page_title: str
    tagline: str = ""
    theme: ThemeSpec = Field(default_factory=ThemeSpec)
    navigation: list[NavLinkSpec] = Field(default_factory=list)
    sections: list[SectionSpec] = Field(default_factory=list)
    footer_text: str = ""

    @model_validator(mode="after")
    def validate_invariants(self) -> WebsiteSpecification:
        validate_site_slug(self.site_slug)
        if not self.page_title.strip():
            raise ValueError("page_title must not be empty")
        for color in (
            self.theme.primary_color,
            self.theme.background_color,
            self.theme.accent_color,
        ):
            if not HEX_COLOR_RE.match(color):
                raise ValueError(f"theme color {color!r} must be a 6-digit hex code")
        if not (3 <= len(self.sections) <= 8):
            raise ValueError(f"sections count {len(self.sections)} out of range [3, 8]")
        seen: set[str] = set()
        for sec in self.sections:
            if not SLUG_RE.match(sec.id):
                raise ValueError(f"invalid section id {sec.id!r}: must be lowercase kebab-case")
            if sec.id in seen:
                raise ValueError(f"duplicate section id {sec.id!r}")
            seen.add(sec.id)
            if not sec.heading.strip():
                raise ValueError(f"section {sec.id!r} heading must not be empty")
        return self


class PMDecisionOutput(BaseModel):
    status: Literal["ready", "needs_clarification", "rejected"]
    rejection_reason: str = ""
    clarification_questions: list[str] = Field(default_factory=list)
    spec: WebsiteSpecification | None = None

    @model_validator(mode="after")
    def validate_decision(self) -> PMDecisionOutput:
        if self.status == "ready" and self.spec is None:
            raise ValueError("status is ready but spec is None")
        if self.status == "needs_clarification" and not self.clarification_questions:
            raise ValueError("status is needs_clarification but clarification_questions is empty")
        if self.status == "rejected" and not self.rejection_reason.strip():
            raise ValueError("status is rejected but rejection_reason is empty")
        return self


class SingleCheck(BaseModel):
    name: str
    passed: bool
    detail: str = ""


class FailureEvidence(BaseModel):
    check_name: str
    expected: str
    actual: str
    file_hint: str = ""
    remediation: str


class DeveloperTaskInput(BaseModel):
    issue_number: int
    attempt: int
    spec: WebsiteSpecification
    prior_failures: list[FailureEvidence] = Field(default_factory=list)


class DeveloperActivityInput(BaseModel):
    worktree_dir: str
    task: DeveloperTaskInput


class ImplementationResult(BaseModel):
    attempt: int
    summary: str
    files_changed: list[str] = Field(default_factory=list)
    turns_used: int = 1
    tool_trace: list[str] = Field(default_factory=list)


class TestRunInput(BaseModel):
    __test__ = False

    issue_number: int
    attempt: int
    spec: WebsiteSpecification


class TesterActivityInput(BaseModel):
    worktree_dir: str
    input: TestRunInput


class TestReport(BaseModel):
    __test__ = False

    attempt: int
    passed: bool
    semantic_quality_ok: bool
    duration_ms: int = 0
    checks: list[SingleCheck] = Field(default_factory=list)
    failures: list[FailureEvidence] = Field(default_factory=list)
    tester_summary: str = ""
    desktop_screenshot_path: str = ""
    mobile_screenshot_path: str = ""


class ReviewInput(BaseModel):
    issue: CustomerIssueInput
    spec: WebsiteSpecification
    test_report: TestReport
    changed_files: list[str] = Field(default_factory=list)
    diff_stat: str = ""


class ReviewerActivityInput(BaseModel):
    worktree_dir: str
    input: ReviewInput


class ReviewResult(BaseModel):
    approved: bool
    path_isolation_ok: bool
    external_assets_ok: bool
    spec_fidelity_ok: bool
    summary_markdown: str = ""
    findings: list[str] = Field(default_factory=list)
