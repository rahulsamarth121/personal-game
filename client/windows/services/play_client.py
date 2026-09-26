"""Play/pair integration with the existing Go client.

The Go ``cmd/play`` binary is the single implementation of the session flow
(create -> wait assignment/preparation -> READY -> launch Moonlight -> close
session -> print playtime). This UI never re-implements that orchestration:
it launches the binary, parses its staged progress output, and relays honest
state to the window.

Binary resolution order:
  1. ``settings.play_command`` pointing at an explicit executable path
  2. ``play-client.exe`` / ``play.exe`` next to this app (packaged layout)
  3. ``<repo>/play-client.exe`` (helper script builds it there)
  4. ``go run ./cmd/play`` (development fallback; requires the Go toolchain)

Moonlight availability is checked up front so the user gets an honest error
before a session is ever created.
"""

from __future__ import annotations

import os
import re
import shutil
import signal
import subprocess
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]  # .../personal-game/github

# Staged progress lines printed by cmd/play (main.go printStage) plus the
# READY line. Matched against actual stdout, never invented here.
_PROGRESS_PATTERNS = [
    (re.compile(r"session [0-9a-fA-F-]+ on node (\S+) \(state (\w+)\)"), "session_created"),
    (re.compile(r"Node assigned"), "node_assigned"),
    (re.compile(r"Checking game cache"), "preparing"),
    (re.compile(r"READY on (\S+) via (\S+)"), "ready"),
    (re.compile(r"moonlight not found"), "moonlight_missing"),
]


@dataclass
class PlayEvent:
    kind: str           # progress | ready | moonlight_missing | exited | error
    message: str
    host: str = ""
    provider: str = ""


class PlayClient:
    """Launches the existing Go play client as a subprocess."""

    def __init__(self, control_url: str, user_id: str, moonlight_path: str = "",
                 play_command: str = "", api_token: str = "") -> None:
        self.control_url = control_url
        self.user_id = user_id
        self.moonlight_path = moonlight_path
        self.play_command = play_command
        self.api_token = api_token  # forwarded via env, never argv
        self._proc: subprocess.Popen | None = None

    # -- resolution ----------------------------------------------------------

    def _explicit_command(self) -> list[str] | None:
        if not self.play_command or self.play_command == "play":
            return None
        return [self.play_command]

    def resolve_binary(self) -> list[str] | None:
        """The command line that runs the Go play client, or None."""
        explicit = self._explicit_command()
        if explicit:
            return explicit
        here = Path(__file__).resolve().parent
        for base in (here, here.parent.parent.parent):  # packaged dir, repo root
            for name in ("play-client.exe", "play.exe", "play-client", "play"):
                cand = base / name
                if cand.is_file():
                    return [str(cand)]
        for name in ("play-client.exe", "play.exe"):
            found = shutil.which(name)
            if found:
                return [found]
        if shutil.which("go"):
            return ["go", "run", "./cmd/play"]
        return None

    def moonlight_available(self) -> tuple[bool, str]:
        """(found, resolved-or-reason). Mirrors cmd/play's LookPath('moonlight')."""
        if self.moonlight_path:
            p = Path(self.moonlight_path)
            if p.is_file():
                return True, str(p)
            return False, (
                f"Moonlight executable not found at {self.moonlight_path}"
            )
        found = shutil.which("moonlight") or shutil.which("moonlight.exe")
        if found:
            return True, found
        return False, (
            "Moonlight was not found on PATH. Install Moonlight "
            "(moonlight-stream.org), then set its path in Settings."
        )

    # -- process control -----------------------------------------------------

    def start(self, game_id: str, on_event) -> None:
        """Run the play flow for game_id, streaming events to the callback.

        The callback receives PlayEvent objects on the thread this runs on;
        the caller moves them to the UI thread via Qt signals.
        """
        ok, detail = self.moonlight_available()
        if not ok:
            on_event(PlayEvent("error", detail))
            return

        argv = self.resolve_binary()
        if argv is None:
            on_event(PlayEvent(
                "error",
                "The play client was not found. Install the Go toolchain or "
                "place play.exe next to the app."
            ))
            return

        args = list(argv) + [
            "play", "--game", game_id,
            "--control", self.control_url,
            "--user", self.user_id,
        ]
        if self.moonlight_path:
            args += ["--moonlight-bin", self.moonlight_path]

        env = dict(os.environ)
        if self.api_token:
            env["PG_API_TOKEN"] = self.api_token  # env, never argv/logs
        try:
            self._proc = subprocess.Popen(
                args, cwd=str(REPO_ROOT), env=env,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                text=True, bufsize=1,
            )
        except OSError as exc:
            on_event(PlayEvent("error", f"Could not start the play client: {exc}"))
            return

        assert self._proc.stdout is not None
        for line in self._proc.stdout:
            line = line.rstrip()
            if line:
                on_event(self._classify(line))
        code = self._proc.wait()
        self._proc = None
        if code == 0:
            on_event(PlayEvent("exited", "Session closed."))
        else:
            on_event(PlayEvent("error", f"The play client exited with code {code}."))

    def _classify(self, line: str) -> PlayEvent:
        for pattern, kind in _PROGRESS_PATTERNS:
            m = pattern.search(line)
            if not m:
                continue
            if kind == "ready":
                return PlayEvent("ready", "Ready — launching Moonlight.",
                                 host=m.group(1), provider=m.group(2))
            if kind == "moonlight_missing":
                return PlayEvent("moonlight_missing", line)
            return PlayEvent(kind, line)
        return PlayEvent("progress", line)

    def abort(self) -> None:
        """User pressed Stop: interrupt the play client (Ctrl+C equivalent)
        so it closes the session cleanly, like Ctrl+C in a terminal."""
        proc, self._proc = self._proc, None
        if proc and proc.poll() is None:
            try:
                if os.name == "nt":
                    proc.send_signal(signal.CTRL_BREAK_EVENT)
                else:
                    proc.terminate()
            except OSError:
                pass


class PairClient:
    """Delegates first-time Moonlight pairing to the existing Go flow."""

    def __init__(self, control_url: str, moonlight_path: str = "", play_command: str = "",
                 api_token: str = "") -> None:
        self.moonlight_path = moonlight_path
        self.play_command = play_command
        self._client = PlayClient(control_url, "", moonlight_path, play_command, api_token)

    def build_args(self, host: str, pin: str = "") -> list[str] | None:
        argv = self._client.resolve_binary()
        if argv is None:
            return None
        args = list(argv) + ["pair", "--host", host]
        if self.moonlight_path:
            args += ["--moonlight-bin", self.moonlight_path]
        if pin:
            args += ["--pin", pin]
        return args
