"""Generate github/kaggle/notebook/personal_game.ipynb deterministically.

Regenerate with:  python scripts/make_kaggle_notebook.py
Guarantees valid notebook JSON (avoids hand-editing escapes).
"""
import json
from pathlib import Path

NB = Path(__file__).resolve().parent.parent / "kaggle" / "notebook" / "personal_game.ipynb"

# Absolute runner path inside the Kaggle checkout (POSIX, matches Cell 1).
RUNNER_PATH = "/kaggle/working/personal-game-work/repo/kaggle/runner.py"

MD = """# Personal Game — Kaggle node

Thin wrapper around the one canonical runner — `kaggle/runner.py` inside this
repository (`personal-game/github/kaggle/runner.py` in the project layout).
All logic lives there; this notebook only bootstraps a fresh kernel.

**One-time setup (Kaggle UI):**
1. Add Kaggle **Secrets** (Add-ons → Secrets) — never hardcode credentials:
   - `GITHUB_TOKEN` — fine-grained PAT with **read** access to the private
     repo `rahulsamarth121/personal-game` (Contents: Read).
   - `NODE_ENROLLMENT_TOKEN`, `PG_API_TOKEN` (if the plane requires one).
   - Optional overrides: `CONTROL_PLANE_URL`, `NODE_NAME`,
     `STREAMING_ALLOWED`, `MEDIA_NETWORK`, `MEDIA_ENDPOINT`.
2. Enable GPU only if you intend to diagnose GPU capabilities.

**Flow:** fresh kernel → Cell 1 loads Secrets and safely clones the repo
(token served through a temporary git askpass helper — never in URLs,
command lines, or printed output) → every runner command runs as a real
**subprocess** (`subprocess.run([sys.executable, runner, cmd], check=True)`),
so the runner parses only its own CLI arguments — never the kernel's
`sys.argv` (which holds a `kernel-*.json` connection file). No `%run`, no
in-process execution; all logic stays in the one canonical runner.

**Streaming policy:** this environment advertises `STREAMING_ALLOWED=false`
by default — Kaggle's AUP forbids game streaming, so the scheduler will
never send a game session here. It is for diagnostics/lifecycle/capability
testing. `STREAMING_ALLOWED=true` only with explicit provider permission;
there is no bypass.

**Control plane (default):** the Personal Game Cloudflare edge relay
`https://personal-game-relay.rahul-zed-relay-84739261.workers.dev/personal-game`
(control HTTP only — Moonlight media never goes through it).
"""

CELL1 = """# CELL 1 — Bootstrap a fresh kernel: load Kaggle Secrets into env, safely
# clone the private repository (token via a git askpass helper — never in
# URLs, arguments, or output), and point the canonical runner at it.
import os
import subprocess
from pathlib import Path

REPO_URL = "https://github.com/rahulsamarth121/personal-game.git"
WORK_ROOT = Path("/kaggle/working/personal-game-work")
REPO_DIR = WORK_ROOT / "repo"          # where kaggle/runner.py expects it
RUNNER = REPO_DIR / "kaggle" / "runner.py"   # the canonical runner

# --- 1. Secrets -> environment (never printed, never hardcoded) ----------
try:
    from kaggle_secrets import UserSecretsClient
    _secrets = UserSecretsClient()
except Exception:
    _secrets = None  # not on Kaggle — plain os.environ still works
for key in ("GITHUB_TOKEN", "NODE_ENROLLMENT_TOKEN", "PG_API_TOKEN",
            "CONTROL_PLANE_URL", "NODE_NAME", "STREAMING_ALLOWED",
            "MEDIA_NETWORK", "MEDIA_ENDPOINT"):
    if _secrets is not None:
        try:
            val = _secrets.get_secret(key)
            if val:
                os.environ[key] = val
        except Exception:
            pass  # secret not defined — the runner reports what is missing

# --- 2. Clone if absent (authenticated safely, token removed after) ------
if not (REPO_DIR / ".git").exists():
    token = os.environ.get("GITHUB_TOKEN", "")
    if not token:
        raise SystemExit("GITHUB_TOKEN secret is not set. Add it via Kaggle "
                         "Add-ons -> Secrets (fine-grained PAT, read access "
                         "to the private repo).")
    tokfile = Path("/tmp/.pg_tok")
    askpass = Path("/tmp/.pg_askpass.sh")
    tokfile.write_text(token)
    tokfile.chmod(0o600)
    askpass.write_text(
        "#!/bin/sh\\n"
        "case \\"$1\\" in Username*) echo personal-game ;; *) cat /tmp/.pg_tok ;; esac\\n")
    askpass.chmod(0o755)
    env = dict(os.environ, GIT_ASKPASS=str(askpass), GIT_TERMINAL_PROMPT="0")
    REPO_DIR.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["git", "clone", "--depth", "1", REPO_URL, str(REPO_DIR)],
                   env=env, check=True)
    tokfile.unlink()
    askpass.unlink()  # token material removed immediately after the clone
else:
    print("Repository already present:", REPO_DIR)

# --- 3. Point the canonical runner at this checkout -----------------------
os.environ["WORK_ROOT"] = str(WORK_ROOT)
os.environ.setdefault("GITHUB_REPO_URL", REPO_URL)
assert RUNNER.exists(), "canonical runner missing: " + str(RUNNER)
print("Bootstrap OK — runner:", RUNNER)
"""

CELL2 = """# CELL 2 — Diagnostics: the canonical runner runs as a REAL SUBPROCESS,
# so argparse parses only the arguments in this list — never Jupyter's own
# kernel sys.argv (which contains a kernel-*.json connection file).
import subprocess
import sys
subprocess.run([sys.executable, "{RUNNER}", "diagnostics"], check=True)""".format(RUNNER=RUNNER_PATH)

CELL3 = """# CELL 3 — Setup (Go toolchain check + shared agent build, idempotent),
# again as a real subprocess with only its own CLI arguments.
import subprocess
import sys
subprocess.run([sys.executable, "{RUNNER}", "setup"], check=True)""".format(RUNNER=RUNNER_PATH)

CELL4 = """# CELL 4 — Run the shared node agent (foreground). Real subprocess; the
# runner handles Ctrl+C itself — interrupt the cell to stop the node.
import subprocess
import sys
subprocess.run([sys.executable, "{RUNNER}", "run"], check=True)""".format(RUNNER=RUNNER_PATH)

CELL5 = """# CELL 5 — Cleanup (ephemeral staging only, never saves).
import subprocess
import sys
subprocess.run([sys.executable, "{RUNNER}", "cleanup"], check=True)""".format(RUNNER=RUNNER_PATH)


def code(src: str) -> dict:
    return {"cell_type": "code", "metadata": {}, "execution_count": None,
            "outputs": [], "source": src}


nb = {
    "cells": [
        {"cell_type": "markdown", "metadata": {}, "source": MD},
        code(CELL1),
        code(CELL2),
        code(CELL3),
        code(CELL4),
        code(CELL5),
    ],
    "metadata": {
        "kernelspec": {"display_name": "Python 3", "language": "python", "name": "python3"},
        "language_info": {"name": "python"},
    },
    "nbformat": 4,
    "nbformat_minor": 5,
}

# Normalize line endings inside sources (the generator file itself may be
# saved with CRLF; notebook JSON should carry plain \n).
for cell in nb["cells"]:
    if isinstance(cell.get("source"), str):
        cell["source"] = cell["source"].replace("\r\n", "\n")

NB.write_text(json.dumps(nb, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
print("wrote", NB)
