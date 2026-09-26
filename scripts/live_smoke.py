#!/usr/bin/env python3
"""Optional live vendor smoke test (manual; never part of CI).

    python scripts/live_smoke.py [--control URL] [--game ID]

Checks each external dependency that may exist on the operator's
machines and reports PASS / BLOCKED BY ENVIRONMENT / FAIL with exact
remediation. Missing optional software is BLOCKED, not failure; exit 1
only when something present-but-broken fails. Standard library only.
"""

import argparse
import json
import logging
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

log = logging.getLogger("live-smoke")
RESULTS = []


def record(name, status, detail=""):
    RESULTS.append((name, status, detail))
    log.info("%-28s %s %s", name, status, detail)


def run(cmd, timeout=30):
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return proc.returncode, (proc.stdout or "") + (proc.stderr or "")
    except FileNotFoundError:
        return 127, "not found"
    except subprocess.TimeoutExpired:
        return 124, "timed out"


def check_gpu():
    if not shutil.which("nvidia-smi"):
        record("GPU", "BLOCKED BY ENVIRONMENT", "no nvidia-smi (needs NVIDIA GPU + driver)")
        return
    rc, out = run(["nvidia-smi", "--query-gpu=name,driver_version", "--format=csv,noheader"])
    if rc != 0 or not out.strip():
        record("GPU", "FAIL", "nvidia-smi present but unusable")
        return
    record("GPU", "PASS", out.strip().splitlines()[0])


def check_docker_gpu():
    if not shutil.which("docker"):
        record("Docker+GPU", "BLOCKED BY ENVIRONMENT", "no docker binary")
        return
    rc, _ = run(["docker", "info"])
    if rc != 0:
        record("Docker+GPU", "BLOCKED BY ENVIRONMENT", "docker daemon not running")
        return
    rc, out = run(["docker", "run", "--rm", "--runtime=nvidia", "--gpus", "all",
                   "nvidia/cuda:12.6.0-base-ubuntu22.04", "nvidia-smi",
                   "--query-gpu=name", "--format=csv,noheader"], timeout=300)
    if rc != 0:
        record("Docker+GPU", "FAIL", "container GPU test failed (nvidia-ctk runtime configure?)")
        return
    record("Docker+GPU", "PASS", (out.strip().splitlines() or ["?"])[0])


def check_wolf():
    sock = Path(os.environ.get("WOLF_SOCKET_PATH", "/var/run/wolf/wolf.sock"))
    if not sock.exists():
        record("Wolf", "BLOCKED BY ENVIRONMENT", f"no socket at {sock} (deploy/gpu-node/setup.py)")
        return
    if not shutil.which("curl"):
        record("Wolf", "BLOCKED BY ENVIRONMENT", "socket present but no curl to probe it")
        return
    rc, out = run(["curl", "-s", "--unix-socket", str(sock), "http://localhost/api/v1/apps"])
    if rc != 0 or '"success":true' not in out.replace(" ", ""):
        record("Wolf", "FAIL", "socket present but API unreachable")
        return
    try:
        apps = json.loads(out).get("apps", [])
        record("Wolf", "PASS", f"{len(apps)} app(s) configured")
    except Exception:
        record("Wolf", "FAIL", "API answered but response unparsable")


def check_tailscale():
    if not shutil.which("tailscale"):
        record("Tailscale", "BLOCKED BY ENVIRONMENT", "not installed (https://tailscale.com/download)")
        return
    rc, _ = run(["tailscale", "status"])
    if rc != 0:
        record("Tailscale", "FAIL", "installed but down (sudo tailscale up)")
        return
    rc, out = run(["tailscale", "ip", "--4"])
    record("Tailscale", "PASS", (out.strip().split() or ["?"])[0])


def check_moonlight():
    if not shutil.which("moonlight"):
        record("Moonlight", "BLOCKED BY ENVIRONMENT", "not installed (https://moonlight-stream.org)")
        return
    record("Moonlight", "PASS", "binary present (pair once, then play)")


def check_postgres():
    url = os.environ.get("PG_DATABASE_URL", "")
    if not url:
        record("PostgreSQL", "BLOCKED BY ENVIRONMENT", "PG_DATABASE_URL unset")
        return
    try:
        import urllib.parse as up
        parts = up.urlparse(url)
        host, port = (parts.hostname or "localhost"), (parts.port or 5432)
        import socket
        with socket.create_connection((host, port), timeout=5):
            pass
        record("PostgreSQL", "PASS", f"{host}:{port} reachable (migrations run at boot)")
    except Exception as exc:
        record("PostgreSQL", "FAIL", f"unreachable: {exc}")


def check_objects():
    endpoint = os.environ.get("R2_ENDPOINT", "")
    if not endpoint and not os.environ.get("R2_ACCOUNT_ID"):
        record("Object storage", "BLOCKED BY ENVIRONMENT", "R2_* unset")
        return
    base = endpoint or ("https://" + os.environ["R2_ACCOUNT_ID"] + ".r2.cloudflarestorage.com")
    try:
        req = urllib.request.Request(base, method="HEAD")
        with urllib.request.urlopen(req, timeout=10):
            pass
        record("Object storage", "PASS", f"{base} reachable (full PUT/GET needs R2_* creds)")
    except Exception as exc:
        record("Object storage", "FAIL", f"{base} unreachable: {exc}")


def check_control_plane(control, game):
    try:
        with urllib.request.urlopen(control.rstrip("/") + "/healthz", timeout=10) as resp:
            if resp.status != 200:
                record("Control plane", "FAIL", f"healthz={resp.status}")
                return
    except Exception as exc:
        record("Control plane", "BLOCKED BY ENVIRONMENT", f"not running at {control}: {exc}")
        return
    try:
        with urllib.request.urlopen(control.rstrip("/") + "/v1/games", timeout=10) as resp:
            games = json.load(resp)["games"]
        ids = [g["game_id"] for g in games]
        extra = f"; catalog: {ids}"
        if game and game not in ids:
            record("Control plane", "FAIL", f"game {game} not in catalog{extra}")
            return
        record("Control plane", "PASS", f"healthy{extra}")
    except Exception as exc:
        record("Control plane", "FAIL", f"games API: {exc}")


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="Live vendor smoke test (optional, manual).")
    p.add_argument("--control", default="http://127.0.0.1:8080")
    p.add_argument("--game", default="")
    args = p.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="[%(levelname)s] %(message)s")
    for fn in (check_gpu, check_docker_gpu, check_wolf, check_tailscale,
               check_moonlight, check_postgres, check_objects):
        try:
            fn()
        except Exception as exc:
            record(fn.__name__, "FAIL", f"probe crashed: {exc}")
    check_control_plane(args.control, args.game)
    fails = [r for r in RESULTS if r[1] == "FAIL"]
    blocked = [r for r in RESULTS if r[1].startswith("BLOCKED")]
    log.info("live smoke: %d PASS, %d BLOCKED BY ENVIRONMENT, %d FAIL",
             len([r for r in RESULTS if r[1] == "PASS"]), len(blocked), len(fails))
    return 1 if fails else 0


if __name__ == "__main__":
    raise SystemExit(main())
