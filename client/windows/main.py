#!/usr/bin/env python3
"""Personal Game — Windows launcher entry point.

Thin client for the existing personal-game Go control plane. Run from the
repository:

    python client/windows/main.py

Optional flags: --control <url> overrides the saved control-plane URL.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

# Allow running this file directly (python client/windows/main.py) while the
# rest of the UI imports itself as the client.windows package.
_REPO = Path(__file__).resolve().parents[2]  # .../personal-game/github
if str(_REPO) not in sys.path:
    sys.path.insert(0, str(_REPO))


def main() -> int:
    parser = argparse.ArgumentParser(prog="personal-game-ui")
    parser.add_argument("--control", default=None, help="control plane base URL")
    args, _qt_args = parser.parse_known_args()

    from PySide6.QtWidgets import QApplication

    from client.windows.app import MainWindow
    from client.windows.services.settings import Settings
    from client.windows.theme import apply_theme

    qt_app = QApplication(sys.argv)
    qt_app.setApplicationName("Personal Game")
    qt_app.setOrganizationName("personal-game")
    apply_theme(qt_app)

    settings = Settings.load()
    if args.control:
        settings.control_url = args.control

    window = MainWindow(settings)
    window.show()
    return qt_app.exec()


if __name__ == "__main__":
    raise SystemExit(main())
