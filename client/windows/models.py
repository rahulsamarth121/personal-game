"""Package/acquisition models shared by the Add Game wizard and its tests.

These mirror the Go manifest semantics (``pkg/protocol/protocol.go``, v3):
the wizard may present simplified choices but the produced manifest must be
exactly what ``cmd/play add-game`` would produce for the same input.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from urllib.parse import urlparse

PACKAGE_TYPES = (
    "provider",            # provider-managed (not built by this wizard yet)
    "archive_installer",   # archive -> (ISO) -> installer EXE -> game
    "archive_prebuilt",    # one archive that IS the game
    "direct_prebuilt",     # one EXE that IS the game (never an installer)
    "iso_installer",       # archive -> ISO -> installer EXE -> game
)

ARCHIVE_TYPES = ("zip", "rar", "7z")

# Mirrors catalog.validManifestID: safe as a bare filename on every OS.
GAME_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")


class ValidationError(Exception):
    """A user-input problem; message is shown verbatim in the UI."""


def normalize_game_id(raw: str) -> str:
    """Normalize a user-typed game id to the backend's filename-safe form.

    Whitespace/case collapse to a lowercase-hyphen id; anything that would
    still be unsafe (empty after cleanup, dot-leading, >64 chars) is left for
    validate_game_id to reject with a friendly message.
    """
    cleaned = re.sub(r"[^A-Za-z0-9._-]+", "-", (raw or "").strip().lower())
    cleaned = cleaned.strip("-._")
    return cleaned[:64]


def validate_game_id(game_id: str) -> None:
    if not game_id:
        raise ValidationError("Game ID is required (used as the manifest filename).")
    if len(game_id) > 64:
        raise ValidationError("Game ID must be 64 characters or fewer.")
    if game_id[0] == ".":
        raise ValidationError("Game ID must start with a letter or digit.")
    if not GAME_ID_RE.match(game_id):
        raise ValidationError(
            "Game ID may only contain letters, digits, dots, hyphens and underscores."
        )


def validate_url(raw: str) -> str:
    """Validate one source URL (http/https/file only). Returns the cleaned URL."""
    url = (raw or "").strip()
    if not url:
        raise ValidationError("Source URL is empty.")
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https", "file"):
        raise ValidationError(f"Unsupported URL scheme in {url!r} (use http, https or file).")
    if parsed.scheme in ("http", "https") and not parsed.netloc:
        raise ValidationError(f"URL is missing a host: {url!r}")
    return url


def url_basename(raw: str) -> str:
    """Final path segment of a URL, matching cmd/play's urlBasename."""
    parsed = urlparse(raw)
    path = parsed.path or ""
    name = path.rstrip("/").rsplit("/", 1)[-1] if path else ""
    # Windows-unsafe characters cannot appear in a URL path meaningfully.
    return name if name and name not in (".", "..") else ""


def is_probable_exe(url: str) -> bool:
    name = url_basename(url).lower()
    return name.endswith(".exe") or name.endswith(".msi")


def is_probable_archive(url: str) -> bool:
    name = url_basename(url).lower()
    return name.endswith(".zip") or name.endswith(".rar") or name.endswith(".7z") or name.endswith(".iso")


@dataclass
class SourceLine:
    """One parsed source row from the URL-per-line field."""

    url: str
    filename: str = ""
    sha256: str = ""
    size_bytes: int = 0

    @property
    def effective_filename(self) -> str:
        return self.filename or url_basename(self.url)


def parse_source_lines(text: str) -> list[SourceLine]:
    """Parse the multiline sources field: one URL per line.

    Blank lines and #comments are skipped; surrounding whitespace ignored.
    N lines -> N ordered sources (arbitrary N, no fixed field count).
    """
    sources: list[SourceLine] = []
    for lineno, raw in enumerate((text or "").splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        url = validate_url(line)
        sources.append(SourceLine(url=url, filename=""))
    return sources


SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")


def validate_sha256(value: str) -> str:
    v = (value or "").strip().lower()
    if v and not SHA256_RE.match(v):
        raise ValidationError("SHA-256 must be 64 hex characters.")
    return v


def parse_size(value: str) -> int:
    v = (value or "").strip()
    if not v:
        return 0
    try:
        n = int(v)
    except ValueError as exc:
        raise ValidationError(f"Size must be an integer number of bytes, got {v!r}.") from exc
    if n < 0:
        raise ValidationError("Size cannot be negative.")
    return n


MODE_LABELS = {
    "archive_installer": "Archive + Installer",
    "iso_installer": "Archive + ISO + Installer",
    "archive_prebuilt": "Prebuilt Archive",
    "direct_prebuilt": "Direct EXE",
}


@dataclass
class AddGameSpec:
    """Everything the wizard collects, in backend terms."""

    game_id: str = ""
    name: str = ""
    version: str = "1.0"
    package_type: str = ""
    archive_type: str = "zip"
    sources: list[SourceLine] = field(default_factory=list)
    # prebuilt / direct
    game_root: str = ""
    expected_files: list[str] = field(default_factory=list)
    # installer modes
    installer_path: str = ""
    installer_args: list[str] = field(default_factory=list)
    installer_timeout: int = 3600
    installer_expected_dir: str = ""
    # ISO stage
    iso_result_path: str = ""
    iso_extract: bool = False
    # launch
    exe: str = ""
    launch_args: list[str] = field(default_factory=list)
    # saves
    ludusavi_title: str = ""
    save_paths: list[str] = field(default_factory=list)
    # footprint hints
    download_bytes: int = 0
    installed_bytes: int = 0

    @property
    def mode_label(self) -> str:
        return MODE_LABELS.get(self.package_type, self.package_type)

    @property
    def is_installer_mode(self) -> bool:
        return self.package_type in ("archive_installer", "iso_installer")

    @property
    def is_prebuilt_mode(self) -> bool:
        return self.package_type in ("archive_prebuilt", "direct_prebuilt")


def validate_spec(spec: AddGameSpec) -> list[str]:
    """Return a list of user-readable problems (empty when valid).

    Enforces the backend's rules (validatePackageType + add-game coherence)
    without a network round-trip or a download.
    """
    errors: list[str] = []
    try:
        validate_game_id(spec.game_id)
    except ValidationError as exc:
        errors.append(str(exc))
    if not spec.name.strip():
        errors.append("Game name is required.")
    if spec.package_type not in PACKAGE_TYPES:
        errors.append("Choose what you are adding (Prebuilt game or Installer game).")

    if not spec.sources:
        errors.append("Add at least one source URL (one per line).")

    # Per-source checks.
    for i, src in enumerate(spec.sources, 1):
        try:
            validate_url(src.url)
        except ValidationError as exc:
            errors.append(f"Source {i}: {exc}")
        if not src.effective_filename:
            errors.append(f"Source {i}: cannot infer a filename from the URL; add a filename.")
        try:
            validate_sha256(src.sha256)
        except ValidationError as exc:
            errors.append(f"Source {i}: {exc}")
        if src.size_bytes < 0:
            errors.append(f"Source {i}: size cannot be negative.")

    # Mode-specific source rules (single-link vs multipart).
    if spec.is_prebuilt_mode and len(spec.sources) > 1:
        errors.append(
            f"{spec.mode_label} takes exactly one source; "
            f"got {len(spec.sources)} lines. Multipart archives are installer games."
        )
    if spec.is_installer_mode and not spec.installer_path.strip():
        errors.append("Installer executable (inside the archive) is required, e.g. setup.exe")

    if spec.is_prebuilt_mode and spec.installer_path.strip():
        # Never invent an installer stage for a prebuilt package.
        errors.append("Prebuilt games cannot have an installer executable (the package is already the game).")

    if not spec.exe.strip():
        errors.append("Launch executable is required (the game's .exe inside the game folder).")

    # ISO stage only where the backend allows it.
    if spec.iso_extract or spec.iso_result_path.strip():
        if spec.is_prebuilt_mode:
            errors.append("ISO stages do not apply to prebuilt games.")
        elif spec.iso_extract and not spec.iso_result_path.strip():
            errors.append("ISO extraction needs an ISO result path (e.g. game.iso).")

    if spec.archive_type not in ARCHIVE_TYPES:
        errors.append("Archive type must be zip, rar or 7z.")

    try:
        t = int(spec.installer_timeout)
        if t <= 0:
            raise ValueError
    except (TypeError, ValueError):
        errors.append("Installer timeout must be a positive number of seconds.")

    return errors


def _source_json(spec: AddGameSpec) -> list[dict]:
    return [
        {
            "url": s.url,
            "part": i,
            "filename": s.effective_filename,
            **({"sha256": validate_sha256(s.sha256)} if validate_sha256(s.sha256) else {}),
            **({"size_bytes": s.size_bytes} if s.size_bytes else {}),
        }
        for i, s in enumerate(spec.sources, 1)
    ]


def build_manifest(spec: AddGameSpec) -> dict:
    """Build the v3 GameManifest JSON exactly like cmd/play add-game would.

    Raises ValidationError for any invalid combination — the wizard never
    silently invents stages or drops user intent.
    """
    errors = validate_spec(spec)
    if errors:
        raise ValidationError("; ".join(errors))

    pt = spec.package_type
    sources = _source_json(spec)
    pipeline: dict = {}
    cleanup: dict = {}

    if pt in ("archive_installer", "iso_installer"):
        pipeline["archive"] = {"type": spec.archive_type, "multipart": len(sources) > 1}
        if spec.iso_result_path or spec.iso_extract:
            pipeline["result"] = {"type": "iso", "path": spec.iso_result_path}
            pipeline["iso"] = {"extract": bool(spec.iso_extract)}
        pipeline["installer"] = {
            "type": "exe",
            "path": spec.installer_path,
            "arguments": list(spec.installer_args),
            "timeout_seconds": int(spec.installer_timeout),
            **({"expected_dir": spec.installer_expected_dir} if spec.installer_expected_dir else {}),
        }
        cleanup = {
            "delete_parts_after_archive_extract": True,
            "delete_iso_after_extract": bool(spec.iso_extract),
            "delete_installer_after_install": True,
        }
    elif pt == "archive_prebuilt":
        pipeline["archive"] = {"type": spec.archive_type}
        pipeline["prebuilt"] = {
            **({"game_root": spec.game_root} if spec.game_root else {}),
            "expected_files": list(spec.expected_files),
        }
        cleanup = {"delete_archive_after_prebuilt_ready": True}
    elif pt == "direct_prebuilt":
        pipeline["prebuilt"] = {
            **({"game_root": spec.game_root} if spec.game_root else {}),
            "expected_files": list(spec.expected_files),
        }
        cleanup = {"delete_archive_after_prebuilt_ready": True}
    # provider: no pipeline stages (not produced by this wizard yet)

    manifest = {
        "schema_version": 3,
        "game_id": spec.game_id,
        "name": spec.name.strip(),
        "version": spec.version or "1.0",
        "acquisition": {
            "provider": "archive",
            "package_type": pt,
            "sources": sources,
        },
        "footprint": {
            "download_bytes": max(0, int(spec.download_bytes)),
            "installed_bytes": max(0, int(spec.installed_bytes)),
        },
        "package_pipeline": pipeline,
        "cleanup": cleanup,
        "runtime": {"os": "windows"},
        "launch": {
            "executable": spec.exe.strip(),
            "arguments": list(spec.launch_args),
            "working_directory": ".",
        },
        "saves": {
            "provider": "ludusavi",
            **({"ludusavi_title": spec.ludusavi_title} if spec.ludusavi_title else {}),
            **(
                {"overrides": [{"platform": "any", "paths": list(spec.save_paths)}]}
                if spec.save_paths
                else {}
            ),
        },
        "capabilities_required": {"gamepad": False, "hdr": False, "min_vram_mb": 0},
    }
    return manifest
