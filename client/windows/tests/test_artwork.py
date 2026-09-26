"""Artwork tests: deterministic placeholders, no network."""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")


@pytest.fixture(scope="module")
def qapp():
    from PySide6.QtWidgets import QApplication
    app = QApplication.instance() or QApplication([])
    yield app


def test_placeholder_is_deterministic(qapp):
    from PySide6.QtGui import QImage

    from client.windows.services.artwork import placeholder_pixmap
    a = placeholder_pixmap("DOOM II", "doom2", 264, 148)
    b = placeholder_pixmap("DOOM II", "doom2", 264, 148)
    img_a, img_b = a.toImage(), b.toImage()
    assert img_a == img_b  # same inputs -> same pixels


def test_placeholder_differs_by_game(qapp):
    from client.windows.services.artwork import placeholder_pixmap
    a = placeholder_pixmap("DOOM II", "doom2", 264, 148).toImage()
    b = placeholder_pixmap("Half-Life", "half-life", 264, 148).toImage()
    assert a != b


def test_initials(qapp):
    from client.windows.services.artwork import initials
    assert initials("DOOM II") == "DI"
    assert initials("Quake") == "QU"
    assert initials("") == "?"


def test_load_artwork_falls_back_to_placeholder(qapp, tmp_path, monkeypatch):
    from client.windows.services import settings as settings_mod
    from client.windows.services.artwork import load_artwork

    monkeypatch.setattr(settings_mod, "SETTINGS_FILE", tmp_path / "s.json")
    s = settings_mod.Settings()
    pm = load_artwork(s, "doom2", "DOOM II", 264, 148)
    assert not pm.isNull()
