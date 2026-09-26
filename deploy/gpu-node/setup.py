#!/usr/bin/env python3
"""GPU node setup: ONE entry point for a real Linux GPU node.

    python3 setup.py check          # diagnostics only (safe everywhere)
    python3 setup.py install-deps   # distro packages, Docker, NVIDIA toolkit, Tailscale
    python3 setup.py install-wolf   # pull + run Wolf with the node socket
    python3 setup.py smoke          # validate: GPU, docker+gpus, wolf socket, tailscale
    python3 setup.py all            # check -> install-deps -> install-wolf -> smoke

Install sources (current official docs):
  Docker:            https://get.docker.com (official convenience script)
  NVIDIA toolkit:    https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html
                     (apt repo nvidia-container-toolkit, then
                      `nvidia-ctk runtime configure --runtime=docker`)
  Tailscale:         https://tailscale.com/download (`install.sh`, then `tailscale up`)
  Wolf:              https://games-on-whales.github.io/wolf (ghcr.io/games-on-whales/wolf:stable)

Rules: detect everything first; hard-fail (no silent continue) when the
GPU/driver is missing; `--dry-run` prints commands without executing;
secrets (tailscale auth keys) come from env/CLI, never stored in git.
Standard library only.
"""

import argparse
import json
import logging
import os
import platform
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
WOLF_IMAGE = os.environ.get("WOLF_IMAGE", "ghcr.io/games-on-whales/wolf:stable")
WOLF_SOCKET_DIR = Path(os.environ.get("WOLF_SOCKET_DIR", "/var/run/wolf"))
WOLF_STATE_DIR = Path(os.environ.get("WOLF_STATE_DIR", "/etc/wolf"))

log = logging.getLogger("gpu-setup")


def setup_logging(verbose: bool) -> None:
    logging.basicConfig(level=logging.DEBUG if verbose else logging.INFO,
                        format="[%(levelname)s] %(message)s")


def sh(cmd, timeout=120, sudo=False, dry=False, env=None):
    """Run a command. Returns (rc, output). dry=True prints and pretends success."""
    if sudo and getattr(os, "geteuid", lambda: 0)() != 0:
        cmd = ["sudo"] + cmd
    label = " ".join(cmd)
    if dry:
        log.info("DRY-RUN: %s", label)
        return 0, ""
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, env=env)
        return proc.returncode, (proc.stdout or "") + (proc.stderr or "")
    except FileNotFoundError:
        return 127, f"not found: {cmd[0]}"
    except subprocess.TimeoutExpired:
        return 124, f"timed out: {label}"


def have(name: str) -> bool:
    return shutil.which(name) is not None


def distro() -> dict:
    info = {"id": "unknown", "version": "", "like": ""}
    try:
        for line in Path("/etc/os-release").read_text().splitlines():
            if "=" not in line:
                continue
            k, _, v = line.partition("=")
            v = v.strip().strip('"')
            if k == "ID":
                info["id"] = v
            elif k == "VERSION_ID":
                info["version"] = v
            elif k == "ID_LIKE":
                info["like"] = v
    except OSError:
        pass
    return info


def is_linux() -> bool:
    return platform.system() == "Linux"


# ---------------------------------------------------------------- checks

def check_distro(rep: dict) -> bool:
    d = distro()
    ok = is_linux() and d["id"] != "unknown"
    rep["distro"] = f"{d['id']} {d['version']}".strip()
    log.info("Distro: %s (supported: Ubuntu/Debian family preferred)", rep["distro"])
    return ok


def check_gpu(rep: dict) -> bool:
    """nvidia-smi must answer AND report at least one GPU with VRAM+driver."""
    if not have("nvidia-smi"):
        rep["gpu"] = "MISSING: nvidia-smi not found (install NVIDIA drivers first)"
        log.error(rep["gpu"])
        return False
    rc, out = sh(["nvidia-smi", "--query-gpu=name,memory.total,driver_version",
                  "--format=csv,noheader,nounits"])
    if rc != 0 or not out.strip():
        rep["gpu"] = f"nvidia-smi failed (rc={rc}); driver present but unusable"
        log.error(rep["gpu"])
        return False
    gpus = [g.strip() for g in out.strip().splitlines() if g.strip()]
    rep["gpu"] = f"{len(gpus)} GPU(s): {gpus[0]}"
    log.info("GPU: %s", rep["gpu"])
    m = re.search(r",\s*(\d+)\s*MiB", gpus[0])
    rep["vram_mib"] = int(m.group(1)) if m else 0
    return True


def check_devices(rep: dict) -> bool:
    """Wolf needs /dev/dri (or NVIDIA devices), /dev/uinput, /dev/uhid."""
    ok = True
    for dev, why in [("/dev/dri", "render nodes (Intel/AMD)"),
                     ("/dev/nvidia0", "NVIDIA device (or /dev/dri)"),
                     ("/dev/uinput", "gamepad/keyboard input"),
                     ("/dev/uhid", "hotplug input")]:
        present = Path(dev).exists()
        log.info("%s: %s (%s)", dev, "present" if present else "MISSING", why)
        if dev in ("/dev/uinput", "/dev/uhid") and not present:
            ok = False
    has_video = Path("/dev/dri").exists() or Path("/dev/nvidia0").exists()
    if not has_video:
        log.error("No GPU device nodes (/dev/dri nor /dev/nvidia0)")
        ok = False
    rep["devices"] = "ok" if ok else "missing device nodes"
    return ok


def check_docker(rep: dict) -> bool:
    if not have("docker"):
        rep["docker"] = "MISSING: install via https://get.docker.com"
        log.error(rep["docker"])
        return False
    rc, out = sh(["docker", "info", "--format", "{{json .Runtimes}}"])
    runtimes = out.strip() if rc == 0 else ""
    rep["docker"] = "present"
    log.info("Docker: present")
    nvidia_rt = "nvidia" in runtimes.lower()
    rep["nvidia_runtime"] = nvidia_rt
    log.info("NVIDIA container runtime: %s", "configured" if nvidia_rt else "NOT configured")
    return True


def check_toolkit(rep: dict) -> bool:
    for binary in ("nvidia-ctk", "nvidia-container-cli"):
        if have(binary):
            rc, out = sh([binary, "--version"])
            ver = (out.strip().splitlines() or ["?"])[0][:80]
            rep["toolkit"] = f"{binary} {ver}"
            log.info("NVIDIA toolkit: %s", rep["toolkit"])
            return True
    rep["toolkit"] = "MISSING: nvidia-container-toolkit (see install-deps)"
    log.error(rep["toolkit"])
    return False


def check_tailscale(rep: dict) -> bool:
    if not have("tailscale"):
        rep["tailscale"] = "MISSING: curl -fsSL https://tailscale.com/install.sh | sh"
        log.error(rep["tailscale"])
        return False
    rc, _ = sh(["tailscale", "status"])
    if rc != 0:
        rep["tailscale"] = "installed but DOWN (run: sudo tailscale up)"
        log.error(rep["tailscale"])
        return False
    rc, out = sh(["tailscale", "ip", "--4"])
    ip = out.strip().split()[0] if out.strip() else "?"
    rc, out = sh(["tailscale", "status", "--self=false"], timeout=15)
    rep["tailscale"] = f"up at {ip}"
    log.info("Tailscale: up at %s", ip)
    return True


def check_wolf(rep: dict) -> bool:
    sock = WOLF_SOCKET_DIR / "wolf.sock"
    if sock.exists():
        rep["wolf"] = f"socket present at {sock}"
        log.info("Wolf: %s", rep["wolf"])
        return True
    rep["wolf"] = f"socket absent at {sock} (run install-wolf)"
    log.error(rep["wolf"])
    return False


def check_go(rep: dict) -> bool:
    if not have("go"):
        rep["go"] = "MISSING: Go >= 1.24 for the node agent"
        log.error(rep["go"])
        return False
    _, out = sh(["go", "version"])
    rep["go"] = out.strip().splitlines()[0] if out.strip() else "present"
    log.info("Go: %s", rep["go"])
    return True


CHECKS = [
    ("distro", check_distro, True),
    ("gpu", check_gpu, True),
    ("devices", check_devices, True),
    ("docker", check_docker, True),
    ("toolkit", check_toolkit, False),
    ("tailscale", check_tailscale, False),
    ("wolf", check_wolf, False),
    ("go", check_go, True),
]


def cmd_check(_args) -> int:
    rep: dict = {}
    failed_required = []
    for name, fn, required in CHECKS:
        try:
            ok = fn(rep)
        except Exception as exc:  # never let one probe kill the report
            log.error("%s probe crashed: %s", name, exc)
            ok = False
        if required and not ok:
            failed_required.append(name)
    print(json.dumps(rep, indent=2))
    if failed_required:
        log.error("REQUIRED checks failed: %s — refusing to continue", ", ".join(failed_required))
        return 1
    log.info("All required checks passed (optional gaps reported above).")
    return 0


# ---------------------------------------------------------------- install

def apt_install(pkgs, dry: bool) -> bool:
    rc, _ = sh(["apt-get", "update"], sudo=True, dry=dry, timeout=300)
    if rc != 0 and not dry:
        return False
    rc, out = sh(["apt-get", "install", "-y"] + pkgs, sudo=True, dry=dry, timeout=900)
    if rc != 0:
        log.error("apt install failed: %s", out[-2000:])
        return False
    return True


def cmd_install_deps(args) -> int:
    dry = args.dry_run
    d = distro()
    if not dry and d["id"] not in ("ubuntu", "debian", "raspbian", "pop", "linuxmint") and "debian" not in d["like"]:
        log.error("install-deps supports Debian-family only (found %s); install manually", d)
        return 2
    log.info("Installing base packages (curl, ca-certificates, gnupg)...")
    if not apt_install(["ca-certificates", "curl", "gnupg"], dry):
        return 1
    if not have("docker"):
        log.info("Installing Docker (official get.docker.com script)...")
        rc, out = sh(["sh", "-c", "curl -fsSL https://get.docker.com | sh"], sudo=True, dry=dry, timeout=900)
        if rc != 0:
            log.error("docker install failed: %s", out[-2000:])
            return 1
    else:
        log.info("Docker already present.")
    if not (have("nvidia-ctk") or have("nvidia-container-cli")):
        log.info("Installing NVIDIA Container Toolkit (official apt repo)...")
        cmds = [
            ["sh", "-c", "curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | "
                         "gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg"],
            ["sh", "-c", "curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | "
                         "sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | "
                         "tee /etc/apt/sources.list.d/nvidia-container-toolkit.list"],
        ]
        for c in cmds:
            rc, out = sh(c, sudo=True, dry=dry, timeout=120)
            if rc != 0:
                log.error("toolkit repo setup failed: %s", out[-2000:])
                return 1
        if not apt_install(["nvidia-container-toolkit"], dry):
            return 1
        rc, out = sh(["nvidia-ctk", "runtime", "configure", "--runtime=docker"], sudo=True, dry=dry)
        if rc != 0:
            log.error("nvidia-ctk configure failed: %s", out[-2000:])
            return 1
        log.info("Restarting docker (applies NVIDIA runtime)...")
        sh(["systemctl", "restart", "docker"], sudo=True, dry=dry)
    else:
        log.info("NVIDIA toolkit already present.")
    if not have("tailscale"):
        log.info("Installing Tailscale (official install.sh)...")
        rc, out = sh(["sh", "-c", "curl -fsSL https://tailscale.com/install.sh | sh"], sudo=True, dry=dry, timeout=600)
        if rc != 0:
            log.error("tailscale install failed: %s", out[-2000:])
            return 1
        log.info("Installed. Authenticate with: sudo tailscale up")
    else:
        log.info("Tailscale already present.")
    log.info("install-deps complete. Next: install-wolf, then smoke.")
    return 0


def cmd_install_wolf(args) -> int:
    dry = args.dry_run
    if not have("docker"):
        log.error("docker missing; run install-deps first")
        return 1
    for d in (WOLF_SOCKET_DIR, WOLF_STATE_DIR):
        if dry:
            log.info("DRY-RUN: mkdir -p %s", d)
        else:
            try:
                d.mkdir(parents=True, exist_ok=True)
            except OSError as exc:
                log.error("Cannot create %s (need root?): %s", d, exc)
                return 1
    log.info("Pulling %s ...", WOLF_IMAGE)
    rc, out = sh(["docker", "pull", WOLF_IMAGE], sudo=True, dry=dry, timeout=1800)
    if rc != 0:
        log.error("pull failed: %s", out[-2000:])
        return 1
    # Idempotent: reuse existing container if present.
    rc, out = sh(["docker", "inspect", "wolf"], dry=dry)
    if rc == 0 and not dry:
        log.info("Wolf container exists; starting it.")
        rc, out = sh(["docker", "start", "wolf"], sudo=True, dry=dry)
        if rc != 0:
            log.error("start failed: %s", out[-2000:])
            return 1
        return 0
    log.info("Creating Wolf container (host network, GPU devices, socket mount)...")
    cmd = ["docker", "run", "-d", "--name", "wolf", "--restart", "unless-stopped",
           "--network=host",
           "-e", f"WOLF_SOCKET_PATH={WOLF_SOCKET_DIR}/wolf.sock",
           "-v", f"{WOLF_STATE_DIR}:/etc/wolf:rw",
           "-v", "/var/run/docker.sock:/var/run/docker.sock:rw",
           "-v", f"{WOLF_SOCKET_DIR}:{WOLF_SOCKET_DIR}",
           "-v", "/dev/:/dev/:rw",
           "-v", "/run/udev:/run/udev:rw",
           "--device", "/dev/dri",
           "--device", "/dev/uinput",
           "--device", "/dev/uhid",
           "--device-cgroup-rule", "c 13:* rmw"]
    if shutil.which("nvidia-ctk") or shutil.which("nvidia-container-cli"):
        cmd += ["--gpus", "all"]
    cmd += [WOLF_IMAGE]
    rc, out = sh(cmd, sudo=True, dry=dry, timeout=300)
    if rc != 0:
        log.error("wolf container failed: %s", out[-3000:])
        log.error("See https://games-on-whales.github.io/wolf/stable/user/quickstart.html")
        return 1
    log.info("Wolf container started. Socket: %s/wolf.sock", WOLF_SOCKET_DIR)
    return 0


def cmd_smoke(_args) -> int:
    failures = []
    rc, out = sh(["nvidia-smi", "--query-gpu=name,driver_version", "--format=csv,noheader"])
    if rc != 0:
        failures.append("nvidia-smi failed")
    else:
        log.info("SMOKE gpu: %s", out.strip().splitlines()[0] if out.strip() else "?")
    rc, out = sh(["docker", "run", "--rm", "--runtime=nvidia", "--gpus", "all",
                  "nvidia/cuda:12.6.0-base-ubuntu22.04", "nvidia-smi",
                  "--query-gpu=name", "--format=csv,noheader"], timeout=600)
    if rc != 0:
        failures.append("docker NVIDIA runtime test failed (nvidia-ctk runtime configure + restart docker)")
    else:
        log.info("SMOKE docker+gpu: %s", (out.strip().splitlines() or ["?"])[0])
    sock = WOLF_SOCKET_DIR / "wolf.sock"
    if not sock.exists():
        failures.append(f"wolf socket missing at {sock}")
    else:
        rc, out = sh(["curl", "--unix-socket", str(sock),
                      "http://localhost/api/v1/apps"], timeout=30)
        if rc != 0 or '"success":true' not in out.replace(" ", ""):
            failures.append("wolf API /api/v1/apps unreachable")
        else:
            log.info("SMOKE wolf API: reachable")
    rc, out = sh(["tailscale", "ip", "--4"], timeout=15)
    if rc != 0 or not out.strip():
        failures.append("tailscale down (sudo tailscale up)")
    else:
        log.info("SMOKE tailscale: %s", out.strip().split()[0])
    if failures:
        for f in failures:
            log.error("SMOKE FAIL: %s", f)
        return 1
    log.info("SMOKE: all live checks passed.")
    return 0


def cmd_all(args) -> int:
    for step in (cmd_check, cmd_install_deps, cmd_install_wolf, cmd_smoke):
        rc = step(args)
        if rc != 0:
            log.error("Stopped at %s.", step.__name__)
            return rc
    return 0


def build_parser():
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--dry-run", action="store_true", help="print install commands without executing")
    p = argparse.ArgumentParser(description="GPU node setup: detect, install, validate Wolf+Tailscale+NVIDIA.",
                                parents=[common])
    p.add_argument("-v", "--verbose", action="store_true")
    sub = p.add_subparsers(dest="command")
    for name, fn, help_text in [
        ("check", cmd_check, "diagnostics only (safe everywhere)"),
        ("install-deps", cmd_install_deps, "base packages, Docker, NVIDIA toolkit, Tailscale"),
        ("install-wolf", cmd_install_wolf, "pull + run Wolf with the node socket"),
        ("smoke", cmd_smoke, "live validation (needs real hardware)"),
        ("all", cmd_all, "check -> install-deps -> install-wolf -> smoke"),
    ]:
        sub.add_parser(name, help=help_text, parents=[common]).set_defaults(func=fn)
    return p


def main(argv=None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    setup_logging(args.verbose)
    if getattr(args, "func", None) is None:
        parser.print_help()
        return 2
    if platform.system() != "Linux" and args.command in ("install-deps", "install-wolf", "smoke", "all"):
        if not args.dry_run:
            log.error("These commands target a Linux GPU node (this machine is %s). `check` still runs.", platform.system())
            return 2
        log.info("Non-Linux host: --dry-run only prints the Linux commands.")
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
