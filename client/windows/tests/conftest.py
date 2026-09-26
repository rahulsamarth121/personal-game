"""Test bootstrap: make `client.windows.*` importable when pytest runs from
``client/windows``. Shared fixtures for all UI test modules."""

import sys
from pathlib import Path

_REPO = Path(__file__).resolve().parents[3]  # .../personal-game/github
if str(_REPO) not in sys.path:
    sys.path.insert(0, str(_REPO))

import pytest


@pytest.fixture
def settings(tmp_path, monkeypatch):
    """Settings persisted into a per-test temp file."""
    from client.windows.services import settings as settings_mod
    monkeypatch.setattr(settings_mod, "SETTINGS_FILE", tmp_path / "ui-settings.json")
    s = settings_mod.Settings()
    s.save()
    return s
