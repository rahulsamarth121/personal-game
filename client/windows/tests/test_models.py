"""Unit tests for models (no Qt): parsing, validation, manifest semantics."""

from __future__ import annotations

import pytest

from client.windows.models import (
    AddGameSpec,
    ValidationError,
    build_manifest,
    normalize_game_id,
    parse_size,
    parse_source_lines,
    url_basename,
    validate_game_id,
    validate_spec,
    validate_url,
)


def spec(**kw) -> AddGameSpec:
    base = dict(
        game_id="doom2",
        name="DOOM II",
        version="1.0",
        package_type="archive_prebuilt",
        archive_type="zip",
        sources=None,
        exe="game.exe",
    )
    base.update(kw)
    if base["sources"] is None:
        base["sources"] = parse_source_lines("https://example.invalid/game.zip")
    return AddGameSpec(**base)


# -- game id ------------------------------------------------------------------

def test_normalize_game_id_basic():
    assert normalize_game_id("DOOM II: Hell!")
    assert normalize_game_id("DOOM II: Hell!") == "doom-ii-hell"


def test_normalize_game_id_keeps_safe_chars():
    assert normalize_game_id("Half-Life_2.exe") == "half-life_2.exe"


def test_validate_game_id_accepts_backend_shape():
    validate_game_id("doom2")
    validate_game_id("My_Game.2")


@pytest.mark.parametrize("bad", ["", ".hidden", "a b", "a/b", "a\\b", "-x" , "x" * 65])
def test_validate_game_id_rejects_unsafe(bad):
    with pytest.raises(ValidationError):
        validate_game_id(bad)


# -- urls ---------------------------------------------------------------------

def test_validate_url_accepts_http_https_file():
    assert validate_url("https://example.com/a.zip")
    assert validate_url("http://example.com/a.zip")
    assert validate_url("file:///C:/games/a.zip")


@pytest.mark.parametrize("bad", ["", "ftp://x/a.zip", "not a url", "https://"])
def test_validate_url_rejects_bad(bad):
    with pytest.raises(ValidationError):
        validate_url(bad)


def test_url_basename_matches_cli_behavior():
    assert url_basename("https://example.com/game.part01.zip") == "game.part01.zip"
    assert url_basename("https://example.com/") == ""
    assert url_basename("https://example.com") == ""


# -- sources field ------------------------------------------------------------

def test_one_url_creates_one_source():
    sources = parse_source_lines("https://example.com/game.zip")
    assert len(sources) == 1
    assert sources[0].url == "https://example.com/game.zip"
    assert sources[0].effective_filename == "game.zip"


def test_twenty_urls_create_twenty_ordered_sources():
    text = "\n".join(
        f"https://example.com/game.part{i:02d}.zip" for i in range(1, 21)
    )
    sources = parse_source_lines(text)
    assert len(sources) == 20
    assert sources[0].effective_filename == "game.part00.zip" or sources[0].effective_filename.endswith("part01.zip")
    # Order preserved: filenames increase.
    names = [s.effective_filename for s in sources]
    assert names == sorted(names)


def test_blank_lines_and_comments_are_skipped_safely():
    text = "\nhttps://a.example/1.zip\n\n   \n# comment line\nhttps://a.example/2.zip\n\n"
    sources = parse_source_lines(text)
    assert [s.effective_filename for s in sources] == ["1.zip", "2.zip"]


def test_invalid_url_line_is_rejected():
    with pytest.raises(ValidationError):
        parse_source_lines("https://ok.example/1.zip\nftp://bad/2.zip")


# -- spec validation ----------------------------------------------------------

def test_prebuilt_with_installer_is_rejected():
    s = spec(package_type="archive_prebuilt", installer_path="setup.exe")
    errors = validate_spec(s)
    assert any("installer" in e.lower() for e in errors)


def test_prebuilt_with_multiple_sources_is_rejected():
    s = spec(
        package_type="archive_prebuilt",
        sources=parse_source_lines(
            "https://a.example/1.zip\nhttps://a.example/2.zip"),
    )
    errors = validate_spec(s)
    assert any("exactly one" in e for e in errors)


def test_direct_exe_does_not_create_installer_stage():
    s = spec(
        package_type="direct_prebuilt",
        sources=parse_source_lines("https://a.example/tiny-game.exe"),
        exe="tiny-game.exe",
    )
    assert validate_spec(s) == []
    m = build_manifest(s)
    pipe = m["package_pipeline"]
    assert "installer" not in pipe
    assert "archive" not in pipe          # direct EXE takes no archive stage
    assert "iso" not in pipe and "result" not in pipe
    assert pipe["prebuilt"]["expected_files"] == []  # or absent


def test_direct_exe_with_iso_is_rejected():
    s = spec(
        package_type="direct_prebuilt",
        sources=parse_source_lines("https://a.example/game.exe"),
        iso_result_path="game.iso",
        iso_extract=True,
    )
    errors = validate_spec(s)
    assert any("ISO" in e for e in errors)


def test_multipart_installer_generates_correct_semantics():
    text = "\n".join(f"https://a.example/part{i:02d}.zip" for i in range(1, 4))
    s = spec(
        package_type="archive_installer",
        sources=parse_source_lines(text),
        installer_path="setup.exe",
        installer_args=["/SILENT"],
        installer_timeout=1800,
        installer_expected_dir="DOOM",
    )
    assert validate_spec(s) == []
    m = build_manifest(s)
    acq = m["acquisition"]
    assert acq["package_type"] == "archive_installer"
    assert [src["part"] for src in acq["sources"]] == [1, 2, 3]
    pipe = m["package_pipeline"]
    assert pipe["archive"] == {"type": "zip", "multipart": True}
    assert pipe["installer"]["path"] == "setup.exe"
    assert pipe["installer"]["arguments"] == ["/SILENT"]
    assert pipe["installer"]["timeout_seconds"] == 1800
    assert pipe["installer"]["expected_dir"] == "DOOM"
    assert m["cleanup"] == {
        "delete_parts_after_archive_extract": True,
        "delete_iso_after_extract": False,
        "delete_installer_after_install": True,
    }


def test_iso_installer_requires_iso_result_path():
    s = spec(
        package_type="iso_installer",
        installer_path="setup.exe",
        iso_extract=True,
        iso_result_path="",
    )
    errors = validate_spec(s)
    assert any("ISO result path" in e for e in errors)


def test_iso_installer_manifest_has_iso_stage():
    s = spec(
        package_type="iso_installer",
        installer_path="setup.exe",
        iso_result_path="game.iso",
        iso_extract=True,
    )
    m = build_manifest(s)
    pipe = m["package_pipeline"]
    assert pipe["result"] == {"type": "iso", "path": "game.iso"}
    assert pipe["iso"] == {"extract": True}
    assert m["cleanup"]["delete_iso_after_extract"] is True


def test_single_installer_is_valid():
    s = spec(package_type="archive_installer", installer_path="setup.exe")
    assert validate_spec(s) == []
    m = build_manifest(s)
    assert m["package_pipeline"]["archive"]["multipart"] is False


def test_missing_exe_is_reported():
    s = spec(exe="")
    errors = validate_spec(s)
    assert any("Launch executable" in e for e in errors)


def test_sha256_and_size_validation():
    s = spec(sources=parse_source_lines("https://a.example/game.zip"))
    s.sources[0].sha256 = "nothex"
    errors = validate_spec(s)
    assert any("SHA-256" in e for e in errors)
    s.sources[0].sha256 = "ab" * 32
    s.sources[0].size_bytes = 123
    assert validate_spec(s) == []


def test_invalid_sha_not_included_in_manifest():
    s = spec(sources=parse_source_lines("https://a.example/game.zip"))
    s.sources[0].sha256 = "zz"
    with pytest.raises(ValidationError):
        build_manifest(s)


def test_size_parsing():
    assert parse_size("") == 0
    assert parse_size("1048576") == 1048576
    with pytest.raises(ValidationError):
        parse_size("12 MB")
    with pytest.raises(ValidationError):
        parse_size("-5")


def test_manifest_shape_matches_go_validator_expectations():
    m = build_manifest(spec())
    assert m["schema_version"] == 3
    assert m["acquisition"]["provider"] == "archive"
    assert m["runtime"]["os"] == "windows"
    assert m["launch"]["executable"] == "game.exe"
    assert m["saves"]["provider"] == "ludusavi"
    assert m["capabilities_required"] == {"gamepad": False, "hdr": False, "min_vram_mb": 0}
    assert m["acquisition"]["sources"][0]["part"] == 1


def test_save_overrides_and_ludusavi_title_roundtrip():
    s = spec(ludusavi_title="DOOM II", save_paths=["%APPDATA%/doom2"])
    m = build_manifest(s)
    assert m["saves"]["ludusavi_title"] == "DOOM II"
    assert m["saves"]["overrides"] == [{"platform": "any", "paths": ["%APPDATA%/doom2"]}]
