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
import runpy
import shutil
import subprocess
import sys
import threading
import types
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


def test_notebook_runner_cells_are_subprocess_calls(cells):
    """Regression: runner commands must run as REAL SUBPROCESSes.

    In-process execution (%run or importlib) makes argparse consume the
    kernel's own sys.argv (kernel-*.json connection file) — the exact
    colab failure this guard prevents. Every runner cell must therefore
    call `subprocess.run([sys.executable, runner, cmd], check=True)`.
    """
    for cell in cells[1:]:
        assert "%run" not in cell["source"], "notebook must not use %run"
        assert "!python" not in cell["source"], "use subprocess.run, not shell magic"
    for cell in cells[2:]:
        assert "subprocess.run([sys.executable," in cell["source"]
        assert "check=True" in cell["source"]


def test_main_uses_only_passed_argv(monkeypatch, tmp_path, _runner, capsys):
    """Programmatic main([...]) parses ONLY its argument list — never the
    kernel's sys.argv — so tests can import and call the runner safely.
    """
    monkeypatch.setenv("WORK_ROOT", str(tmp_path))
    monkeypatch.setattr(sys, "argv",
                        ["colab_kernel_launcher.py", "-f", "/tmp/kernel-xyz.json"])
    rc = _runner.main(["cleanup"])  # explicit argv: kernel argv must be ignored
    assert rc == 0
    out = capsys.readouterr().out
    assert "kernel-xyz.json" not in out
    assert "invalid choice" not in out


# --------------------------------------------------------------------------
# notebook / CLI entry-point contract (resolve_runner_argv + __main__ path)
# --------------------------------------------------------------------------

KERNEL_ARGV = ["ipykernel_launcher.py", "-f",
               "/root/.local/share/jupyter/runtime/kernel-test.json"]


def test_resolve_runner_argv_contract(monkeypatch, _runner):
    # Explicit argv always wins verbatim — notebook detection irrelevant.
    assert _runner.resolve_runner_argv(["run"]) == (["run"], False)
    # Plain Python (no IPython): real process argv, unchanged semantics.
    monkeypatch.setattr(sys, "argv", ["runner.py", "diagnostics"])
    assert _runner.resolve_runner_argv() == (["diagnostics"], False)
    # Interactive kernel: kernel argv never becomes runner input.
    fake = types.ModuleType("IPython")

    class _FakeKernelShell:  # NOT TerminalInteractiveShell
        pass

    fake.get_ipython = lambda: _FakeKernelShell()
    monkeypatch.setitem(sys.modules, "IPython", fake)
    monkeypatch.setattr(sys, "argv", KERNEL_ARGV)
    resolved, notebook = _runner.resolve_runner_argv()
    assert notebook is True
    assert resolved == [_runner.NOTEBOOK_DEFAULT_COMMAND]
    assert "kernel-test.json" not in resolved


def test_ipython_poisoned_argv_enters_notebook_mode(monkeypatch, tmp_path, _runner, capsys):
    """The exact Kaggle failure: main() with a kernel argv. The kernel JSON
    must never reach argparse as the runner command; the safe default runs.
    """
    fake = types.ModuleType("IPython")

    class _FakeKernelShell:
        pass

    fake.get_ipython = lambda: _FakeKernelShell()
    monkeypatch.setitem(sys.modules, "IPython", fake)
    monkeypatch.setattr(sys, "argv", KERNEL_ARGV)
    monkeypatch.setenv("WORK_ROOT", str(tmp_path))

    calls = []
    monkeypatch.setattr(_runner, "cmd_diagnostics",
                        lambda args: calls.append(getattr(args, "command", None)) or 0)
    rc = _runner.main()  # argv=None — the pasted-cell scenario
    out = capsys.readouterr().out
    assert rc == 0
    assert calls == ["diagnostics"]
    assert "NOTEBOOK MODE" in out
    assert "invalid choice" not in out
    assert "kernel-test.json" not in out


def test_dunder_main_runpy_with_fake_ipython_kernel(monkeypatch, tmp_path, _runner):
    """End-to-end through the REAL entry path: run the actual runner source
    with run_name='__main__' (exactly what pasting into a cell does) while a
    fake IPython kernel supplies the poisoned argv. The old implementation
    raised SystemExit(2) with 'invalid choice: kernel-*.json' here.
    """
    fake = types.ModuleType("IPython")

    class _FakeKernelShell:
        pass

    fake.get_ipython = lambda: _FakeKernelShell()
    monkeypatch.setitem(sys.modules, "IPython", fake)
    monkeypatch.setattr(sys, "argv", KERNEL_ARGV)
    monkeypatch.setenv("WORK_ROOT", str(tmp_path))

    calls = []
    real_source = RUNNER.read_text(encoding="utf-8")
    # Patch the stub in the source string (runpy compiles the actual file;
    # module-level monkeypatch would not survive the fresh namespace). The
    # stub records the command it received into a temp file.
    assert "def cmd_diagnostics(" in real_source
    calls_file = tmp_path / "calls.txt"
    patched = real_source.replace(
        "def cmd_diagnostics(args: argparse.Namespace) -> int:",
        "def cmd_diagnostics(args: argparse.Namespace) -> int:\n"
        "    open(r" + repr(str(calls_file)) + ", 'a').write(getattr(args, 'command', '') + '\\n')\n"
        "    return 0", 1)
    src_file = tmp_path / "runner_under_test.py"
    src_file.write_text(patched, encoding="utf-8")
    # runpy must not raise SystemExit in notebook mode (success reported).
    runpy.run_path(str(src_file), run_name="__main__")
    assert calls_file.read_text().splitlines() == ["diagnostics"]


def test_real_cli_subprocess_regression(tmp_path):
    """Normal CLI behavior via real subprocesses (Phase 4 + 6)."""
    # Plain CLI: unknown args must STILL be rejected by argparse (exit 2) —
    # proves the fix introduced no parse_known_args-style weakening.
    proc = subprocess.run([sys.executable, str(RUNNER), "-f", "/tmp/kernel-fake.json"],
                          capture_output=True, text=True, timeout=120, cwd=str(tmp_path))
    combined = proc.stdout + proc.stderr
    assert proc.returncode == 2
    assert "invalid choice" in combined

    # THE Kaggle scenario, end-to-end through the real __main__ path in a
    # real subprocess: an IPython-kernel shim on PYTHONPATH plus the poisoned
    # kernel argv. The runner must enter notebook mode and run the safe
    # default — never 'invalid choice: kernel-*.json'.
    shim = tmp_path / "ipysim"
    shim.mkdir()
    (shim / "IPython.py").write_text(
        "class _FakeKernelShell:\n    pass\n\n\n"
        "def get_ipython():\n    return _FakeKernelShell()\n", encoding="utf-8")
    env = dict(os.environ, PYTHONPATH=str(shim) + os.pathsep + os.environ.get("PYTHONPATH", ""))
    proc = subprocess.run([sys.executable, str(RUNNER), "-f",
                           "/root/.local/share/jupyter/runtime/kernel-subproc.json"],
                          capture_output=True, text=True, timeout=300,
                          cwd=str(tmp_path), env=env)
    combined = proc.stdout + proc.stderr
    assert proc.returncode == 0, combined[-800:]
    assert "NOTEBOOK MODE" in proc.stdout
    assert "invalid choice" not in combined
    assert "kernel-subproc.json" not in combined

    # --help for the parser and every command.
    proc = subprocess.run([sys.executable, str(RUNNER), "--help"],
                          capture_output=True, text=True, timeout=120)
    assert proc.returncode == 0 and "clone" in proc.stdout and "diagnostics" in proc.stdout
    for command in ("clone", "diagnostics", "setup", "run", "cleanup", "all"):
        proc = subprocess.run([sys.executable, str(RUNNER), command, "--help"],
                              capture_output=True, text=True, timeout=120)
        assert proc.returncode == 0, command
        assert command in proc.stdout


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


# --------------------------------------------------------------------------
# pipeline: clone -> discovery -> build -> caps (Phases 3/4/5 of the audit)
# --------------------------------------------------------------------------

def test_clone_failure_is_clear_and_offline(tmp_path, monkeypatch):
    """A bad repo URL fails fast with a useful error — no partial state,
    no credential files left behind, no hang.
    """
    env = dict(os.environ,
               WORK_ROOT=str(tmp_path),
               GITHUB_REPO_URL="file:///nonexistent/pg-does-not-exist.git")
    env.pop("GITHUB_TOKEN", None)
    proc = subprocess.run([sys.executable, str(RUNNER), "clone"], env=env,
                          capture_output=True, text=True, timeout=120)
    assert proc.returncode == 1
    assert "Clone failed" in (proc.stdout + proc.stderr)
    assert not (tmp_path / ".pg_tok").exists()
    assert not (tmp_path / ".pg_askpass.sh").exists()


def test_pipeline_clone_discover_build_caps(tmp_path):
    """The exact Kaggle pipeline against a local bare origin (no network):
    clone -> find_repo -> go build ./cmd/agent -> `agent caps` dump via
    diagnostics. Requires the Go toolchain (present in dev; skipped here if
    absent — Kaggle installs its own).
    """
    if shutil.which("go") is None:
        pytest.skip("Go toolchain not available on this machine")
    origin = tmp_path / "origin.git"
    subprocess.run(["git", "clone", "--bare", "--quiet", str(HERE.parent),
                    str(origin)], check=True)
    # Work root holds the built agent binary (~20 MB); honor PG_TEST_WORKROOT
    # so constrained-CI machines can point it at a roomy volume.
    work = Path(os.environ["PG_TEST_WORKROOT"]) if os.environ.get("PG_TEST_WORKROOT") \
        else tmp_path / "work"
    work.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ,
               WORK_ROOT=str(work),
               GITHUB_REPO_URL=str(origin),
               # deliberately unreachable: diagnostics must still exit 0 and
               # report honestly (health failure is a warning, not a crash)
               CONTROL_PLANE_URL="http://127.0.0.1:9",
               STREAMING_ALLOWED="false")
    env.pop("GITHUB_TOKEN", None)
    # Keep the Go build cache on a volume with real free space; the repo's
    # dev machine runs with a nearly-full system drive (documented env quirk).
    if os.environ.get("PG_TEST_GOCACHE"):
        cache_dir = os.environ["PG_TEST_GOCACHE"]
        os.makedirs(cache_dir, exist_ok=True)
        env["GOCACHE"] = cache_dir
    clone = subprocess.run([sys.executable, str(RUNNER), "clone"], env=env,
                           capture_output=True, text=True, timeout=300)
    assert clone.returncode == 0, clone.stderr[-500:]
    diag = subprocess.run([sys.executable, str(RUNNER), "diagnostics"], env=env,
                          capture_output=True, text=True, timeout=600)
    out = diag.stdout + diag.stderr
    assert diag.returncode == 0, out[-800:]
    assert "Agent caps:" in out, "diagnostics did not dump real agent capabilities"
    assert "os=windows" in out or "os=linux" in out
    # The built agent binary lives in the work root, next to the clone.
    assert (work / "bin" / "agent").exists() or (work / "bin" / "agent.exe").exists()


def test_notebook_default_repo_is_project_public_url(monkeypatch, _runner):
    """Paste-one-cell mode points GITHUB_REPO_URL at the project's public
    repo (via setdefault, so explicit operator config still wins)."""
    fake = types.ModuleType("IPython")

    class _FakeKernelShell:
        pass

    fake.get_ipython = lambda: _FakeKernelShell()
    monkeypatch.setitem(sys.modules, "IPython", fake)
    monkeypatch.setattr(sys, "argv", KERNEL_ARGV)
    monkeypatch.delenv("GITHUB_REPO_URL", raising=False)
    monkeypatch.setenv("WORK_ROOT", str(tmp_dir_factory()))
    # main() runs diagnostics; the repo will not exist -> harmless warning.
    rc = _runner.main()
    assert rc == 0
    assert os.environ["GITHUB_REPO_URL"] == _runner.DEFAULT_REPO_URL


def tmp_dir_factory():
    import tempfile
    return tempfile.mkdtemp(prefix="pg_nb_default_")
