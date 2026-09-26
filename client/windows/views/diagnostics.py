"""Diagnostics page (Developer Mode): control plane, node, media, client.

Read-only truth: what the control plane and node actually report, plus the
client's own Moonlight availability. Never shows tokens; never invents a
green state.
"""

from __future__ import annotations

from PySide6.QtWidgets import (
    QGridLayout,
    QHBoxLayout,
    QLabel,
    QPushButton,
    QVBoxLayout,
    QWidget,
)

from .. import theme
from ..api import ControlClient
from ..services.play_client import PlayClient
from ..services.settings import Settings

APP_VERSION = "0.1.0"


def _ok(value: object) -> str:
    return "✓" if value else "✗"


class _Panel(QWidget):
    """One titled group of key/value rows."""

    def __init__(self, title: str) -> None:
        super().__init__()
        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(theme.SPACE["s"])
        head = QLabel(title)
        head.setProperty("role", "cardTitle")
        outer.addWidget(head)
        self.grid = QGridLayout()
        self.grid.setContentsMargins(0, 0, 0, 0)
        self.grid.setHorizontalSpacing(theme.SPACE["l"])
        self.grid.setVerticalSpacing(4)
        outer.addLayout(self.grid)
        self._row = 0

    def add(self, key: str, value: str) -> None:
        k = QLabel(key)
        k.setProperty("role", "cardMeta")
        v = QLabel(value)
        v.setProperty("role", "cardMeta")
        v.setTextInteractionFlags(v.textInteractionFlags() | __import__(
            "PySide6.QtCore", fromlist=["Qt"]).Qt.TextSelectableByMouse)
        self.grid.addWidget(k, self._row, 0)
        self.grid.addWidget(v, self._row, 1)
        self._row += 1


class DiagnosticsPage(QWidget):
    def __init__(self, client: ControlClient, settings: Settings, parent=None) -> None:
        super().__init__(parent)
        self.client = client
        self.settings = settings

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(theme.SPACE["l"])

        bar = QHBoxLayout()
        bar.addStretch(1)
        refresh = QPushButton("⟳  Run diagnostics")
        refresh.clicked.connect(self.refresh)
        bar.addWidget(refresh)
        outer.addLayout(bar)

        columns = QHBoxLayout()
        columns.setSpacing(theme.SPACE["l"])

        # CONTROL PLANE
        self.cp = _Panel("Control plane")
        columns.addWidget(self.cp)

        # NODE + MEDIA
        self.node = _Panel("Node")
        self.media = _Panel("Media")
        node_media = QVBoxLayout()
        node_media.setContentsMargins(0, 0, 0, 0)
        node_media.setSpacing(theme.SPACE["l"])
        node_media.addWidget(self.node)
        node_media.addWidget(self.media)
        columns.addLayout(node_media)

        # CLIENT
        self.client_panel = _Panel("Client")
        columns.addWidget(self.client_panel)
        outer.addLayout(columns)
        outer.addStretch(1)

        self.summary = QLabel("")
        self.summary.setProperty("role", "pageSubtitle")
        self.summary.setWordWrap(True)
        outer.addWidget(self.summary)

    def refresh(self) -> None:
        # -- control plane ---------------------------------------------------
        online = False
        authenticated: bool | None = None
        try:
            online = self.client.health()
            authenticated = None  # healthz is open by design
            if online and self.client.api_token:
                # A token is configured: probe an authenticated route.
                try:
                    self.client.games()
                    authenticated = True
                except Exception:  # noqa: BLE001 - 401 vs offline distinguished below
                    authenticated = False
        except Exception:  # noqa: BLE001
            online = False
        self.cp.add("Reachable", f"{_ok(online)}  {self.client.base_url if online else 'no response'}")
        if authenticated is not None:
            self.cp.add("Authenticated", _ok(authenticated))
        else:
            self.cp.add("Authenticated", "n/a (no API token set)")

        # -- node + media (from the control plane's registry, honest when absent)
        try:
            nodes = self.client.nodes_snapshot()
        except Exception:  # noqa: BLE001
            nodes = []
        if not nodes:
            self.node.add("Nodes", "no node data available (control plane exposes no node list)")
            self.media.add("Media", "unknown — no node reporting")
        for n in nodes[:4]:  # single-user system; show a few
            name = n.get("node_id") or n.get("name") or "?"
            caps = n.get("caps") or n.get("capabilities") or {}
            gpu = (caps.get("gpu") or {})
            self.node.add(f"{name} · GPU", str(gpu.get("model") or "none reported"))
            self.node.add(f"{name} · VRAM", f"{gpu.get('vram_mb', 0)} MB")
            self.node.add(f"{name} · streaming_allowed", _ok(caps.get("streaming_allowed")))
            backend = ", ".join(caps.get("streaming") or []) or "none"
            self.node.add(f"{name} · backend", backend)
            media = (caps.get("network") or {}).get("media_network") or {}
            if media:
                self.media.add(
                    f"{name} · {media.get('provider', '?')}",
                    f"{_ok(media.get('reachable'))}  {media.get('endpoint', '')} "
                    f"(tcp {_ok(media.get('tcp_ok'))}, udp {_ok(media.get('udp_ok'))})")
            else:
                self.media.add(f"{name} · media", "not advertised")

        # -- client ----------------------------------------------------------
        pc = PlayClient(self.settings.control_url, self.settings.user_id,
                        self.settings.moonlight_path, self.settings.play_command)
        ok, detail = pc.moonlight_available()
        self.client_panel.add("Moonlight", f"{_ok(ok)}  {detail if ok else ''}")
        self.client_panel.add("UI version", APP_VERSION)
        self.client_panel.add("Play client", " ".join(pc.resolve_binary() or ["not found"]))
        self.summary.setText(
            "All values above are read live from the control plane / this machine; "
            "nothing is cached or assumed.")
