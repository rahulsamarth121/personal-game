"""Play client wrapper tests (no live session, no Moonlight needed)."""

from __future__ import annotations

import sys
from pathlib import Path

from client.windows.services.play_client import PlayClient


def test_moonlight_missing_is_honest(monkeypatch):
    monkeypatch.setattr("client.windows.services.play_client.shutil.which", lambda name: None)
    client = PlayClient("http://127.0.0.1:8080", "player1")
    ok, detail = client.moonlight_available()
    assert ok is False
    assert "Moonlight" in detail


def test_moonlight_on_path_is_found(monkeypatch):
    monkeypatch.setattr(
        "client.windows.services.play_client.shutil.which",
        lambda name: "C:/fake/moonlight.exe" if name.startswith("moonlight") else None)
    client = PlayClient("http://127.0.0.1:8080", "player1")
    ok, resolved = client.moonlight_available()
    assert ok is True
    assert "moonlight" in resolved.lower()


def test_start_refuses_without_moonlight(monkeypatch):
    """No session is created when Moonlight is missing (honest failure)."""
    monkeypatch.setattr("client.windows.services.play_client.shutil.which", lambda name: None)
    events = []
    client = PlayClient("http://127.0.0.1:8080", "player1")
    # Also make Popen record if it were called (it must not be).
    def _no_popen(*a, **kw):  # pragma: no cover
        raise AssertionError("Popen must not be called when Moonlight is missing")
    monkeypatch.setattr("client.windows.services.play_client.subprocess.Popen", _no_popen)
    client.start("doom2", events.append)
    assert len(events) == 1
    assert events[0].kind == "error"
    assert "Moonlight" in events[0].message


def test_progress_classification_of_real_cli_lines():
    client = PlayClient("http://127.0.0.1:8080", "player1")
    ev = client._classify("session 6f2c on node gpu-1 (state NODE_ASSIGNED)")
    assert ev.kind == "session_created"
    ev = client._classify("[+3s] Node assigned, preparing...")
    assert ev.kind == "node_assigned"
    ev = client._classify("[+8s] Checking game cache, restoring save, starting backend...")
    assert ev.kind == "preparing"
    ev = client._classify('READY on 100.1.2.3 via wolf — streaming app "DOOM II"')
    assert ev.kind == "ready"
    assert ev.host == "100.1.2.3"
    assert ev.provider == "wolf"
    ev = client._classify("moonlight not found — install it, then run:")
    assert ev.kind == "moonlight_missing"
    ev = client._classify("play: something unexpected")
    assert ev.kind == "progress"


def test_resolve_binary_prefers_explicit_setting(tmp_path, monkeypatch):
    exe = tmp_path / "custom-play.exe"
    exe.write_bytes(b"")
    client = PlayClient("http://127.0.0.1:8080", "player1", play_command=str(exe))
    assert client.resolve_binary() == [str(exe)]


def test_resolve_binary_dev_fallback_to_go_run(monkeypatch):
    monkeypatch.setattr(
        "client.windows.services.play_client.Path.is_file", lambda self: False)
    monkeypatch.setattr(
        "client.windows.services.play_client.shutil.which",
        lambda name: "C:/Go/go.exe" if name == "go" else None)
    client = PlayClient("http://127.0.0.1:8080", "player1")
    argv = client.resolve_binary()
    assert argv is not None
    assert argv[0] == "go"
    assert argv[1:3] == ["run", "./cmd/play"]


def test_resolve_binary_returns_none_without_any_option(monkeypatch):
    monkeypatch.setattr(
        "client.windows.services.play_client.Path.is_file", lambda self: False)
    monkeypatch.setattr(
        "client.windows.services.play_client.shutil.which", lambda name: None)
    client = PlayClient("http://127.0.0.1:8080", "player1")
    assert client.resolve_binary() is None


def test_start_reports_missing_client_without_popen(monkeypatch):
    """Moonlight present but no play client -> honest error, no Popen."""
    monkeypatch.setattr(
        "client.windows.services.play_client.Path.is_file", lambda self: False)
    monkeypatch.setattr(
        "client.windows.services.play_client.shutil.which",
        lambda name: "C:/fake/moonlight.exe" if name.startswith("moonlight") else None)

    def _no_popen(*a, **kw):  # pragma: no cover
        raise AssertionError("Popen must not be called without a play client")
    monkeypatch.setattr("client.windows.services.play_client.subprocess.Popen", _no_popen)
    events = []
    client = PlayClient("http://127.0.0.1:8080", "player1")
    client.start("doom2", events.append)
    assert events and events[0].kind == "error"
    assert "play client" in events[0].message
