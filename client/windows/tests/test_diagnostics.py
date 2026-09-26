"""Tests for API-token handling, diagnostics page, and play-stage labels."""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from client.windows.api import ControlClient

pytest.importorskip("PySide6", reason="PySide6 not installed")


class TokenControl(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, payload):
        body = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        auth = self.headers.get("Authorization", "")
        if self.path == "/healthz":
            self._send(200, {"status": "ok"})
        elif not auth == "Bearer good-token":
            self._send(401, {"error": "invalid or missing API token"})
        elif self.path == "/v1/games":
            self._send(200, {"games": []})
        else:
            self._send(404, {"error": "unknown route"})


@pytest.fixture(scope="module")
def control_url():
    server = HTTPServer(("127.0.0.1", 0), TokenControl)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{server.server_port}"
    server.shutdown()


def test_client_sends_bearer_token(control_url):
    client = ControlClient(control_url, api_token="good-token")
    assert client.games() == []


def test_client_without_token_gets_401(control_url):
    from client.windows.api import ControlPlaneError
    with pytest.raises(ControlPlaneError) as exc:
        ControlClient(control_url).games()
    assert exc.value.status == 401


def test_healthz_open_without_token(control_url):
    assert ControlClient(control_url).health() is True


def test_base_url_trailing_slash_normalized():
    """<worker>/personal-game/ + /v1/games must not become //v1/games."""
    c1 = ControlClient("https://w.test/personal-game")
    c2 = ControlClient("https://w.test/personal-game/")
    assert c1.base_url == c2.base_url == "https://w.test/personal-game"
    assert (c1.base_url + "/v1/games").endswith("/personal-game/v1/games")
    assert "//v1" not in (c1.base_url + "/v1/games")


def test_nodes_snapshot_degrades_honestly(control_url):
    client = ControlClient(control_url, api_token="good-token")
    assert client.nodes_snapshot() == []


def test_settings_roundtrip_token(tmp_path, monkeypatch):
    from client.windows.services import settings as settings_mod
    monkeypatch.setattr(settings_mod, "SETTINGS_FILE", tmp_path / "s.json")
    s = settings_mod.Settings()
    s.api_token = "tok-1"
    s.save()
    assert settings_mod.Settings.load().api_token == "tok-1"


def test_play_stage_labels_are_friendly():
    from client.windows.app import MainWindow
    labels = MainWindow.STAGE_LABELS
    assert labels["session_created"] == "Finding available gaming node…"
    assert labels["Checking game cache, restoring save, starting backend..."].startswith("Restoring save")
    assert "Preparing game…" in labels.values()


@pytest.fixture(scope="module")
def qapp():
    from PySide6.QtWidgets import QApplication
    app = QApplication.instance() or QApplication([])
    yield app


def test_diagnostics_page_renders(qapp, settings, monkeypatch):
    from client.windows.app import MainWindow
    from client.windows.api import ControlClient, ControlPlaneError

    class NoNodeClient(ControlClient):
        def __init__(self):
            super().__init__("http://fake")
            self.online = True

        def health(self):
            return self.online

        def games(self):
            if not self.online:
                raise ControlPlaneError("offline")
            return []

        def playtime(self, user_id):
            return []

        def nodes_snapshot(self):
            return [{
                "node_id": "gpu-1",
                "caps": {
                    "streaming_allowed": True,
                    "streaming": ["wolf"],
                    "gpu": {"model": "RTX Test", "vram_mb": 8192},
                    "network": {"media_network": {
                        "provider": "tailscale", "endpoint": "100.1.2.3",
                        "tcp_ok": True, "udp_ok": True, "reachable": True}},
                },
            }]

    monkeypatch.setattr("client.windows.app.ControlClient", lambda url, api_token="": NoNodeClient())
    win = MainWindow(settings)
    win.navigate("diagnostics")
    page = win.diagnostics_page
    page.refresh()
    rows = []
    for i in range(page.cp.grid.count()):
        rows.append(page.cp.grid.itemAt(i).widget())
    texts = [w.text() for w in rows if w is not None]
    assert any("✓" in t for t in texts)      # reachable shown with a check
    assert any("n/a" in t for t in texts)    # honest: no token configured
    node_texts = []
    for i in range(page.node.grid.count()):
        w = page.node.grid.itemAt(i).widget()
        if w is not None:
            node_texts.append(w.text())
    assert any("RTX Test" in t for t in node_texts)
    assert any("streaming_allowed" in t for t in node_texts)
    win.close()
