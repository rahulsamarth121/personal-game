"""Worker unit tests (no network): run the exported fetch() handler with
fake upstreams via a tiny Node shim? No — pure-Python approach: the Worker
is JavaScript, so these tests execute it with the `quickjs`-free route:
we validate the source statically AND run behavioral tests through
`wrangler dev` when available.

To keep the suite dependency-free and hermetic, the behavioral contract is
exercised with Node (present alongside wrangler) using the `workerd`-free
approach: a minimal fetch/Response shim implementing what worker.js uses.
That shim lives in test/shim.mjs and is invoked via subprocess.
"""

from __future__ import annotations

import json
import shutil
import subprocess
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
WORKER = HERE / "worker.js"

pytestmark = pytest.mark.skipif(
    shutil.which("node") is None, reason="node not available for worker tests"
)


def run_worker(
    url: str,
    method: str = "GET",
    body: dict | None = None,
    headers: dict | None = None,
    env: dict | None = None,
) -> tuple[int, dict | str, dict]:
    """Execute worker.fetch() under Node with a WHATWG-shaped shim."""
    payload = {
        "url": url,
        "method": method,
        "body": json.dumps(body) if body is not None else None,
        "headers": headers or {},
        "env": env or {},
    }
    proc = subprocess.run(
        [shutil.which("node"), str(HERE / "test" / "shim.mjs"), WORKER.name],
        input=json.dumps(payload),
        capture_output=True,
        text=True,
        cwd=HERE,
        timeout=30,
    )
    if proc.returncode != 0:
        raise AssertionError(f"worker crashed: {proc.stderr[-2000:]}")
    out = json.loads(proc.stdout)
    body_out: dict | str
    try:
        body_out = json.loads(out["body"])
    except (json.JSONDecodeError, TypeError):
        body_out = out["body"]
    return out["status"], body_out, out.get("headers", {})


ORIGIN = "https://control.internal"
ENV = {"CONTROL_PLANE_ORIGIN": ORIGIN}


# --- static contract ---------------------------------------------------------

def test_worker_source_never_logs_authorization():
    src = WORKER.read_text(encoding="utf-8")
    assert "console.log" not in src and "console.error" not in src
    # Authorization is forwarded, not logged or echoed into responses.
    assert '"Authorization"' in src
    # AbortSignal.timeout is a static factory; `new` would throw at runtime.
    assert "new AbortSignal.timeout" not in src


def test_worker_has_single_configured_origin_no_client_destination():
    src = WORKER.read_text(encoding="utf-8")
    assert "CONTROL_PLANE_ORIGIN" in src
    # The worker must not accept a destination from the request.
    for forbidden in ("url.searchParams.get('target')", "x-upstream", "X-Upstream"):
        assert forbidden not in src


# --- behavior ----------------------------------------------------------------

def test_root_health():
    status, body, headers = run_worker("https://relay.test/", env=ENV)
    assert status == 200
    assert "PERSONAL GAME RELAY OK" in body
    assert headers.get("content-type", "").startswith("text/plain")


def test_relay_health_upstream_ok():
    status, body, _ = run_worker(
        "https://relay.test/personal-game/health", env=ENV,
        headers={"__mock_upstream__": ORIGIN},
    )
    assert status == 200
    assert body == {"relay": "ok", "upstream": "ok", "project": "personal-game"}


def test_relay_health_upstream_unreachable():
    status, body, _ = run_worker(
        "https://relay.test/personal-game/health",
        env={"CONTROL_PLANE_ORIGIN": "https://127.0.0.1:9"},
    )
    assert status == 200
    assert body["relay"] == "ok"
    assert body["upstream"] == "unreachable"  # honest, never faked


def test_relay_health_misconfigured():
    status, body, _ = run_worker("https://relay.test/personal-game/health", env={})
    assert status == 200
    assert body["upstream"] == "misconfigured"


def test_path_rewrite_preserves_v1_path_and_query():
    # The shim records the upstream URL the worker fetched.
    status, body, headers = run_worker(
        "https://relay.test/personal-game/v1/games?user_id=player1",
        env=ENV, headers={"__mock_upstream__": ORIGIN},
    )
    upstream = json.loads(headers.get("x-mock-upstream-url", "{}"))
    assert upstream["path"] == "/v1/games"
    assert upstream["search"] == "?user_id=player1"
    assert upstream["origin"] == ORIGIN


def test_post_body_and_content_type_preserved():
    payload = {"user_id": "player1", "game_id": "doom2"}
    status, body, headers = run_worker(
        "https://relay.test/personal-game/v1/sessions",
        method="POST", body=payload,
        headers={"Content-Type": "application/json", "__mock_upstream__": ORIGIN},
        env=ENV,
    )
    upstream = json.loads(headers.get("x-mock-upstream-url", "{}"))
    assert upstream["method"] == "POST"
    assert json.loads(upstream["body"]) == payload
    assert upstream["headers"].get("content-type") == "application/json"
    assert body == {"proxied": True}  # upstream response passed through


def test_authorization_forwarded_opaquely():
    status, _body, headers = run_worker(
        "https://relay.test/personal-game/v1/games",
        env=ENV,
        headers={"Authorization": "Bearer sekrit-token", "__mock_upstream__": ORIGIN},
    )
    upstream = json.loads(headers.get("x-mock-upstream-url", "{}"))
    assert upstream["headers"].get("authorization") == "Bearer sekrit-token"
    # And never reflected back to the client in the relay's own headers.
    own = {k: v for k, v in headers.items() if k != "x-mock-upstream-url"}
    assert "sekrit-token" not in json.dumps(own)


def test_unknown_paths_rejected():
    for path in ("/v1/games", "/zed/relay", "/personal-gameX/v1/games", "/admin"):
        status, body, _ = run_worker(f"https://relay.test{path}", env=ENV)
        assert status == 404, path


def test_arbitrary_upstream_impossible():
    # Even a path that looks like a URL must stay under the fixed origin.
    status, _body, headers = run_worker(
        "https://relay.test/personal-game/https://evil.example/v1/games",
        env=ENV, headers={"__mock_upstream__": ORIGIN},
    )
    upstream = json.loads(headers.get("x-mock-upstream-url", "{}"))
    assert upstream["origin"] == ORIGIN
    assert upstream["path"].startswith("/https:")  # treated as a path only


def test_invalid_method_rejected():
    status, body, _ = run_worker(
        "https://relay.test/personal-game/v1/games", method="TRACE", env=ENV
    )
    assert status == 405
    assert body == {"error": "method not allowed"}


def test_connect_rejected():
    status, _, _ = run_worker(
        "https://relay.test/personal-game/v1/games", method="CONNECT", env=ENV
    )
    assert status == 405


def test_oversized_request_rejected():
    status, body, _ = run_worker(
        "https://relay.test/personal-game/v1/sessions",
        method="POST",
        headers={"Content-Length": str(50 * 1024 * 1024), "__mock_upstream__": ORIGIN},
        env={**ENV, "RELAY_MAX_BODY_BYTES": "1024"},
    )
    assert status == 413


def test_upstream_failure_returns_502():
    status, body, _ = run_worker(
        "https://relay.test/personal-game/v1/games",
        env=ENV,
        headers={"__mock_upstream__": ORIGIN, "__mock_fail__": "1"},
    )
    assert status == 502
    assert body == {"error": "control plane unavailable"}


def test_trailing_slash_base_normalization():
    """<base>/personal-game/ + /v1/games must not become //v1/games."""
    status, _body, headers = run_worker(
        "https://relay.test/personal-game//v1/games",
        env=ENV, headers={"__mock_upstream__": ORIGIN},
    )
    upstream = json.loads(headers.get("x-mock-upstream-url", "{}"))
    assert "//" not in upstream["path"]
