"""API client tests against a local fake control plane (HTTP loopback)."""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from client.windows.api import ControlClient, ControlPlaneError


class FakeControl(BaseHTTPRequestHandler):
    def log_message(self, *a):  # silence
        pass

    def _send(self, code, payload):
        body = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/healthz":
            self._send(200, {"status": "ok"})
        elif self.path == "/v1/games":
            self._send(200, {"games": [
                {"game_id": "doom2", "name": "DOOM II", "version": "1.0",
                 "acquisition": {"package_type": "archive_prebuilt"},
                 "launch": {"executable": "game.exe"},
                 "runtime": {"os": "windows"}},
                {"game_id": "minimal"},  # missing optional fields must not crash
            ]})
        elif self.path.startswith("/v1/stats/playtime"):
            self._send(200, {"playtime": [
                {"game_id": "doom2", "total_active_seconds": 3725,
                 "sessions": 3, "last_played": "2026-09-25"}]})
        elif self.path.startswith("/v1/games/"):
            self._send(404, {"error": "game not found"})
        elif self.path.startswith("/v1/sessions/"):
            self._send(200, {"session_id": "abc", "game_id": "doom2",
                             "node_id": "n1", "state": "PREPARING"})
        else:
            self._send(404, {"error": "unknown route"})

    def do_POST(self):
        if self.path == "/v1/games":
            length = int(self.headers.get("Content-Length", 0))
            data = json.loads(self.rfile.read(length) or b"{}")
            self._send(201, {"manifest": data, "persisted": True})
        elif self.path == "/v1/sessions":
            self._send(201, {"session_id": "abc", "game_id": "doom2",
                             "node_id": "n1", "state": "NODE_ASSIGNED"})
        else:
            self._send(404, {"error": "unknown route"})


@pytest.fixture(scope="module")
def control_url():
    server = HTTPServer(("127.0.0.1", 0), FakeControl)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{server.server_port}"
    server.shutdown()


def test_health(control_url):
    assert ControlClient(control_url).health() is True


def test_games_parses_manifest(control_url):
    games = ControlClient(control_url).games()
    assert len(games) == 2
    assert games[0].game_id == "doom2"
    assert games[0].package_type == "archive_prebuilt"
    assert games[0].executable == "game.exe"


def test_games_missing_optional_fields_do_not_crash(control_url):
    g = ControlClient(control_url).games()[1]
    assert g.game_id == "minimal"
    assert g.version == ""
    assert g.package_type == ""


def test_playtime_server_observed_numbers(control_url):
    entries = ControlClient(control_url).playtime("player1")
    assert entries[0].total_active_seconds == 3725
    assert entries[0].sessions == 3


def test_add_game_round_trip(control_url):
    client = ControlClient(control_url)
    out = client.add_game({"schema_version": 3, "game_id": "x"})
    assert out["persisted"] is True
    assert out["manifest"]["game_id"] == "x"


def test_unreachable_control_plane_is_user_readable():
    with pytest.raises(ControlPlaneError) as exc:
        ControlClient("http://127.0.0.1:9", timeout=1).games()
    assert "Cannot reach the control plane" in str(exc.value)


def test_http_error_surfaces_message(control_url):
    client = ControlClient(control_url)
    # /v1/games/<unknown> returns 404 with {"error": ...} from the fake.
    with pytest.raises(ControlPlaneError) as exc:
        client._request("GET", "/v1/games/nope/manifest")
    assert exc.value.status == 404
    assert "game not found" in str(exc.value)
