"""Settings page: control plane, identity, Moonlight pairing, developer tools."""

from __future__ import annotations

import os
import subprocess
import webbrowser
from pathlib import Path

from PySide6.QtCore import Qt, Signal
from PySide6.QtWidgets import (
    QCheckBox,
    QFileDialog,
    QFormLayout,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QMessageBox,
    QPushButton,
    QVBoxLayout,
    QWidget,
)

from .. import theme
from ..api import DEFAULT_CONTROL_URL, ControlClient
from ..services.play_client import PairClient, REPO_ROOT
from ..services.settings import CONFIG_DIR, Settings

APP_VERSION = "0.1.0"


class SettingsPage(QWidget):
    settings_changed = Signal()

    def __init__(self, client: ControlClient, settings: Settings, parent=None) -> None:
        super().__init__(parent)
        self.client = client
        self.settings = settings
        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(theme.SPACE["l"])

        form = QFormLayout()
        form.setLabelAlignment(Qt.AlignRight)
        form.setSpacing(theme.SPACE["m"])

        self.control_url = QLineEdit(self.settings.control_url)
        self.control_url.setMinimumWidth(360)
        form.addRow("Control plane URL", self.control_url)

        self.user_id = QLineEdit(self.settings.user_id)
        form.addRow("User ID", self.user_id)

        self.api_token = QLineEdit(self.settings.api_token)
        self.api_token.setEchoMode(QLineEdit.Password)
        self.api_token.setPlaceholderText("Only when the control plane requires one")
        form.addRow("API token", self.api_token)

        ml_row = QHBoxLayout()
        self.moonlight_path = QLineEdit(self.settings.moonlight_path)
        self.moonlight_path.setPlaceholderText("Leave empty to use moonlight from PATH")
        browse = QPushButton("Browse…")
        browse.clicked.connect(self._browse_moonlight)
        ml_row.addWidget(self.moonlight_path, 1)
        ml_row.addWidget(browse)
        form.addRow("Moonlight executable", ml_row)

        outer.addLayout(form)

        actions = QHBoxLayout()
        test_btn = QPushButton("Test connection")
        test_btn.clicked.connect(self._test_connection)
        save_btn = QPushButton("Save settings")
        save_btn.setProperty("role", "accent")
        save_btn.clicked.connect(self._save)
        actions.addWidget(test_btn)
        actions.addWidget(save_btn)
        actions.addStretch(1)
        outer.addLayout(actions)

        self.status = QLabel("")
        self.status.setProperty("role", "pageSubtitle")
        outer.addWidget(self.status)

        # ---- Pairing ---------------------------------------------------------
        pair_title = QLabel("Moonlight pairing")
        pair_title.setProperty("role", "cardTitle")
        pair_hint = QLabel(
            "Pair this PC with a GPU node once; Moonlight stores the pairing "
            "credentials itself. Enter the node's address (Tailscale IP).")
        pair_hint.setProperty("role", "pageSubtitle")
        pair_hint.setWordWrap(True)
        pair_row = QHBoxLayout()
        self.pair_host = QLineEdit()
        self.pair_host.setPlaceholderText("e.g. 100.x.y.z")
        self.pair_pin = QLineEdit()
        self.pair_pin.setPlaceholderText("Optional PIN")
        self.pair_pin.setFixedWidth(160)
        pair_btn = QPushButton("Pair…")
        pair_btn.clicked.connect(self._pair)
        pair_row.addWidget(self.pair_host, 1)
        pair_row.addWidget(self.pair_pin)
        pair_row.addWidget(pair_btn)
        outer.addWidget(pair_title)
        outer.addWidget(pair_hint)
        outer.addLayout(pair_row)

        # ---- Developer mode ----------------------------------------------------
        self.dev_box = QWidget()
        dev_lay = QVBoxLayout(self.dev_box)
        dev_lay.setContentsMargins(0, 0, 0, 0)
        hint = QLabel("Diagnostics surface raw data for troubleshooting; "
                      "everything here is read-only.")
        hint.setProperty("role", "pageSubtitle")
        hint.setWordWrap(True)
        dev_row = QHBoxLayout()
        api_btn = QPushButton("Check API health")
        api_btn.clicked.connect(self._test_connection)
        logs_btn = QPushButton("Open config folder")
        logs_btn.clicked.connect(self._open_config_folder)
        dev_row.addWidget(api_btn)
        dev_row.addWidget(logs_btn)
        dev_row.addStretch(1)
        dev_lay.addWidget(hint)
        dev_lay.addLayout(dev_row)
        outer.addWidget(self.dev_box)
        self.dev_box.setVisible(self.settings.developer_mode)

        self.dev_toggle = QCheckBox("Developer mode")
        self.dev_toggle.setChecked(self.settings.developer_mode)
        self.dev_toggle.toggled.connect(self.dev_box.setVisible)
        outer.addWidget(self.dev_toggle)

        about = QLabel(
            f"Personal Game UI {APP_VERSION} — thin client for the Go control plane. "
            "Streaming is handled by Moonlight; saves and sessions stay in the backend.")
        about.setProperty("role", "pageSubtitle")
        about.setWordWrap(True)
        outer.addWidget(about)
        outer.addStretch(1)

    def refresh(self) -> None:  # page protocol
        self.control_url.setText(self.settings.control_url)
        self.user_id.setText(self.settings.user_id)
        self.api_token.setText(self.settings.api_token)
        self.moonlight_path.setText(self.settings.moonlight_path)
        self.dev_toggle.setChecked(self.settings.developer_mode)

    def _browse_moonlight(self) -> None:
        flt = "Executables (*.exe);;All files (*)" if os.name == "nt" else "All files (*)"
        path, _ = QFileDialog.getOpenFileName(self, "Select Moonlight executable", "", flt)
        if path:
            self.moonlight_path.setText(path)

    def _test_connection(self) -> None:
        self.client = ControlClient(
            self.control_url.text().strip() or self.settings.control_url,
            api_token=self.api_token.text().strip())
        if self.client.health():
            self.status.setText(f"✓ Control plane reachable at {self.client.base_url}")
            self.status.setStyleSheet(f"color: {theme.SUCCESS.name()}; background: transparent;")
        else:
            self.status.setText(f"✗ No response from {self.client.base_url} — is it running?")
            self.status.setStyleSheet(f"color: {theme.DANGER.name()}; background: transparent;")

    def _save(self) -> None:
        url = self.control_url.text().strip() or DEFAULT_CONTROL_URL
        if not url.startswith(("http://", "https://")):
            QMessageBox.warning(self, "Settings",
                                "Control plane URL must start with http:// or https://")
            return
        self.settings.control_url = url
        self.settings.user_id = self.user_id.text().strip() or "player1"
        self.settings.api_token = self.api_token.text().strip()
        self.settings.moonlight_path = self.moonlight_path.text().strip()
        self.settings.developer_mode = self.dev_toggle.isChecked()
        try:
            self.settings.save()
        except OSError as exc:
            QMessageBox.critical(self, "Settings", f"Could not save settings: {exc}")
            return
        self.settings_changed.emit()

    def _pair(self) -> None:
        host = self.pair_host.text().strip()
        if not host:
            QMessageBox.warning(self, "Pairing", "Enter the node address first (its Tailscale IP).")
            return
        pairer = PairClient(self.settings.control_url, self.settings.moonlight_path,
                            self.settings.play_command, self.settings.api_token)
        args = pairer.build_args(host, self.pair_pin.text().strip())
        if args is None:
            QMessageBox.critical(self, "Pairing",
                                 "The play client was not found; pairing needs it to invoke Moonlight.")
            return
        try:
            proc = subprocess.run(args, capture_output=True, text=True, timeout=180,
                                  cwd=str(REPO_ROOT))
        except subprocess.TimeoutExpired:
            QMessageBox.warning(self, "Pairing",
                                "Pairing timed out. Approve the PIN on the host, then retry.")
            return
        if proc.returncode == 0:
            QMessageBox.information(self, "Pairing", f"Paired with {host}.")
        else:
            detail = ((proc.stdout or "") + (proc.stderr or "")).strip()[-500:]
            QMessageBox.warning(self, "Pairing", f"Pairing failed:\n{detail or 'unknown error'}")

    def _open_config_folder(self) -> None:
        CONFIG_DIR.mkdir(parents=True, exist_ok=True)
        if os.name == "nt":
            os.startfile(str(CONFIG_DIR))  # noqa: S606 - explicit user action
        else:
            webbrowser.open(Path(CONFIG_DIR).as_uri())
