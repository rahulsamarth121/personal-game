#!/usr/bin/env python3
"""Kaggle runner: prepare and run the shared node agent inside Kaggle.

Canonical Kaggle entrypoint (ONE file, stdlib only)::

    python kaggle/runner.py all
    python kaggle/runner.py [clone|diagnostics|setup|run|cleanup|all]

The SAME file also supports direct execution inside Jupyter/Kaggle: paste
the entire source into one cell and Run. The entry point detects the
interactive kernel, never parses the kernel's own ``sys.argv`` (which
holds a ``kernel-*.json`` connection file), and runs the safe default
command ``diagnostics`` (read-only). Further cells can invoke specific
commands programmatically: ``runner.main(["all"])``, ``runner.main(["run"])``,
``runner.main(["cleanup"])`` — always with an explicit argument list.

Workflow: detect environment -> clone/pull the GitHub repo -> check deps ->
diagnose capabilities honestly -> build shared ``cmd/agent`` -> launch it
-> register via enroll/heartbeat -> monitor. ``cleanup`` removes ephemeral
download/extract staging only, never saves or the installed game.

Config precedence: CLI flags > environment (/ Kaggle Secrets) > defaults.
Secrets are never printed. Idempotent: reruns skip finished work.
"""

import argparse
import contextlib
import json
import logging
import os
import platform
import shlex
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

# Default control path for this deployment: the Personal Game Cloudflare
# edge relay in front of the Go control plane (control HTTP only — the
# Worker never carries Moonlight media traffic).
DEFAULT_CONTROL_PLANE_URL = (
    "https://personal-game-relay.rahul-zed-relay-84739261.workers.dev/personal-game"
)

# This project's own public repository: the paste-one-cell default so a
# fresh kernel can clone + report real agent capabilities with zero setup.
# Operators override with their fork via GITHUB_REPO_URL / --repo-url.
DEFAULT_REPO_URL = "https://github.com/rahulsamarth121/personal-game.git"

DEFAULTS = {
    "GITHUB_REPO_URL": "",
    "GITHUB_BRANCH": "main",
    "WORK_ROOT": "/tmp/personal-game",
    "CONTROL_PLANE_URL": DEFAULT_CONTROL_PLANE_URL,
    "NODE_ENROLLMENT_TOKEN": "",
    "NODE_NAME": "",
    "PG_API_TOKEN": "",
    "PG_DATA_DIR": "",
    # Capability policy: Kaggle is NOT automatically a gaming host. Streaming
    # is enabled ONLY by an operator who has explicit permission for that
    # workload on that infrastructure. Default is honestly "false".
    "STREAMING_ALLOWED": "false",
    "MEDIA_NETWORK": "",   # tailscale | cloudflare_private_network | direct
    "MEDIA_ENDPOINT": "",
    "UPDATE_REPO": "auto",  # auto|always|never
}

SECRET_KEYS = {"GITHUB_TOKEN", "NODE_ENROLLMENT_TOKEN", "PG_API_TOKEN"}


def redact(text: str) -> str:
    """Mask configured secret values before text reaches logs or output."""
    for value in (os.environ.get(k, "") for k in SECRET_KEYS):
        if value and len(value) >= 8:
            text = text.replace(value, "[REDACTED]")
    return text

log = logging.getLogger("kaggle-runner")

# Cloudflare's bot protection on *.workers.dev rejects the default
# "Python-urllib/x.y" User-Agent (HTML error 1010), so every health probe
# identifies itself explicitly. (The Go agent already sends Go-http-client,
# which is allowed.)
USER_AGENT = "personal-game-kaggle-runner/1.0"


def _http_get(url: str, timeout: int = 10):
    """GET with an identifying User-Agent; returns (status, body) or raises."""
    req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.status, resp.read().decode("utf-8", "replace")


def setup_logging(verbose: bool) -> None:
    logging.basicConfig(
        level=logging.DEBUG if verbose else logging.INFO,
        format="[%(levelname)s] %(message)s",
    )


def print_banner(cfg: dict | None = None) -> None:
    """Startup identity + where to configure things. Never prints secrets."""
    repo = "github/kaggle/runner.py"
    print("PERSONAL GAME NODE")
    print("==================")
    print()
    print("Repository:      " + ((cfg or {}).get("GITHUB_REPO_URL") or "(set GITHUB_REPO_URL / --repo-url)"))
    print(f"Node runner:     {repo}")
    print("Shared agent:    Go node agent (cmd/agent) — no second implementation")
    print("Control Plane:   " + (cfg or {}).get("CONTROL_PLANE_URL", DEFAULT_CONTROL_PLANE_URL))
    print("Streaming policy: capability/configuration controlled")
    print("                  (STREAMING_ALLOWED=true ONLY with explicit provider permission)")
    print("Media:           provider-neutral (tailscale | cloudflare_private_network | direct)")
    print()
    print("Configure via Kaggle Secrets / env:")
    print("  CONTROL_PLANE_URL      control-plane base URL (default: the relay)")
    print("  PG_API_TOKEN           client-API bearer token (SECRET)")
    print("  NODE_ENROLLMENT_TOKEN  node enrollment credential (SECRET)")
    print("  NODE_NAME              node identity (default: kernel type)")
    print("  STREAMING_ALLOWED      'true' only where streaming is permitted")
    print("  MEDIA_NETWORK          tailscale | cloudflare_private_network | direct")
    print("  MEDIA_ENDPOINT         Moonlight-reachable IP/DNS (when required)")
    print()


def load_config(args: argparse.Namespace) -> dict:
    cfg = dict(DEFAULTS)
    for key in DEFAULTS:
        if os.environ.get(key):
            cfg[key] = os.environ[key]
    for key in ("repo_url", "branch", "work_root", "control_plane_url",
                "enroll_token", "node_name", "data_dir", "update_repo",
                "api_token", "streaming_allowed", "media_network",
                "media_endpoint"):
        val = getattr(args, key, None)
        if val:
            cfg[{"repo_url": "GITHUB_REPO_URL", "branch": "GITHUB_BRANCH",
                 "work_root": "WORK_ROOT", "control_plane_url": "CONTROL_PLANE_URL",
                 "enroll_token": "NODE_ENROLLMENT_TOKEN", "node_name": "NODE_NAME",
                 "data_dir": "PG_DATA_DIR",
                 "update_repo": "UPDATE_REPO",
                 "api_token": "PG_API_TOKEN",
                 "streaming_allowed": "STREAMING_ALLOWED",
                 "media_network": "MEDIA_NETWORK",
                 "media_endpoint": "MEDIA_ENDPOINT"}[key]] = val
    if not cfg["NODE_NAME"]:
        cfg["NODE_NAME"] = os.environ.get("KAGGLE_KERNEL_RUN_TYPE", "kaggle-node")
    return cfg


def repo_dir(cfg: dict) -> Path:
    return Path(cfg["WORK_ROOT"]) / "repo"


def run(cmd, cwd=None, timeout=120, check_text=True, env=None):
    try:
        proc = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True,
                              timeout=timeout, env=env)
        return proc.returncode, redact((proc.stdout or "") + (proc.stderr or ""))
    except FileNotFoundError:
        return 127, f"not found: {cmd[0]}"
    except subprocess.TimeoutExpired:
        return 124, f"timed out: {' '.join(cmd)}"


@contextlib.contextmanager
def git_auth_env(cfg: dict):
    """Private-repo auth for git subprocesses via a temporary askpass helper.

    When the GITHUB_TOKEN secret is set, it is written to a 0600 temp file
    read by a tiny askpass script (mirroring the notebook bootstrap): the
    token never appears in the Git URL or the command line, and both files
    are deleted when the block exits — even on failure. Yields an env dict
    for run(..., env=...), or {} when no token is configured (public repos
    or pre-authenticated environments).
    """
    token = os.environ.get("GITHUB_TOKEN", "")
    tokfile = Path(cfg["WORK_ROOT"]) / ".pg_tok"
    askpass = Path(cfg["WORK_ROOT"]) / ".pg_askpass.sh"
    if not token:
        yield {}
        return
    tokfile.parent.mkdir(parents=True, exist_ok=True)
    tokfile.write_text(token)
    try:
        tokfile.chmod(0o600)
        askpass.write_text("#!/bin/sh\ncase \"$1\" in Username*) echo personal-game ;; "
                           "*) cat " + shlex.quote(str(tokfile)) + " ;; esac\n")
        askpass.chmod(0o755)
        yield dict(os.environ, GIT_ASKPASS=str(askpass), GIT_TERMINAL_PROMPT="0")
    finally:
        askpass.unlink(missing_ok=True)
        tokfile.unlink(missing_ok=True)


def cmd_clone(args: argparse.Namespace) -> int:
    """Clone the repo if absent; fast-forward safely if requested."""
    cfg = load_config(args)
    if not cfg["GITHUB_REPO_URL"]:
        log.error("GITHUB_REPO_URL is not set (env or --repo-url). Nothing to clone.")
        return 2
    dest = repo_dir(cfg)
    if not (dest / ".git").exists() and dest.exists() and any(dest.iterdir()):
        log.error("WORK_ROOT/repo exists but is not a git checkout: %s", dest)
        return 1
    if not dest.exists():
        log.info("Cloning %s (branch %s)...", cfg["GITHUB_REPO_URL"], cfg["GITHUB_BRANCH"])
        with git_auth_env(cfg) as auth_env:
            code, out = run(["git", "clone", "--branch", cfg["GITHUB_BRANCH"], "--depth", "1",
                             cfg["GITHUB_REPO_URL"], str(dest)], timeout=600, env=auth_env or None)
        if code != 0:
            log.error("Clone failed: %s", out[-2000:])
            return 1
        log.info("Cloned to %s", dest)
        return 0
    mode = cfg["UPDATE_REPO"]
    if mode == "never":
        log.info("Repo present, update disabled: %s", dest)
        return 0
    code, dirty = run(["git", "status", "--porcelain"], cwd=dest)
    if code == 0 and dirty.strip() and mode == "auto":
        log.warning("Repo has local changes; skipping pull (use --update-repo always to override).")
        return 0
    log.info("Updating repo (fast-forward only)...")
    with git_auth_env(cfg) as auth_env:
        code, out = run(["git", "pull", "--ff-only"], cwd=dest, timeout=300, env=auth_env or None)
    if code != 0:
        log.warning("Pull skipped/failed (keeping current checkout): %s", out[-1000:])
        return 0
    log.info("Repo updated: %s", dest)
    return 0


def find_repo(cfg: dict) -> Path | None:
    """Locate the Go project root (go.mod + cmd/agent)."""
    candidates = [repo_dir(cfg), Path.cwd(), Path.cwd() / "personal-game"]
    for cand in candidates:
        if (cand / "go.mod").exists() and (cand / "cmd" / "agent").exists():
            return cand
    return None


def mem_total_mb() -> int:
    try:
        for line in Path("/proc/meminfo").read_text().splitlines():
            if line.startswith("MemTotal:"):
                return int(line.split()[1]) // 1024
    except Exception:
        pass
    return 0


def gpu_info() -> str:
    if shutil.which("nvidia-smi") is None:
        return "unavailable (no nvidia-smi)"
    code, out = run(["nvidia-smi", "--query-gpu=name,memory.total,driver_version",
                     "--format=csv,noheader"], timeout=30)
    if code != 0 or not out.strip():
        return "unavailable (nvidia-smi failed)"
    return out.strip().splitlines()[0]


def disk_free(path: Path) -> int:
    try:
        return shutil.disk_usage(path).free
    except OSError:
        return 0


def _worker_health_ok(url: str) -> bool:
    """True only when a Personal Game Worker relay is fully healthy.

    The relay's /health must return HTTP 200 AND its JSON body must report
    both ``relay=ok`` and ``upstream=ok``. A 200 with a degraded upstream is
    NOT reachable — health is verified, never assumed.
    """
    try:
        status, body = _http_get(url, timeout=10)
    except Exception as exc:
        log.debug("No Worker-style health at %s: %s", url, exc)
        return False
    if status != 200:
        return False
    try:
        health = json.loads(body)
    except ValueError:
        log.debug("/health at %s returned non-JSON body", url)
        return False
    if health.get("relay") == "ok" and health.get("upstream") == "ok":
        log.info("Worker relay healthy: %s (upstream: ok)", url)
        return True
    log.warning("Worker relay responded but is not fully healthy: %s", body.strip()[:200])
    return False


def control_reachable(url: str) -> bool:
    """Health-check the configured control plane. Two supported shapes:

    * Personal Game Worker relay (``.../personal-game``): probe ``/health``
      and require JSON ``relay=ok`` + ``upstream=ok``.
    * Direct Go control plane: probe ``/healthz`` (HTTP 200).

    Failure of either probe is reported honestly; success is never faked.
    """
    base = url.rstrip("/")
    if _worker_health_ok(base + "/health"):
        return True
    try:
        status, _ = _http_get(base + "/healthz", timeout=10)
        if status == 200:
            log.info("Control plane reachable: %s/healthz", base)
            return True
        return False
    except Exception as exc:
        log.warning("Control plane unreachable at %s (tried /health and /healthz): %s", base, exc)
        return False


def cmd_diagnostics(args: argparse.Namespace) -> int:
    """Read-only capability report. Missing capabilities are reported, never faked."""
    cfg = load_config(args)
    log.info("OS: %s", platform.platform())
    log.info("CPU: %s x%d", platform.processor() or "unknown", os.cpu_count() or 0)
    log.info("RAM: %s", f"{mem_total_mb()} MB" if mem_total_mb() else "unknown")
    log.info("GPU: %s", gpu_info())
    free_gb = disk_free(Path(cfg["WORK_ROOT"]))
    log.info("Disk free (work root): %s", f"{free_gb / 1e9:.1f} GB" if free_gb else "unknown")
    for tool in ("go", "git", "aria2c", "7z", "docker", "tailscale", "sunshine", "wolf"):
        log.info("%s: %s", tool, "present" if shutil.which(tool) else "missing")
    log.info("Gamepad/uinput: %s",
             "present" if Path("/dev/uinput").exists() or Path("/dev/input").exists() else "unavailable")
    repo = find_repo(cfg)
    if repo is None:
        log.warning("Repo not found (run `clone` first); skipping agent capability dump.")
    elif shutil.which("go") is None:
        log.warning("Go toolchain missing; skipping agent capability dump.")
    else:
        # Build the shared agent, then ask it for its capability document
        # (`agent caps`: no enrollment, no network I/O, exits immediately).
        binary = agent_binary(repo, cfg)
        binary.parent.mkdir(parents=True, exist_ok=True)
        code, out = run(["go", "build", "-o", str(binary), "./cmd/agent"],
                        cwd=repo, timeout=900)
        if code != 0:
            log.warning("Agent build failed: %s", out[-800:])
        else:
            # Generous timeout: first execution of a fresh binary can stall
            # under antivirus real-time scanning (observed >60 s on Windows).
            code, out = run([str(binary), "caps"], timeout=300)
            if code != 0:
                log.warning("Agent capability dump failed (exit %d): %s", code, out[-300:])
            else:
                try:
                    caps = json.loads(out[out.index("{"):out.rindex("}") + 1])["caps"]
                    log.info("Agent caps: os=%s arch=%s gpu=%s encoders=%s docker=%s tailscale=%s",
                             caps.get("os"), caps.get("arch"), (caps.get("gpu") or {}).get("model") or "none",
                             ",".join(caps.get("encoders") or []) or "none",
                             caps.get("docker"), caps.get("tailscale"))
                except Exception:
                    log.warning("Could not parse agent caps output.")
    control_reachable(cfg["CONTROL_PLANE_URL"])
    streaming = str(cfg.get("STREAMING_ALLOWED", "false")).lower() in ("1", "true", "yes", "on")
    log.info("Streaming allowed: %s", streaming)
    if cfg.get("MEDIA_NETWORK"):
        log.info("Media network: %s%s", cfg["MEDIA_NETWORK"],
                 f" at {cfg['MEDIA_ENDPOINT']}" if cfg.get("MEDIA_ENDPOINT") else "(endpoint unset)")
    else:
        log.info("Media network: unset (agent defaults to measured Tailscale state)")
    if not streaming:
        log.info("This node advertises streaming_allowed=false and will never be scheduled for game sessions.")
    return 0


def agent_binary(repo: Path, cfg: dict) -> Path:
    # Platform-correct name: Windows executables MUST carry .exe (an
    # extension-less PE makes CreateProcess semantics flaky — observed as
    # intermittent FileNotFoundError / AV-scan timeouts); Linux/Kaggle keeps
    # the plain name.
    suffix = ".exe" if os.name == "nt" else ""
    return Path(cfg["WORK_ROOT"]) / "bin" / ("agent" + suffix)


def cmd_setup(args: argparse.Namespace) -> int:
    """Idempotent: ensure toolchain, data dirs, and a fresh agent binary."""
    cfg = load_config(args)
    if shutil.which("go") is None:
        log.error("Go toolchain missing. Install Go >= 1.24 in this environment first.")
        return 1
    repo = find_repo(cfg)
    if repo is None:
        log.error("Repo not found. Run `clone` first (GITHUB_REPO_URL required).")
        return 1
    data = Path(cfg["PG_DATA_DIR"] or (repo / "data"))
    for sub in ("games", "cache", "state"):
        (data / sub).mkdir(parents=True, exist_ok=True)
    binary = agent_binary(repo, cfg)
    binary.parent.mkdir(parents=True, exist_ok=True)
    newest = 0.0
    for path in list(repo.rglob("*.go")) + list(repo.rglob("go.mod")):
        if ".git" in path.parts:
            continue
        newest = max(newest, path.stat().st_mtime)
    if binary.exists() and binary.stat().st_mtime >= newest:
        log.info("Agent binary up to date: %s", binary)
        return 0
    log.info("Building node agent...")
    code, out = run(["go", "build", "-o", str(binary), "./cmd/agent"], cwd=repo, timeout=900)
    if code != 0:
        log.error("Build failed: %s", out[-4000:])
        return 1
    log.info("Built: %s", binary)
    return 0


def cmd_run(args: argparse.Namespace) -> int:
    """Build if needed, then launch the shared agent in the foreground."""
    cfg = load_config(args)
    for key in ("CONTROL_PLANE_URL", "NODE_NAME"):
        if not cfg[key]:
            log.error("%s is not set (env or CLI).", key)
            return 2
    if not cfg["NODE_ENROLLMENT_TOKEN"]:
        log.warning("NODE_ENROLLMENT_TOKEN unset; enrollment may be rejected.")
    if cmd_setup(args) != 0:
        return 1
    repo = find_repo(cfg)
    binary = agent_binary(repo, cfg)
    env = dict(os.environ)
    env["PG_CONTROL_URL"] = cfg["CONTROL_PLANE_URL"]
    env["PG_NODE_ID"] = cfg["NODE_NAME"]
    env["PG_ENROLL_TOKEN"] = cfg["NODE_ENROLLMENT_TOKEN"]
    env["PG_DATA_DIR"] = cfg["PG_DATA_DIR"] or str(repo / "data")
    if cfg.get("PG_API_TOKEN"):
        env["PG_API_TOKEN"] = cfg["PG_API_TOKEN"]  # forwarded, never logged
    # Capability policy + media network are plain config values.
    env["STREAMING_ALLOWED"] = "true" if str(cfg.get("STREAMING_ALLOWED", "false")).lower() in ("1", "true", "yes", "on") else "false"
    if cfg.get("MEDIA_NETWORK"):
        env["MEDIA_NETWORK"] = cfg["MEDIA_NETWORK"]
    if cfg.get("MEDIA_ENDPOINT"):
        env["MEDIA_ENDPOINT"] = cfg["MEDIA_ENDPOINT"]
    log_path = Path(cfg["WORK_ROOT"]) / "agent.log"
    log.info("Node: %s -> %s", cfg["NODE_NAME"], cfg["CONTROL_PLANE_URL"])
    log.info("Streaming allowed: %s", env["STREAMING_ALLOWED"])
    if cfg.get("MEDIA_NETWORK"):
        log.info("Media network: %s%s", cfg["MEDIA_NETWORK"],
                 f" at {cfg['MEDIA_ENDPOINT']}" if cfg.get("MEDIA_ENDPOINT") else "")
    log.info("Starting node agent... (Ctrl+C to stop, log: %s)", log_path)
    try:
        proc = subprocess.Popen([str(binary)], cwd=repo, env=env,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
    except OSError as exc:
        log.error("Could not start %s: %s (check the build)", binary, exc)
        return 1
    try:
        assert proc.stdout is not None
        with open(log_path, "a", encoding="utf-8") as logf:
            for line in proc.stdout:
                sys.stdout.write(line)
                sys.stdout.flush()
                logf.write(line)
        return proc.wait()
    except KeyboardInterrupt:
        log.info("Stopping node agent...")
        proc.terminate()
        try:
            return proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
            return proc.wait()


def cmd_cleanup(args: argparse.Namespace) -> int:
    """Delete ephemeral staging only. Never saves, games, or metadata."""
    cfg = load_config(args)
    repo = find_repo(cfg)
    data = Path(cfg["PG_DATA_DIR"] or ((repo / "data") if repo else Path(cfg["WORK_ROOT"]) / "data"))
    removed = []
    for sub in ("cache", "tmp"):
        target = data / sub
        if target.exists():
            shutil.rmtree(target, ignore_errors=True)
            removed.append(str(target))
    log.info("Cleanup: removed %s (saves, games, metadata untouched)",
             ", ".join(removed) if removed else "nothing (already clean)")
    return 0


def cmd_all(args: argparse.Namespace) -> int:
    for step in (cmd_clone, cmd_diagnostics, cmd_setup, cmd_run):
        rc = step(args)
        if rc != 0:
            log.error("Sequence stopped at %s (exit %d).", step.__name__, rc)
            return rc
    return 0


def build_parser() -> argparse.ArgumentParser:
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--repo-url", help="override GITHUB_REPO_URL")
    common.add_argument("--branch", help="override GITHUB_BRANCH")
    common.add_argument("--work-root", help="override WORK_ROOT")
    common.add_argument("--control-plane-url", help="override CONTROL_PLANE_URL")
    common.add_argument("--enroll-token", help="override NODE_ENROLLMENT_TOKEN")
    common.add_argument("--api-token", help="override PG_API_TOKEN (prefer Kaggle Secrets)")
    common.add_argument("--streaming-allowed", choices=["true", "false"],
                        help="override STREAMING_ALLOWED (true ONLY with provider permission)")
    common.add_argument("--media-network", help="override MEDIA_NETWORK")
    common.add_argument("--media-endpoint", help="override MEDIA_ENDPOINT")
    common.add_argument("--node-name", help="override NODE_NAME")
    common.add_argument("--data-dir", help="override PG_DATA_DIR")
    common.add_argument("--update-repo", choices=["auto", "always", "never"])
    p = argparse.ArgumentParser(
        description="Kaggle runner: clone, diagnose, build, and run the shared node agent.",
        parents=[common])
    p.add_argument("-v", "--verbose", action="store_true")
    sub = p.add_subparsers(dest="command")
    for name, fn, help_text in [
        ("clone", cmd_clone, "clone/pull the GitHub repo (safe, idempotent)"),
        ("diagnostics", cmd_diagnostics, "honest capability report (read-only)"),
        ("setup", cmd_setup, "check deps and build the agent (idempotent)"),
        ("run", cmd_run, "build if needed and run the agent (foreground)"),
        ("cleanup", cmd_cleanup, "remove ephemeral staging only (never saves)"),
        ("all", cmd_all, "clone -> diagnostics -> setup -> run"),
    ]:
        sub.add_parser(name, help=help_text, parents=[common]).set_defaults(func=fn)
    return p


def print_status(cfg: dict) -> None:
    """Human-readable node status. Secrets are never printed."""
    streaming = str(cfg.get("STREAMING_ALLOWED", "false")).lower() in ("1", "true", "yes", "on")
    print("Personal Game Node")
    print("------------------")
    print(f"Node name:         {cfg.get('NODE_NAME') or '(default: kernel type)'}")
    print(f"GPU:               (see diagnostics — reported by the agent honestly)")
    print(f"VRAM:              (see diagnostics)")
    print(f"Control plane:     {cfg.get('CONTROL_PLANE_URL')}")
    enrolled = bool(cfg.get("NODE_ENROLLMENT_TOKEN"))
    print(f"Enrollment:        {'credential configured' if enrolled else 'NOT configured (set NODE_ENROLLMENT_TOKEN)'}")
    print(f"API token:         {'configured' if cfg.get('PG_API_TOKEN') else 'not configured (set PG_API_TOKEN if the plane requires one)'}")
    print(f"Heartbeat:         agent loop renews the lease automatically")
    print(f"Streaming allowed: {streaming}")
    if cfg.get("MEDIA_NETWORK"):
        print(f"Media network:     {cfg['MEDIA_NETWORK']}" +
              (f" at {cfg['MEDIA_ENDPOINT']}" if cfg.get("MEDIA_ENDPOINT") else ""))
    else:
        print(f"Media network:     (unset — defaults to measured Tailscale state)")
    if not streaming:
        print("NOTE: streaming_allowed=false — this node will NEVER receive game sessions.")
        print("      Set STREAMING_ALLOWED=true only with explicit provider permission.")


# --- execution context / argument resolution ------------------------------

# Safe default when the whole runner source is pasted into a notebook cell:
# read-only, honest, non-destructive, and it never starts a game host.
NOTEBOOK_DEFAULT_COMMAND = "diagnostics"


def _in_ipython_interactive() -> bool:
    """True only inside an IPython/Jupyter interactive kernel.

    Plain ``python`` / ``python runner.py`` has no IPython at all. A
    terminal IPython session is still a terminal, so CLI semantics apply.
    Notebook kernels (Kaggle, Colab, Jupyter) run a non-terminal
    InteractiveShell — exactly the case where kernel argv must be ignored.
    """
    try:
        from IPython import get_ipython
    except Exception:
        return False
    try:
        ipy = get_ipython()
    except Exception:
        return False
    if ipy is None:
        return False
    return type(ipy).__name__ != "TerminalInteractiveShell"


def resolve_runner_argv(argv=None):
    """Resolve ``(args_list, notebook_mode)`` for ``parser.parse_args``.

    Contract (unit-tested):

    * ``resolve_runner_argv([...])`` / ``main([...])`` — explicit argv wins
      verbatim; notebook detection is irrelevant (tests, programmatic calls).
    * ``main()`` in a normal CLI process — parses ``sys.argv[1:]``, exactly
      the historical behavior.
    * ``main()`` with the whole source pasted into a Jupyter/Kaggle cell —
      the kernel's ``sys.argv`` (``-f .../kernel-*.json``) is NOT runner
      input: return the safe default command instead. The kernel argv is
      neither parsed (no ``parse_known_args``) nor mutated, and argparse
      validation is untouched.
    """
    if argv is not None:
        return list(argv), False
    if _in_ipython_interactive():
        return [NOTEBOOK_DEFAULT_COMMAND], True
    return list(sys.argv[1:]), False


def main(argv=None) -> int:
    resolved, notebook_mode = resolve_runner_argv(argv)
    if notebook_mode:
        # Paste-one-cell default: the project's public repo so a fresh
        # kernel can clone + report real agent caps with zero setup.
        # Explicit env / later explicit argv still win.
        os.environ.setdefault("GITHUB_REPO_URL", DEFAULT_REPO_URL)
    parser = build_parser()
    args = parser.parse_args(resolved)
    setup_logging(args.verbose)
    cfg = load_config(args)
    if notebook_mode:
        print("NOTEBOOK MODE: runner source executed inside an interactive kernel.")
        print("The kernel's own arguments were ignored (they are not runner input).")
        print(f"Running the safe default command {NOTEBOOK_DEFAULT_COMMAND!r} (read-only).")
        print("Other commands in later cells: runner.main([\"all\"]), "
              "runner.main([\"run\"]), runner.main([\"cleanup\"]), ...")
        print()
    print_banner(cfg)
    print_status(cfg)
    print()
    if getattr(args, "func", None) is None:
        parser.print_help()
        return 2
    started = time.time()
    rc = args.func(args)
    log.info("Done in %.1fs (exit %d)", time.time() - started, rc)
    return rc


if __name__ == "__main__":
    _rc = main()
    if _in_ipython_interactive():
        # Raising SystemExit inside a notebook cell would surface as an
        # exception traceback even on success; report the code instead.
        print(f"[runner] exit code: {_rc}")
    else:
        raise SystemExit(_rc)
