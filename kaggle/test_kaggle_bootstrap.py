"""Static tests for the Kaggle notebook bootstrap + runner health logic.

No external network required: control-plane health checks run against
local HTTP servers, and the notebook Cell 1 bootstrap is executed in a
sandbox against a local bare clone of this repository. Live checks against
the deployed Worker are run separately (see kaggle/README.md).
"""

from __future__ import annotations

import argparse
import hashlib
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
    # Kaggle is Linux with a writable /tmp; redirect ONLY the cell's own
    # credential-helper files (the two Path literals and the path embedded
    # in the askpass script) into the pytest tmp dir. This runs FIRST and
    # targets the exact '/tmp/.pg_' prefix, so it can never touch the
    # origin/work paths inserted below, however deeply tmp_path is nested
    # (on Linux tmp_path itself lives under /tmp/).
    helper_prefix = "/tmp/.pg_"
    assert src.count(helper_prefix) == 3, "cell 1 credential-helper literals changed"
    src = src.replace(helper_prefix, tmp_path.as_posix() + "/.pg_")
    repo_literal = 'REPO_URL = "https://github.com/rahulsamarth121/personal-game.git"'
    work_literal = 'WORK_ROOT = Path("/kaggle/working/personal-game-work")'
    assert repo_literal in src and work_literal in src
    src = src.replace(repo_literal, f'REPO_URL = r"{origin}"')
    src = src.replace(work_literal, f'WORK_ROOT = Path(r"{work}")')

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
    if os.environ.get("PG_TEST_GOTMPDIR"):
        gotmp = os.environ["PG_TEST_GOTMPDIR"]
        os.makedirs(gotmp, exist_ok=True)
        env["GOTMPDIR"] = gotmp
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

# --------------------------------------------------------------------------
# missing-Go path: pinned WORK_ROOT toolchain bootstrap (Kaggle ships no Go)
# --------------------------------------------------------------------------

# A minimal *fake* toolchain: bin/go that prints the env it was run with.
# Used to prove PATH-prepending and toolchain reuse without any network.
FAKE_GO_SCRIPT = """#!/bin/sh
echo fake-go-1.24.11 $PG_BOOTSTRAP_PROOF
echo PATH=$PATH
echo GOROOT=$GOROOT
"""


def _fake_toolchain(root, version='fake-go-1.24.11'):
    """Write a structurally valid fake Go root (bin/go[.exe] + VERSION)."""
    bin_dir = root / 'bin'
    bin_dir.mkdir(parents=True, exist_ok=True)
    for name in ('go', 'go.exe'):  # go.exe: Windows shutil.which finds it too
        go = bin_dir / name
        go.write_text(FAKE_GO_SCRIPT.replace('fake-go-1.24.11', version))
        go.chmod(0o755)
    (root / 'VERSION').write_text(version + chr(10))
    return root


def _tarball_bytes(root):
    """Deterministic gzipped tar of a toolchain dir, mirroring the official
    dl.google.com layout: members live under a top-level 'go/' directory."""
    import io
    import tarfile as _tarfile
    buf = io.BytesIO()
    with _tarfile.open(fileobj=buf, mode='w:gz', format=_tarfile.GNU_FORMAT) as tf:
        for path in sorted(root.rglob('*')):
            if path.is_file():
                tf.add(str(path), arcname='go/' + path.relative_to(root).as_posix())
    return buf.getvalue()


def _no_go_env(monkeypatch):
    """Simulate 'go is missing from PATH' (subprocess env only)."""
    monkeypatch.setenv('PATH', os.defpath)


def _hidden_go_cfg(tmp_path, **extra):
    """Config for ensure_go_toolchain with Go hidden from PATH."""
    cfg = dict(_load_runner().DEFAULTS,
               WORK_ROOT=str(tmp_path / 'work'),
               GOROOT_URL='', GOROOT_SHA256='')
    cfg.update(extra)
    return cfg


def _fake_repo(repo):
    """A repo find_repo() accepts (go.mod + cmd/agent), agent source optional."""
    (repo / 'cmd' / 'agent').mkdir(parents=True, exist_ok=True)
    (repo / 'go.mod').write_text(
        'module github.com/personal-game/personal-game' + chr(10) * 2 + 'go 1.24' + chr(10))
    (repo / 'cmd' / 'agent' / 'main.go').write_text(
        'package main' + chr(10) * 2 + 'func main() {}' + chr(10))
    return repo


def test_setup_fails_honestly_without_go_and_without_bootstrap(tmp_path, monkeypatch, _runner, caplog):
    """GOROOT_SHA256=never (bootstrap disabled) + no Go anywhere: setup must
    fail with exit 1 and the honest error -- never fake success."""
    _no_go_env(monkeypatch)
    monkeypatch.chdir(tmp_path)
    rc = _runner.main(['setup', '--work-root', str(tmp_path / 'work'),
                       '--goroot-sha256', 'never'])
    assert rc == 1
    # The runner logs (not prints) the honest failure -- check the log capture.
    assert 'Go toolchain missing' in caplog.text
    assert 'bootstrap unavailable/disabled' in caplog.text


def test_diagnostics_swallows_bootstrap_failure_and_leaves_no_toolchain(tmp_path, monkeypatch, _runner, caplog):
    """diagnostics with a repo but an unreachable bootstrap source: exit 0,
    honest warning, and NO toolchain/binary ever left in the work root."""
    _no_go_env(monkeypatch)
    monkeypatch.chdir(tmp_path)
    work = tmp_path / 'work'
    _fake_repo(work / 'repo')
    rc = _runner.main(['diagnostics', '--work-root', str(work),
                       '--control-plane-url', 'http://127.0.0.1:9',
                       '--goroot-url', 'http://127.0.0.1:9/go.tgz'])
    assert rc == 0
    assert 'Go bootstrap failed' in caplog.text
    assert not (work / 'go').exists()
    assert not (work / 'bin').exists()


def test_ensure_go_uses_system_go_when_present(tmp_path, monkeypatch, _runner):
    """Go already reachable in the given env: returned unchanged, no
    WORK_ROOT toolchain consulted or created."""
    fake_bin = tmp_path / 'sys-go-bin'
    _fake_toolchain(fake_bin)
    env = {'PATH': str(fake_bin / 'bin')}  # PATH entries hold the executables
    cfg = _hidden_go_cfg(tmp_path)
    got = _runner.ensure_go_toolchain(cfg, env)
    assert got == env
    assert not (Path(cfg['WORK_ROOT']) / 'go').exists()


def test_ensure_go_reuses_work_root_toolchain_without_download(tmp_path, monkeypatch, _runner):
    """Second run: the cached WORK_ROOT/go toolchain is prepended to PATH --
    no download (the unreachable URL proves no second fetch happens)."""
    _no_go_env(monkeypatch)
    cfg = _hidden_go_cfg(tmp_path, GOROOT_URL='http://127.0.0.1:9/go.tgz')
    root = _fake_toolchain(Path(cfg['WORK_ROOT']) / 'go')
    got = _runner.ensure_go_toolchain(
        cfg, {'PATH': os.defpath, 'PG_BOOTSTRAP_PROOF': 'reuse'})
    assert got is not None
    assert got['PATH'].startswith(str(root / 'bin'))
    assert got['GOROOT'] == str(root)
    assert got['GOTOOLCHAIN'] == 'local'
    if os.name != 'nt':  # exec proof is POSIX-only (shebang scripts)
        proc = subprocess.run([str(root / 'bin' / 'go')], capture_output=True,
                              text=True, env=got, timeout=30)
        assert 'fake-go-1.24.11 reuse' in proc.stdout
        assert ('PATH=' + str(root / 'bin')) in proc.stdout
        assert ('GOROOT=' + str(root)) in proc.stdout


def test_ensure_go_prepend_never_mutates_process_env(tmp_path, monkeypatch, _runner):
    """The returned env is a copy: os.environ must stay untouched."""
    _no_go_env(monkeypatch)
    cfg = _hidden_go_cfg(tmp_path)
    _fake_toolchain(Path(cfg['WORK_ROOT']) / 'go')
    before = dict(os.environ)
    got = _runner.ensure_go_toolchain(cfg, {'PATH': os.defpath})
    assert got['PATH'] != os.environ.get('PATH')
    assert os.environ == before
    assert 'GOROOT' not in os.environ


def _serve_bytes():
    """Start a local HTTP server serving a dict of path -> bytes."""
    payloads = {}
    hits = {}

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            hits[self.path] = hits.get(self.path, 0) + 1
            body = payloads.get(self.path)
            if body is None:
                self.send_response(404)
                self.end_headers()
                return
            self.send_response(200)
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a):
            pass

    server = HTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, payloads, hits


def test_ensure_go_download_success_verifies_and_installs(tmp_path, monkeypatch, _runner):
    """Full bootstrap: verified tarball downloaded from a *local* HTTP server,
    extracted into WORK_ROOT/go, PATH-prepended, structural check passed,
    and no tarball/staging residue left behind."""
    src = _fake_toolchain(tmp_path / 'src' / 'go')
    data = _tarball_bytes(src)
    sha = hashlib.sha256(data).hexdigest()
    server, payloads, hits = _serve_bytes()
    try:
        payloads['/go/go1.24.11.linux-amd64.tar.gz'] = data
        _no_go_env(monkeypatch)
        cfg = _hidden_go_cfg(
            tmp_path,
            GOROOT_URL='http://127.0.0.1:%d/go/go1.24.11.linux-amd64.tar.gz' % server.server_port,
            GOROOT_SHA256=sha)
        got = _runner.ensure_go_toolchain(cfg, {'PATH': os.defpath})
        assert got is not None
        root = Path(cfg['WORK_ROOT']) / 'go'
        assert (root / 'bin' / 'go').exists()
        assert (root / 'VERSION').read_text().startswith('fake-go-1.24.11')
        assert got['PATH'].startswith(str(root / 'bin'))
        work = Path(cfg['WORK_ROOT'])
        assert not (work / 'go-toolchain.tar.gz').exists()
        assert not (work / 'go-extract-staging').exists()
        assert list(hits) == ['/go/go1.24.11.linux-amd64.tar.gz']
    finally:
        server.shutdown()


def test_ensure_go_download_checksum_mismatch_refuses(tmp_path, monkeypatch, _runner):
    """Wrong sha256: nothing is extracted, the temp file is deleted, and the
    caller sees None (honest failure)."""
    src = _fake_toolchain(tmp_path / 'src' / 'go')
    data = _tarball_bytes(src)
    server, payloads, hits = _serve_bytes()
    try:
        payloads['/go.tgz'] = data
        _no_go_env(monkeypatch)
        cfg = _hidden_go_cfg(
            tmp_path,
            GOROOT_URL='http://127.0.0.1:%d/go.tgz' % server.server_port,
            GOROOT_SHA256='0' * 64)
        assert _runner.ensure_go_toolchain(cfg, {'PATH': os.defpath}) is None
        assert hits.get('/go.tgz') == 1
        work = Path(cfg['WORK_ROOT'])
        assert not (work / 'go').exists()
        assert not (work / 'go-extract-staging').exists()
        assert not (work / 'go-toolchain.tar.gz').exists()
    finally:
        server.shutdown()


def test_ensure_go_corrupted_toolchain_archive_is_rejected(tmp_path, monkeypatch, _runner):
    """A structurally broken toolchain (bin/go missing) inside a checksummed
    archive is refused: no WORK_ROOT/go is ever installed."""
    src = tmp_path / 'src' / 'go'
    src.mkdir(parents=True)
    (src / 'VERSION').write_text('fake-go-1.24.11' + chr(10))
    data = _tarball_bytes(src)
    server, payloads, hits = _serve_bytes()
    try:
        payloads['/go.tgz'] = data
        _no_go_env(monkeypatch)
        cfg = _hidden_go_cfg(
            tmp_path,
            GOROOT_URL='http://127.0.0.1:%d/go.tgz' % server.server_port,
            GOROOT_SHA256=hashlib.sha256(data).hexdigest())
        assert _runner.ensure_go_toolchain(cfg, {'PATH': os.defpath}) is None
        assert hits.get('/go.tgz') == 1
        work = Path(cfg['WORK_ROOT'])
        assert not (work / 'go').exists()
        assert not (work / 'go-extract-staging').exists()
    finally:
        server.shutdown()


def _evil_tarball(kind, outside):
    """Checksum-valid gzipped tar carrying one hostile member (plus a valid
    go/bin/go + go/VERSION so only the hostile member can cause rejection)."""
    import io
    import tarfile as _tarfile
    buf = io.BytesIO()
    with _tarfile.open(fileobj=buf, mode='w:gz', format=_tarfile.GNU_FORMAT) as tf:
        def add_file(name, data=b'x'):
            info = _tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = 0o755
            tf.addfile(info, io.BytesIO(data))
        add_file('go/VERSION', b'fake-go-1.24.11')
        add_file('go/bin/go', b'#!/bin/sh')
        if kind == 'dotdot':
            add_file('../evil.txt', b'pwned')
        elif kind == 'nested-dotdot':
            add_file('go/../../evil.txt', b'pwned')
        elif kind == 'absolute':
            add_file(outside.as_posix() + '/evil.txt', b'pwned')
        elif kind in ('symlink-escape', 'symlink-absolute', 'hardlink-escape'):
            info = _tarfile.TarInfo('go/escape')
            info.type = _tarfile.SYMTYPE if kind != 'hardlink-escape' else _tarfile.LNKTYPE
            info.linkname = {'symlink-escape': '../../outside',
                             'symlink-absolute': outside.as_posix(),
                             'hardlink-escape': '../../outside/evil.txt'}[kind]
            tf.addfile(info)
        elif kind == 'device':
            info = _tarfile.TarInfo('go/dev')
            info.type = _tarfile.CHRTYPE
            tf.addfile(info)
        else:
            raise AssertionError(kind)
    return buf.getvalue()


@pytest.mark.parametrize('kind', ['dotdot', 'nested-dotdot', 'absolute',
                                  'symlink-escape', 'symlink-absolute',
                                  'hardlink-escape', 'device'])
def test_download_and_extract_go_rejects_malicious_archive(tmp_path, _runner, kind):
    """A hostile archive that PASSES the sha256 check is still refused by
    _download_and_extract_go(): nothing is written outside staging, no
    toolchain is installed, and no staging/tarball residue remains."""
    outside = tmp_path / 'outside'
    outside.mkdir()
    data = _evil_tarball(kind, outside)
    server, payloads, hits = _serve_bytes()
    try:
        payloads['/go.tgz'] = data
        cfg = _hidden_go_cfg(tmp_path)
        url = 'http://127.0.0.1:%d/go.tgz' % server.server_port
        with pytest.raises(RuntimeError, match='extraction failed'):
            _runner._download_and_extract_go(cfg, url, hashlib.sha256(data).hexdigest())
        assert hits.get('/go.tgz') == 1  # it really downloaded and got past the checksum
        work = Path(cfg['WORK_ROOT'])
        assert not (work / 'go').exists()
        assert not (work / 'go-extract-staging').exists()
        assert not (work / 'go-toolchain.tar.gz').exists()
        assert not (work / 'evil.txt').exists()
        assert not (tmp_path / 'evil.txt').exists()
        assert list(outside.iterdir()) == []
    finally:
        server.shutdown()


def test_malicious_archive_makes_ensure_go_toolchain_fail_honestly(tmp_path, monkeypatch, _runner):
    """End to end through ensure_go_toolchain: hostile archive -> None."""
    data = _evil_tarball('dotdot', tmp_path / 'outside')
    server, payloads, hits = _serve_bytes()
    try:
        payloads['/go.tgz'] = data
        _no_go_env(monkeypatch)
        cfg = _hidden_go_cfg(
            tmp_path,
            GOROOT_URL='http://127.0.0.1:%d/go.tgz' % server.server_port,
            GOROOT_SHA256=hashlib.sha256(data).hexdigest())
        assert _runner.ensure_go_toolchain(cfg, {'PATH': os.defpath}) is None
        assert not (Path(cfg['WORK_ROOT']) / 'go').exists()
        assert not (tmp_path / 'work' / 'evil.txt').exists()
    finally:
        server.shutdown()


@pytest.mark.skipif(os.name == 'nt', reason='symlink extraction needs POSIX')
def test_download_and_extract_go_allows_safe_internal_symlink(tmp_path, _runner):
    """No over-rejection: a symlink that stays inside the toolchain is fine."""
    import io
    import tarfile as _tarfile
    buf = io.BytesIO()
    with _tarfile.open(fileobj=buf, mode='w:gz', format=_tarfile.GNU_FORMAT) as tf:
        for name, body in (('go/VERSION', b'fake-go-1.24.11'), ('go/bin/go', b'#!/bin/sh')):
            info = _tarfile.TarInfo(name)
            info.size = len(body)
            info.mode = 0o755
            tf.addfile(info, io.BytesIO(body))
        link = _tarfile.TarInfo('go/bin/go-alias')
        link.type = _tarfile.SYMTYPE
        link.linkname = 'go'
        tf.addfile(link)
    data = buf.getvalue()
    server, payloads, hits = _serve_bytes()
    try:
        payloads['/go.tgz'] = data
        cfg = _hidden_go_cfg(tmp_path)
        root = _runner._download_and_extract_go(
            cfg, 'http://127.0.0.1:%d/go.tgz' % server.server_port,
            hashlib.sha256(data).hexdigest())
        assert (root / 'bin' / 'go-alias').is_symlink()
    finally:
        server.shutdown()


def test_ensure_go_idempotent_rerun_replaces_not_duplicates(tmp_path, monkeypatch, _runner):
    """Reruns are idempotent: a valid WORK_ROOT/go is REUSED (no second
    download), and a forced re-extract REPLACES it -- never two toolchains,
    never staging/tarball residue."""
    old = _tarball_bytes(_fake_toolchain(tmp_path / 'src1' / 'go', version='fake-go-old'))
    new = _tarball_bytes(_fake_toolchain(tmp_path / 'src2' / 'go', version='fake-go-new'))
    server, payloads, hits = _serve_bytes()
    try:
        payloads['/go-old.tgz'] = old
        payloads['/go-new.tgz'] = new
        _no_go_env(monkeypatch)
        cfg = _hidden_go_cfg(
            tmp_path,
            GOROOT_URL='http://127.0.0.1:%d/go-old.tgz' % server.server_port,
            GOROOT_SHA256=hashlib.sha256(old).hexdigest())
        work = Path(cfg['WORK_ROOT'])
        root = work / 'go'
        # 1. First run downloads + installs.
        assert _runner.ensure_go_toolchain(cfg, {'PATH': os.defpath}) is not None
        assert (root / 'VERSION').read_text().startswith('fake-go-old')
        # 2. Rerun with an UNREACHABLE url: reuse wins, no download attempted.
        cached = _hidden_go_cfg(tmp_path, GOROOT_URL='http://127.0.0.1:9/go.tgz')
        assert _runner.ensure_go_toolchain(cached, {'PATH': os.defpath}) is not None
        assert (root / 'VERSION').read_text().startswith('fake-go-old')
        assert hits.get('/go-old.tgz') == 1
        # 3. A new verified extraction REPLACES the toolchain in place.
        cfg2 = _hidden_go_cfg(
            tmp_path,
            GOROOT_URL='http://127.0.0.1:%d/go-new.tgz' % server.server_port,
            GOROOT_SHA256=hashlib.sha256(new).hexdigest())
        _runner._download_and_extract_go(cfg2, cfg2['GOROOT_URL'],
                                         hashlib.sha256(new).hexdigest())
        assert (root / 'VERSION').read_text().startswith('fake-go-new')
        assert sorted(pp.name for pp in work.glob('go*')) == ['go']
        assert not (work / 'go-toolchain.tar.gz').exists()
        assert not (work / 'go-extract-staging').exists()
    finally:
        server.shutdown()


def test_pinned_toolchain_constants_are_official_and_match(_runner):
    """The pin is the smallest release satisfying go.mod (go 1.24) and the
    default URL must embed that version -- regresses silent pin drift."""
    assert _runner.PINNED_GO_VERSION.startswith('1.24.')
    # Official sha256 for the pinned linux-amd64 tarball (go.dev/dl).
    assert _runner.PINNED_GO_SHA256_LINUX_AMD64 == (
        'bceca00afaac856bc48b4cc33db7cd9eb383c81811379faed3bdbc80edb0af65')
    assert len(_runner.PINNED_GO_SHA256_LINUX_AMD64) == 64
    int(_runner.PINNED_GO_SHA256_LINUX_AMD64, 16)
    go_mod = (HERE.parent / 'go.mod').read_text(encoding='utf-8')
    assert 'go 1.24' in go_mod
    assert _runner.PINNED_GO_VERSION in (
        'https://dl.google.com/go/go%s.linux-amd64.tar.gz' % _runner.PINNED_GO_VERSION)


@pytest.mark.skipif(os.name == 'nt',
                    reason='POSIX stub toolchain (Kaggle is Linux); the Windows build path is covered by the real pipeline test')
def test_setup_builds_agent_after_bootstrap(tmp_path, monkeypatch, _runner):
    """setup with Go hidden from PATH but a cached WORK_ROOT toolchain:
    the build runs through the prepended env (proven via a stub go that
    writes the -o target) -- the clone-to-build chain uses the bootstrapped
    toolchain env, not the system one."""
    _no_go_env(monkeypatch)
    monkeypatch.chdir(tmp_path)
    cfg = _hidden_go_cfg(tmp_path)
    work = Path(cfg['WORK_ROOT'])
    root = _fake_toolchain(work / 'go')
    stub = root / 'bin' / 'go'
    stub.write_text("""#!/bin/sh
if [ "$1" = "build" ]; then
  out=""
  prev=""
  for a in "$@"; do if [ "$prev" = "-o" ]; then out="$a"; fi; prev="$a"; done
  [ -n "$out" ] && : > "$out"
  exit 0
fi
echo fake-go-1.24.11
""")
    stub.chmod(0o755)
    _fake_repo(work / 'repo')
    # Binary deliberately absent so setup must take the build path.
    rc = _runner.cmd_setup(argparse.Namespace(
        repo_url=str(work / 'repo'), branch='main', work_root=str(work),
        control_plane_url='', enroll_token='', node_name='', data_dir='',
        update_repo='auto', api_token='', streaming_allowed='false',
        media_network='', media_endpoint='', goroot_url='', goroot_sha256='',
        verbose=False, func=None))
    assert rc == 0, 'setup must succeed using the bootstrapped toolchain env'
    assert (work / 'bin' / 'agent').exists()
