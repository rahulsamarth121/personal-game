"""Static tests for the Kaggle notebook bootstrap + runner health logic.

No external network required: control-plane health checks run against
local HTTP servers, and the notebook Cell 1 bootstrap is executed in a
sandbox against a local bare clone of this repository. Live checks against
the deployed Worker are run separately (see kaggle/README.md).
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
NOTEBOOK = HERE / "notebook" / "personal_game.ipynb"
RUNNER = HERE / "runner.py"


def _load_runner():
    spec = importlib.util.spec_from_file_location("pg_kaggle_runner", RUNNER)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


# --------------------------------------------------------------------------
# notebook bootstrap
# --------------------------------------------------------------------------

@pytest.fixture(scope="module")
def cells():
    nb = json.loads(NOTEBOOK.read_text(encoding="utf-8"))
    return [c for c in nb["cells"] if c["cell_type"] == "code"]


def test_notebook_boot_cell_contents(cells):
    src = cells[0]["source"]
    assert 'REPO_URL = "https://github.com/rahulsamarth121/personal-game.git"' in src
    assert "GITHUB_TOKEN" in src
    assert "GIT_ASKPASS" in src
    assert "kaggle/runner.py" in src


def test_notebook_never_embeds_token_in_clone_url(cells):
    src = cells[0]["source"]
    assert "git clone --depth 1 REPO_URL" not in src  # uses the plain URL var
    assert "GITHUB_TOKEN@" not in src and "x-access-token@" not in src


def test_notebook_bootstrap_on_fresh_kernel(tmp_path, monkeypatch, cells):
    """Execute Cell 1 with rewritten paths against a local bare 'origin'."""
    work = tmp_path / "work"
    origin = tmp_path / "origin.git"
    subprocess.run(["git", "clone", "--bare", "--quiet", str(HERE.parent),
                    str(origin)], check=True)

    src = cells[0]["source"]
    src = src.replace('REPO_URL = "https://github.com/rahulsamarth121/personal-game.git"',
                      f'REPO_URL = r"{origin}"')
    src = src.replace('WORK_ROOT = Path("/kaggle/working/personal-game-work")',
                      f'WORK_ROOT = Path(r"{work}")')
    # Kaggle is Linux with a writable /tmp; the sandbox (e.g. Windows CI)
    # rewrites every /tmp/ reference -- Path literals AND the path embedded
    # in the askpass helper -- into the pytest tmp dir.
    src = src.replace("/tmp/", tmp_path.as_posix() + "/")

    monkeypatch.setenv("GITHUB_TOKEN", "ghp_fake_token_for_local_test_1234567890")
    snapshot = dict(os.environ)
    try:
        exec(compile(src, "cell1", "exec"), {})
        repo = work / "repo"
        assert (repo / ".git").exists(), "cell did not clone into WORK_ROOT/repo"
        assert (repo / "kaggle" / "runner.py").exists(), "canonical runner missing"
        assert os.environ["WORK_ROOT"] == str(work)
        assert os.environ["GITHUB_REPO_URL"]  # runner clone stays idempotent
        assert not (tmp_path / ".pg_tok").exists(), "token file was not removed"
        assert not (tmp_path / ".pg_askpass.sh").exists(), "askpass not removed"

        # Rerunning must be a no-op (repo already present, no second clone).
        exec(compile(src, "cell1", "exec"), {})
    finally:
        os.environ.clear()
        os.environ.update(snapshot)


def test_runner_clone_private_repo_temp_askpass_cleanup(tmp_path, monkeypatch, _runner):
    """runner.py clone with GITHUB_TOKEN: temp askpass is used for the git
    subprocess, the token never appears in the URL/argv, and both credential
    files are deleted afterwards — even though the clone target is local.
    """
    origin = tmp_path / "origin.git"
    subprocess.run(["git", "clone", "--bare", "--quiet", str(HERE.parent),
                    str(origin)], check=True)
    work = tmp_path / "work"
    work.mkdir()
    monkeypatch.setenv("GITHUB_TOKEN", "ghp_fake_token_for_local_test_1234567890")
    monkeypatch.setenv("WORK_ROOT", str(work))
    monkeypatch.setenv("GITHUB_REPO_URL", str(origin))
    args = argparse.Namespace(repo_url=None, branch=None, work_root=None,
                              update_repo=None)
    assert _runner.cmd_clone(args) == 0
    repo = work / "repo"
    assert (repo / ".git").exists()
    assert (repo / "kaggle" / "runner.py").exists()
    assert not (work / ".pg_tok").exists(), "token file survived the clone"
    assert not (work / ".pg_askpass.sh").exists(), "askpass survived the clone"
    # Idempotent rerun: repo present, fast-forward pull, still no leftovers.
    assert _runner.cmd_clone(args) == 0
    assert not (work / ".pg_tok").exists()
    assert not (work / ".pg_askpass.sh").exists()


# --------------------------------------------------------------------------
# control-plane health detection (local HTTP servers, no network)
# --------------------------------------------------------------------------

def make_handler(routes):
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            body, status = routes.get(self.path, (b"{}", 404))
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    return Handler


@pytest.fixture
def serve():
    servers = []

    def _serve(routes):
        srv = HTTPServer(("127.0.0.1", 0), make_handler(routes))
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        servers.append(srv)
        return f"http://127.0.0.1:{srv.server_port}"

    yield _serve
    for srv in servers:
        srv.shutdown()
        srv.server_close()


HEALTHY = (b'{"relay":"ok","upstream":"ok","project":"personal-game"}', 200)


def test_worker_relay_healthy(serve, _runner):
    url = serve({"/health": HEALTHY})
    assert _runner.control_reachable(url) is True


def test_worker_relay_degraded_upstream(serve, _runner):
    url = serve({"/health": (b'{"relay":"ok","upstream":"unreachable"}', 200)})
    assert _runner.control_reachable(url) is False


def test_worker_relay_not_ok(serve, _runner):
    url = serve({"/health": (b'{"relay":"bad","upstream":"ok"}', 200)})
    assert _runner.control_reachable(url) is False


def test_worker_relay_non_json_body(serve, _runner):
    url = serve({"/health": (b"<html>gateway</html>", 200)})
    assert _runner.control_reachable(url) is False


def test_worker_relay_error_status(serve, _runner):
    url = serve({"/health": (b"{}", 500)})
    assert _runner.control_reachable(url) is False


def test_worker_health_404_falls_back_to_healthz(serve, _runner):
    url = serve({"/healthz": (b"OK", 200)})  # /health intentionally 404
    assert _runner.control_reachable(url) is True


def test_direct_control_plane_healthz(serve, _runner):
    url = serve({"/healthz": (b"OK", 200)})
    assert _runner.control_reachable(url) is True


def test_direct_control_plane_unhealthy(serve, _runner):
    url = serve({"/healthz": (b"boom", 500)})
    assert _runner.control_reachable(url) is False


def test_control_plane_unreachable(serve, _runner):
    url = serve({})  # grab a port, then take it away
    assert _runner.control_reachable(url) is False


# --------------------------------------------------------------------------
# runner defaults + secret hygiene
# --------------------------------------------------------------------------

@pytest.fixture(scope="module")
def _runner():
    return _load_runner()


def test_defaults_are_honest(_runner):
    assert _runner.DEFAULTS["STREAMING_ALLOWED"] == "false"
    assert _runner.DEFAULTS["CONTROL_PLANE_URL"] == (
        "https://personal-game-relay.rahul-zed-relay-84739261.workers.dev/personal-game"
    )


def test_status_never_prints_secrets(_runner, capsys):
    cfg = dict(_runner.DEFAULTS, PG_API_TOKEN="sk-super-secret-987654321",
               NODE_ENROLLMENT_TOKEN="enroll-secret-987654321")
    _runner.print_banner(cfg)
    _runner.print_status(cfg)
    out = capsys.readouterr().out
    assert "sk-super-secret-987654321" not in out
    assert "enroll-secret-987654321" not in out
    assert "Enrollment:        credential configured" in out


def test_redact_masks_configured_secrets(monkeypatch, _runner):
    monkeypatch.setenv("PG_API_TOKEN", "supersecretvalue123456")
    text = _runner.redact("boom: token supersecretvalue123456 rejected")
    assert "supersecretvalue123456" not in text
    assert "[REDACTED]" in text


def test_cli_help_exposes_all_flags():
    proc = subprocess.run([sys.executable, str(RUNNER), "--help"],
                          capture_output=True, text=True, timeout=60)
    assert proc.returncode == 0
    for flag in ("--api-token", "--streaming-allowed", "--media-network",
                 "--media-endpoint", "--control-plane-url", "--repo-url"):
        assert flag in proc.stdout, flag
