"""Path-jailed filesystem sandbox, subprocess env scrubbing, and static security checks."""

from __future__ import annotations

import os
import pathlib
import re
from dataclasses import dataclass
from html.parser import HTMLParser

from agents_py.contracts import WebsiteSpecification, validate_site_slug

MAX_READ_BYTES = 64 * 1024
MAX_WRITE_BYTES = 512 * 1024
ALLOWED_EXTENSIONS = {".html", ".css", ".js", ".svg", ".json"}

EXTERNAL_ATTR_URL_RE = re.compile(
    r"""(?i)<(?:link|script|img|iframe|source|video|audio)\b[^>]*(?:href|src)\s*=\s*['"]?(https?://[^'"\s>]+|//[^'"\s>]+)"""
)
EXTERNAL_CSS_URL_RE = re.compile(
    r"""(?i)(?:url\(\s*['"]?|@import\s+['"]?)(https?://[^'"\)\s;]+|//[^'"\)\s;]+)"""
)


def wrap_untrusted_input(label: str, payload: str) -> str:
    """Wrap untrusted GitHub issue/comment data in explicit boundary tags."""
    safe_label = re.sub(r"[^a-zA-Z0-9_-]", "_", label)
    escaped_payload = payload.replace("<", "&lt;").replace(">", "&gt;")
    return (
        f"<untrusted_github_input source={safe_label!r}>\n"
        f"{escaped_payload}\n"
        "</untrusted_github_input>"
    )


def clean_exec_env(extra: dict[str, str] | None = None) -> dict[str, str]:
    """Strip secret environment variables before spawning child verification processes."""
    candidate = dict(os.environ)
    if extra:
        candidate.update(extra)

    cleaned: dict[str, str] = {}
    for key, val in candidate.items():
        upper = key.upper()
        if (
            upper.startswith(
                (
                    "LITELLM_",
                    "GITHUB_APP_",
                    "GITHUB_INSTALLATION_",
                    "AI_FACTORY_GIT_",
                    "OTEL_",
                    "PHOENIX_",
                )
            )
            or upper.endswith(("_KEY", "_SECRET", "_TOKEN", "_PASSWORD", "_PEM"))
            or "PRIVATE_KEY" in upper
        ):
            continue
        cleaned[key] = val
    return cleaned


def sanitize_rel_path(raw: str) -> str:
    """Validate a relative path inside a site directory, blocking traversal and dotfiles."""
    p = raw.strip().replace("\\", "/").removeprefix("./")
    if not p or p == ".":
        raise ValueError("path must not be empty")
    if p.startswith("/") or pathlib.PurePosixPath(p).is_absolute():
        raise ValueError(f"absolute paths are forbidden: {raw!r}")
    parts = [seg for seg in p.split("/") if seg not in ("", ".")]
    if not parts:
        raise ValueError("path must not be empty")
    for seg in parts:
        if seg == ".." or seg.startswith("."):
            raise ValueError(f"hidden or traversal path segment forbidden in {raw!r}")
    return "/".join(parts)


class SiteSandbox:
    """Confines Developer Agent filesystem operations strictly to sites/<slug>/."""

    def __init__(self, site_dir: str | pathlib.Path) -> None:
        requested_root = pathlib.Path(site_dir).absolute()
        if requested_root.is_symlink() or requested_root.parent.is_symlink():
            raise ValueError(f"symlinked site sandbox root is forbidden: {requested_root}")
        requested_root.mkdir(parents=True, exist_ok=True)
        self.root_real = requested_root.resolve()
        self.root_path = self.root_real

    def _resolve_safe(self, rel: str) -> pathlib.Path:
        clean = sanitize_rel_path(rel)
        target = self.root_path / clean
        cur = self.root_path
        for part in pathlib.PurePosixPath(clean).parts:
            cur = cur / part
            if cur.is_symlink():
                raise ValueError(f"symlinks are forbidden inside sandbox: {clean!r}")
        resolved = target.resolve()
        if resolved != self.root_real and self.root_real not in resolved.parents:
            raise ValueError(f"path escapes sandbox root: {clean!r}")
        return target

    def list_files(self) -> list[str]:
        files: list[str] = []
        for path in sorted(self.root_path.rglob("*")):
            if path.is_symlink() or not path.is_file():
                continue
            rel = path.relative_to(self.root_path).as_posix()
            if any(seg.startswith(".") for seg in rel.split("/")):
                continue
            files.append(rel)
        return files

    def read_file(self, rel: str) -> str:
        target = self._resolve_safe(rel)
        if not target.is_file():
            raise FileNotFoundError(f"file {rel!r} does not exist")
        data = target.read_bytes()
        if len(data) > MAX_READ_BYTES:
            raise ValueError(f"file {rel!r} exceeds {MAX_READ_BYTES} bytes")
        return data.decode("utf-8")

    def write_file(self, rel: str, content: str) -> int:
        clean = sanitize_rel_path(rel)
        ext = pathlib.PurePosixPath(clean).suffix.lower()
        if ext not in ALLOWED_EXTENSIONS:
            raise ValueError(
                f"file extension {ext!r} is not allowed (allowed: {sorted(ALLOWED_EXTENSIONS)})"
            )
        encoded = content.encode("utf-8")
        if len(encoded) > MAX_WRITE_BYTES:
            raise ValueError(f"content exceeds max size of {MAX_WRITE_BYTES} bytes")
        target = self._resolve_safe(clean)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(encoded)
        return len(encoded)


class _DOMInspector(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.has_doctype = False
        self.has_charset = False
        self.has_viewport = False
        self.has_header_or_nav = False
        self.has_main = False
        self.has_footer = False
        self.title = ""
        self.footer_text_parts: list[str] = []
        self.ids: set[str] = set()
        self.id_text: dict[str, list[str]] = {}
        self.anchors: list[str] = []
        self.asset_refs: list[tuple[str, str]] = []
        self.section_headings: list[str] = []
        self._stack: list[tuple[str, str]] = []
        self._in_title = False
        self._in_heading = False
        self._cur_heading: list[str] = []

    def handle_decl(self, decl: str) -> None:
        if decl.strip().lower().startswith("doctype html"):
            self.has_doctype = True

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        t = tag.lower()
        attr_map = {k.lower(): (v or "") for k, v in attrs}
        el_id = attr_map.get("id", "")
        if el_id:
            self.ids.add(el_id)
            self.id_text.setdefault(el_id, [])
        self._stack.append((t, el_id))

        if t in ("header", "nav"):
            self.has_header_or_nav = True
        elif t == "main":
            self.has_main = True
        elif t == "footer":
            self.has_footer = True
        elif t == "title":
            self._in_title = True
        elif t in ("h1", "h2", "h3"):
            self._in_heading = True
            self._cur_heading = []
        elif t == "meta":
            if attr_map.get("charset", "").strip().lower() == "utf-8" or (
                attr_map.get("http-equiv", "").strip().lower() == "content-type"
                and "utf-8" in attr_map.get("content", "").lower()
            ):
                self.has_charset = True
            if (
                attr_map.get("name", "").lower() == "viewport"
                and "width=device-width" in attr_map.get("content", "").lower()
            ):
                self.has_viewport = True
        elif t == "a":
            href = attr_map.get("href", "")
            if href:
                self.anchors.append(href)
        elif t == "link":
            if href := attr_map.get("href", ""):
                self.asset_refs.append(("link href", href))
        elif t in ("script", "img", "iframe", "source", "video", "audio") and (
            src := attr_map.get("src", "")
        ):
            self.asset_refs.append((f"{t} src", src))

    def handle_endtag(self, tag: str) -> None:
        t = tag.lower()
        if t == "title":
            self._in_title = False
        elif t in ("h1", "h2", "h3") and self._in_heading:
            self._in_heading = False
            heading = "".join(self._cur_heading).strip()
            if heading:
                self.section_headings.append(heading)
        for i in range(len(self._stack) - 1, -1, -1):
            if self._stack[i][0] == t:
                del self._stack[i:]
                break

    def handle_data(self, data: str) -> None:
        if self._in_title:
            self.title += data
        if self._in_heading:
            self._cur_heading.append(data)
        for tag, el_id in self._stack:
            if tag == "footer":
                self.footer_text_parts.append(data)
            if el_id:
                self.id_text.setdefault(el_id, []).append(data)


@dataclass
class StructureCheckResult:
    passed: bool
    errors: list[str]


def check_site_structure(sandbox: SiteSandbox, spec: WebsiteSpecification) -> StructureCheckResult:
    """Validate HTML5 landmarks, section IDs, keywords, local assets, and anti-prompt-leak."""
    errors: list[str] = []
    try:
        raw_html = sandbox.read_file("index.html")
    except FileNotFoundError:
        return StructureCheckResult(
            passed=False,
            errors=["index.html is missing; call write_site_bundle or write_file first"],
        )

    try:
        css = sandbox.read_file("styles.css")
    except FileNotFoundError:
        css = ""
    if css:
        if EXTERNAL_CSS_URL_RE.search(css):
            errors.append(
                "styles.css contains an external http(s):// url(...); remove external URLs"
            )
        if len(css.strip()) < 200 or css.count("{") < 3:
            errors.append(
                "styles.css is missing or too minimal (<200 bytes or <3 CSS rules); "
                "preserve existing CSS or write full responsive rules"
            )
        css_lower = css.lower()
        for label, color in (
            ("primary_color", spec.theme.primary_color),
            ("background_color", spec.theme.background_color),
        ):
            if color and color.lower() not in css_lower:
                errors.append(
                    f"styles.css must apply spec.theme.{label} ({color}) in CSS variables or rules"
                )
    if EXTERNAL_CSS_URL_RE.search(raw_html):
        errors.append(
            "index.html contains an external http(s):// CSS url(...); remove external URLs"
        )

    parser = _DOMInspector()
    parser.feed(raw_html)

    if not parser.has_doctype:
        errors.append("index.html is missing <!DOCTYPE html>")
    if not parser.has_charset:
        errors.append('index.html is missing <meta charset="UTF-8">')
    if not parser.has_viewport:
        errors.append(
            'index.html is missing <meta name="viewport" content="width=device-width, initial-scale=1.0">'
        )
    if not parser.has_header_or_nav:
        errors.append("index.html must include a <header> or <nav> landmark")
    if not parser.has_main:
        errors.append("index.html must include a <main> landmark")
    if not parser.has_footer:
        errors.append("index.html must include a <footer> landmark")
    elif spec.footer_text.strip():
        actual_footer = " ".join(" ".join(parser.footer_text_parts).lower().split())
        expected_footer = " ".join(spec.footer_text.lower().split())
        if expected_footer not in actual_footer:
            errors.append(f"<footer> must include exact spec.footer_text {spec.footer_text!r}")

    for kind, ref in parser.asset_refs:
        lower = ref.strip().lower()
        if lower.startswith(("http://", "https://", "//")):
            errors.append(
                f"{kind}={ref!r} references an external URL; use local files or inline SVG only"
            )
        elif not lower.startswith("data:"):
            try:
                clean = sanitize_rel_path(ref)
                sandbox.read_file(clean)
            except (OSError, ValueError) as exc:
                errors.append(f"{kind}={ref!r} missing or invalid: {exc}")

    for href in parser.anchors:
        if href.startswith("#") and len(href) > 1:
            target = href[1:]
            if target not in parser.ids:
                errors.append(
                    f"broken anchor link href={href!r}: no element with id={target!r} exists"
                )

    for nav in spec.navigation:
        if nav.href and nav.href not in parser.anchors:
            errors.append(
                f"navigation link {nav.label!r} ({nav.href!r}) is missing from index.html"
            )

    for sec in spec.sections:
        if sec.id not in parser.ids:
            errors.append(f"missing element with id={sec.id!r} for section {sec.heading!r}")
            continue
        sec_text = " ".join(" ".join(parser.id_text.get(sec.id, [])).lower().split())
        for kw in sec.required_keywords:
            if kw and kw.lower() not in sec_text:
                errors.append(f"section #{sec.id} is missing required_keyword {kw!r}")
        purpose_norm = " ".join(sec.purpose.lower().split())
        if len(purpose_norm) >= 25 and purpose_norm in sec_text:
            errors.append(
                f"section #{sec.id} leaked spec.purpose instruction verbatim; "
                "write authentic user-facing domain copy instead"
            )

    return StructureCheckResult(passed=len(errors) == 0, errors=errors)


def inspect_dom_snapshot(sandbox: SiteSandbox) -> str:
    """Return a concise summary of HTML landmarks, headings, anchors, and text preview."""
    raw_html = sandbox.read_file("index.html")
    parser = _DOMInspector()
    parser.feed(raw_html)
    landmarks = [
        name
        for name, ok in (
            ("header/nav", parser.has_header_or_nav),
            ("main", parser.has_main),
            ("footer", parser.has_footer),
        )
        if ok
    ]
    return (
        f"title={parser.title.strip()!r}; landmarks={landmarks}; "
        f"ids={sorted(parser.ids)}; headings={parser.section_headings}; "
        f"anchors={parser.anchors}"
    )


def scan_external_urls(sandbox: SiteSandbox) -> list[str]:
    """Scan HTML/CSS/JS/SVG files inside sandbox for disallowed external URLs."""
    found: list[str] = []
    for rel in sandbox.list_files():
        if pathlib.PurePosixPath(rel).suffix.lower() not in {".html", ".css", ".js", ".svg"}:
            continue
        content = sandbox.read_file(rel)
        for match in EXTERNAL_ATTR_URL_RE.findall(content):
            found.append(f"{rel}: {match}")
        for match in EXTERNAL_CSS_URL_RE.findall(content):
            found.append(f"{rel}: {match}")
    return found


def site_dir_for_worktree(worktree_dir: str, slug: str) -> pathlib.Path:
    validate_site_slug(slug)
    worktree = pathlib.Path(worktree_dir).resolve()
    sites_dir = worktree / "sites"
    site_dir = sites_dir / slug
    if sites_dir.is_symlink() or site_dir.is_symlink():
        raise ValueError(f"symlinked site path is forbidden: {site_dir}")
    return site_dir
