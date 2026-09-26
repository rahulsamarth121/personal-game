#!/usr/bin/env python3
"""Build the Go play client binary for the Windows UI.

Development uses `go run ./cmd/play` transparently (needs the Go toolchain).
For a packaged desktop app, build once and ship the binary next to the UI:

    python scripts/build_play_client.py            # -> github/play-client.exe

The UI's PlayClient resolves `play-client.exe` automatically (repo root or
app dir), so users never install Go. Stdlib only, mirrors local/runner style.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent


def main() -> int:
    parser = argparse.ArgumentParser(description="Build the play client binary")
    parser.add_argument("--out", default="", help="output path (default: repo root/play-client[.exe])")
    args = parser.parse_args()

    suffix = ".exe" if os.name == "nt" else ""
    out = Path(args.out) if args.out else REPO / f"play-client{suffix}"
    cmd = ["go", "build", "-o", str(out), "./cmd/play"]
    print("building:", " ".join(cmd), f"(cwd={REPO})")
    proc = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    if proc.returncode != 0:
        print(proc.stderr, file=sys.stderr)
        return 1
    print("built:", out)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
