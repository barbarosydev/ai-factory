"""Tests for SiteSandbox, clean_exec_env, and anti-prompt-leak validation."""

import pathlib

import pytest
from agents_py.contracts import SectionSpec, WebsiteSpecification
from agents_py.security import (
    SiteSandbox,
    check_site_structure,
    clean_exec_env,
    scan_external_urls,
    wrap_untrusted_input,
)


def _sample_spec() -> WebsiteSpecification:
    return WebsiteSpecification(
        site_slug="kaffeekultur-berlin",
        page_title="Kaffeekultur Berlin",
        sections=[
            SectionSpec(
                id="hero",
                heading="Kaffeekultur Berlin",
                purpose="Welcome visitors with the roastery tagline and primary call to action.",
                required_keywords=["Kreuzberg"],
            ),
            SectionSpec(id="menu", heading="Menu", purpose="Drinks"),
            SectionSpec(id="hours", heading="Hours", purpose="Schedule"),
        ],
    )


def test_site_sandbox_blocks_escapes_symlinks_and_bad_extensions(tmp_path: pathlib.Path) -> None:
    secret = tmp_path / "secret.txt"
    secret.write_text("top-secret", encoding="utf-8")

    site_dir = tmp_path / "sites" / "kaffeekultur-berlin"
    sb = SiteSandbox(site_dir)
    (site_dir / "escape.html").symlink_to(secret)

    for bad in (
        "../../secret.txt",
        "../secret.txt",
        "/etc/passwd",
        ".git/config",
        ".env",
        "escape.html",
    ):
        with pytest.raises((ValueError, OSError)):
            sb.read_file(bad)
        with pytest.raises((ValueError, OSError)):
            sb.write_file(bad, "pwned")

    for bad_ext in ("run.sh", "payload.py", "bin.exe"):
        with pytest.raises(ValueError):
            sb.write_file(bad_ext, "echo hi")

    sb.write_file("index.html", "<h1>Valid</h1>")
    assert sb.read_file("index.html") == "<h1>Valid</h1>"


def test_clean_exec_env_strips_secrets(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("LITELLM_PM_KEY", "sk-pm-secret")
    monkeypatch.setenv("GITHUB_APP_PRIVATE_KEY_BASE64", "c2VjcmV0")
    monkeypatch.setenv("CUSTOM_API_TOKEN", "tok-secret")

    env = clean_exec_env({"SITE_SLUG": "kaffeekultur-berlin", "STRICT_COPY": "1"})
    assert "LITELLM_PM_KEY" not in env
    assert "GITHUB_APP_PRIVATE_KEY_BASE64" not in env
    assert "CUSTOM_API_TOKEN" not in env
    assert env["SITE_SLUG"] == "kaffeekultur-berlin"
    assert env["STRICT_COPY"] == "1"


def test_check_site_structure_rejects_leaked_purpose_and_external_urls(
    tmp_path: pathlib.Path,
) -> None:
    sb = SiteSandbox(tmp_path / "site")
    sb.write_file(
        "index.html",
        """<!DOCTYPE html>
<html>
<head><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Cafe</title></head>
<body>
  <header><nav><a href="#menu">Menu</a></nav></header>
  <main>
    <section id="hero"><h1>Kaffeekultur Berlin</h1><p>Kreuzberg</p>
      <p>Welcome visitors with the roastery tagline and primary call to action.</p>
    </section>
    <section id="menu"><h2>Menu</h2></section>
    <section id="hours"><h2>Hours</h2></section>
  </main>
  <footer>Cafe</footer>
</body>
</html>""",
    )
    res = check_site_structure(sb, _sample_spec())
    assert not res.passed
    assert any("leaked spec.purpose" in err for err in res.errors)

    sb.write_file("styles.css", 'body { background: url("https://cdn.example/bg.png"); }')
    assert len(scan_external_urls(sb)) == 1


def test_wrap_untrusted_input() -> None:
    wrapped = wrap_untrusted_input("issue", "hello")
    assert "<untrusted_github_input source='issue'>" in wrapped
    assert "</untrusted_github_input>" in wrapped


def test_check_site_structure_enforces_charset_and_utf8_footer(tmp_path: pathlib.Path) -> None:
    spec = WebsiteSpecification(
        site_slug="kaffeekultur-berlin",
        page_title="Kaffeekultur Berlin",
        footer_text="© 2026 Kaffeekultur Berlin · Oranienstraße 42, 10999 Berlin-Kreuzberg",
        sections=[
            SectionSpec(
                id="hero", heading="Hero", purpose="Intro", required_keywords=["Kreuzberg"]
            ),
            SectionSpec(id="menu", heading="Menu", purpose="Drinks"),
            SectionSpec(id="hours", heading="Hours", purpose="Times"),
        ],
    )
    sb = SiteSandbox(tmp_path / "site")
    sb.write_file(
        "index.html",
        """<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Kaffeekultur</title></head>
<body>
  <header><nav><a href="#hero">Hero</a></nav></header>
  <main>
    <section id="hero"><h1>Kaffeekultur</h1><p>Kreuzberg</p></section>
    <section id="menu"><h2>Menu</h2></section>
    <section id="hours"><h2>Hours</h2></section>
  </main>
  <footer>© 2026 Kaffeekultur Berlin · Oranienstrañe 42, 10999 Berlin-Kreuzberg</footer>
</body>
</html>""",
    )
    res = check_site_structure(sb, spec)
    assert not res.passed
    assert any("spec.footer_text" in err for err in res.errors)

    sb.write_file(
        "index.html",
        sb.read_file("index.html").replace("Oranienstrañe", "Oranienstraße"),
    )
    res_ok = check_site_structure(sb, spec)
    assert res_ok.passed, res_ok.errors
