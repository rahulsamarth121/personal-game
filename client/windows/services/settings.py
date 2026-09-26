"""Local UI settings, persisted as JSON under the user's config directory.

Never stores secrets — the control plane is unauthenticated LAN/local for
this single-user system, and Moonlight owns its own pairing data. The file
is written atomically (tmp + rename) and validated on load.
"""

from __future__ import annotations

import json
import os
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

from ..api import DEFAULT_CONTROL_URL, DEFAULT_USER_ID

CONFIG_DIR = Path(os.environ.get("APPDATA") or Path.home() / ".config") / "personal-game"
SETTINGS_FILE = CONFIG_DIR / "ui-settings.json"

# The UI launches only these known commands — never anything user-typed.
DEFAULT_PLAY_COMMAND = "play"


@dataclass
class Settings:
    control_url: str = DEFAULT_CONTROL_URL
    user_id: str = DEFAULT_USER_ID
    api_token: str = ""                # bearer token when the plane requires one
    moonlight_path: str = ""           # empty = rely on PATH lookup
    play_command: str = DEFAULT_PLAY_COMMAND  # "play" | repo-relative go run
    artwork_dir: str = ""              # optional user artwork overrides
    developer_mode: bool = False

    @classmethod
    def load(cls) -> "Settings":
        try:
            raw = SETTINGS_FILE.read_text(encoding="utf-8")
            data = json.loads(raw)
        except (OSError, json.JSONDecodeError):
            return cls()
        if not isinstance(data, dict):
            return cls()
        s = cls()
        for key in ("control_url", "user_id", "api_token", "moonlight_path", "play_command", "artwork_dir"):
            value = data.get(key)
            if isinstance(value, str) and value:
                setattr(s, key, value)
        s.developer_mode = bool(data.get("developer_mode", False))
        return s

    def save(self) -> None:
        SETTINGS_FILE.parent.mkdir(parents=True, exist_ok=True)
        payload = {
            "control_url": self.control_url,
            "user_id": self.user_id,
            "api_token": self.api_token,
            "moonlight_path": self.moonlight_path,
            "play_command": self.play_command,
            "artwork_dir": self.artwork_dir,
            "developer_mode": self.developer_mode,
        }
        fd, tmp = tempfile.mkstemp(dir=str(SETTINGS_FILE.parent), suffix=".tmp")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                json.dump(payload, f, indent=2)
            os.replace(tmp, SETTINGS_FILE)
        except OSError:
            try:
                os.unlink(tmp)
            except OSError:
                pass
            raise


@dataclass
class ArtworkOverride:
    """Optional per-game artwork the user selected locally."""

    game_id: str
    image_path: str


def artwork_overrides(settings: Settings) -> dict[str, str]:
    """game_id -> image path, read from <artwork_dir>/<game_id>.png|jpg."""
    out: dict[str, str] = {}
    if not settings.artwork_dir:
        return out
    root = Path(settings.artwork_dir)
    if not root.is_dir():
        return out
    for ext in ("png", "jpg", "jpeg", "webp"):
        for p in root.glob(f"*.{ext}"):
            out[p.stem] = str(p)
    return out
