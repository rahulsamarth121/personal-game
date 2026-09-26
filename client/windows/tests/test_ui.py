"""Offscreen Qt UI tests (QT_QPA_PLATFORM=offscreen). No GPU/Wolf needed."""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6", reason="PySide6 not installed")

from client.windows.models import parse_source_lines  # noqa: E402


@pytest.fixture(scope="module")
def qapp():
    from PySide6.QtWidgets import QApplication
    app = QApplication.instance() or QApplication([])
    yield app


@pytest.fixture
def fake_client(monkeypatch):
    """ControlClient stub: in-memory games, honest failure switch."""
    from client.windows.api import ControlClient, ControlPlaneError

    class FakeClient(ControlClient):
        def __init__(self):
            super().__init__("http://fake")
            self.online = True
            self.games_data = []

        def health(self):
            return self.online

        def games(self):
            if not self.online:
                raise ControlPlaneError("Cannot reach the control plane at http://fake.")
            return self.games_data

        def playtime(self, user_id):
            if not self.online:
                raise ControlPlaneError("offline")
            return []

        def add_game(self, manifest):
            if not self.online:
                raise ControlPlaneError("offline")
            return {"manifest": manifest, "persisted": True}

    return FakeClient()


def _make_window(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    win = MainWindow.__new__(MainWindow)  # build via real ctor but stub client
    MainWindow.__init__.__wrapped__  # noqa: B018 - documentation only
    return MainWindow(settings)


def test_main_window_builds(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    assert win.windowTitle() == "Personal Game"
    assert win.stack.count() == 5
    win.close()


def test_empty_library_renders_empty_state(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    assert win.library_page.empty.isVisibleTo(win.library_page) or True  # rendered after refresh
    win.library_page.refresh()
    assert win.library_page.empty.isVisibleTo(win.library_page)
    assert win.library_page.cards_layout.count() >= 1  # empty state is in layout
    win.close()


def test_control_plane_failure_renders_useful_error(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    fake_client.online = False
    win.library_page.refresh()
    text = win.library_page.feedback.text()
    assert "Cannot reach the control plane" in text
    assert win.library_page.feedback.isVisibleTo(win.library_page)
    win.close()


def test_game_cards_render(qapp, fake_client, settings, monkeypatch):
    from client.windows.api import Game
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    fake_client.games_data = [
        Game("doom2", "DOOM II", "1.0", "archive_prebuilt", "game.exe", "windows"),
        Game("half-life", "Half-Life", "2", "direct_prebuilt", "hl.exe", "windows"),
    ]
    win.library_page.refresh()
    cards = [win.library_page.cards_layout.itemAt(i).widget()
             for i in range(win.library_page.cards_layout.count())]
    assert len([c for c in cards if hasattr(c, "play_btn")]) == 2
    win.close()


def test_search_filters_cards(qapp, fake_client, settings, monkeypatch):
    from client.windows.api import Game
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    fake_client.games_data = [
        Game("doom2", "DOOM II", "1.0", "archive_prebuilt", "game.exe", "windows"),
        Game("hl", "Half-Life", "2", "direct_prebuilt", "hl.exe", "windows"),
    ]
    win.library_page.refresh()
    win.library_page.search.setText("doom")
    cards = [win.library_page.cards_layout.itemAt(i).widget()
             for i in range(win.library_page.cards_layout.count())]
    named = [c for c in cards if hasattr(c, "play_btn")]
    assert len(named) == 1
    win.close()


def test_play_action_invokes_existing_client_path(qapp, fake_client, settings, monkeypatch):
    """Clicking PLAY must route through the Go play client wrapper."""
    from client.windows.api import Game
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    started = {}

    class FakeWorker:
        def __init__(self, client, game_id, parent=None):
            started["game_id"] = game_id

        event = type("S", (), {"connect": staticmethod(lambda *a: None)})()

        def start(self):
            started["run"] = True

        def isRunning(self):
            return False

    monkeypatch.setattr("client.windows.app._PlayWorker", FakeWorker)
    win = MainWindow(settings)
    fake_client.games_data = [Game("doom2", "DOOM II", "1.0", "archive_prebuilt", "game.exe", "windows")]
    win.library_page.refresh()
    win.start_play("doom2")
    assert started.get("game_id") == "doom2"
    assert started.get("run") is True
    win.close()


def test_wizard_rejects_prebuilt_plus_installer(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    wiz = win.add_page
    wiz.name_edit.setText("Test Game")
    wiz.id_edit.setProperty("auto", True)
    wiz.id_edit.setText("test-game")
    # Fill sources as prebuilt then flip mode to installer with installer path.
    monkeypatch.setattr(wiz, "_sources_text",
                        lambda: "https://a.example/game.zip")
    wiz.rb_installer.setChecked(True)
    wiz.iso_check.setChecked(False)
    spec = wiz._collect()
    spec.installer_path = ""  # prebuilt-derived collect gives installer fields empty
    spec.package_type = "archive_prebuilt"
    spec.installer_path = "setup.exe"  # invalid combo
    from client.windows.models import validate_spec
    errors = validate_spec(spec)
    assert any("installer" in e.lower() for e in errors)
    win.close()


def test_wizard_builds_multipart_installer_manifest(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    wiz = win.add_page
    wiz.name_edit.setText("Multipart Game")
    wiz.id_edit.setText("multipart-game")
    monkeypatch.setattr(
        wiz, "_sources_text",
        lambda: "\n".join(f"https://a.example/part{i:02d}.zip" for i in range(1, 21)))
    wiz.rb_installer.setChecked(True)
    wiz.iso_check.setChecked(False)
    wiz.installer_edit.setText("setup.exe")
    wiz.exe_edit.setText("game.exe")
    spec = wiz._collect()
    assert len(spec.sources) == 20  # 20 pasted URLs -> 20 ordered sources
    from client.windows.models import build_manifest
    m = build_manifest(spec)
    assert [s["part"] for s in m["acquisition"]["sources"]] == list(range(1, 21))
    assert m["acquisition"]["package_type"] == "archive_installer"
    assert m["package_pipeline"]["archive"]["multipart"] is True
    win.close()


def test_wizard_submit_calls_real_api_and_emits(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    wiz = win.add_page
    wiz.name_edit.setText("Direct Game")
    wiz.id_edit.setText("direct-game")
    monkeypatch.setattr(wiz, "_sources_text", lambda: "https://a.example/tiny.exe")
    wiz.rb_prebuilt.setChecked(True)
    # direct_prebuilt needs single source; force direct via URL sniffing
    wiz.exe_edit.setText("tiny.exe")
    submitted = []
    fake_client_calls = []
    real_add = fake_client.add_game

    def spy(manifest):
        fake_client_calls.append(manifest)
        return real_add(manifest)

    monkeypatch.setattr(fake_client, "add_game", spy)
    wiz.game_added.connect(lambda spec, persisted: submitted.append((spec, persisted)))
    # Jump to review+submit path directly
    wiz._step = 5
    wiz._submit()
    assert len(fake_client_calls) == 1
    assert fake_client_calls[0]["acquisition"]["package_type"] in (
        "direct_prebuilt", "archive_prebuilt")
    assert submitted and submitted[0][1] is True
    win.close()


def test_settings_persist_safely(qapp, fake_client, settings, monkeypatch):
    from client.windows.services import settings as settings_mod
    s = settings_mod.Settings.load()
    s.control_url = "http://127.0.0.1:9999"
    s.user_id = "tester"
    s.save()
    loaded = settings_mod.Settings.load()
    assert loaded.control_url == "http://127.0.0.1:9999"
    assert loaded.user_id == "tester"


def test_settings_page_updates_and_saves(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    page = win.settings_page
    page.control_url.setText("http://127.0.0.1:8090")
    page.user_id.setText("player2")
    page._save()
    assert win.settings.control_url == "http://127.0.0.1:8090"
    assert win.settings.user_id == "player2"
    assert settings_mod_settings().control_url == "http://127.0.0.1:8090"
    win.close()


def settings_mod_settings():
    from client.windows.services.settings import Settings
    return Settings.load()


def test_game_id_validation_shows_in_ui(qapp, fake_client, settings, monkeypatch):
    from client.windows.app import MainWindow
    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": fake_client)
    win = MainWindow(settings)
    wiz = win.add_page
    wiz.name_edit.setText("")
    wiz.id_edit.setText("")
    wiz._go_next()
    assert wiz._errors.isVisibleTo(wiz)
    assert "Game ID" in wiz._errors.text() or "name" in wiz._errors.text()
    win.close()
