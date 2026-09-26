"""History page: server-observed playtime, never client-computed."""

from __future__ import annotations

from PySide6.QtWidgets import (
    QHBoxLayout,
    QLabel,
    QPushButton,
    QScrollArea,
    QVBoxLayout,
    QWidget,
)

from .. import theme
from ..api import ControlClient, ControlPlaneError
from ..services.settings import Settings
from ..widgets import EmptyState, fmt_seconds


class HistoryPage(QWidget):
    def __init__(self, client: ControlClient, settings: Settings, parent=None) -> None:
        super().__init__(parent)
        self.client = client
        self.settings = settings

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(theme.SPACE["m"])

        bar = QHBoxLayout()
        bar.addStretch(1)
        self.refresh_btn = QPushButton("⟳  Refresh")
        self.refresh_btn.clicked.connect(self.refresh)
        bar.addWidget(self.refresh_btn)
        outer.addLayout(bar)

        self.feedback = QLabel("")
        self.feedback.setProperty("role", "pageSubtitle")
        self.feedback.setWordWrap(True)
        self.feedback.hide()
        outer.addWidget(self.feedback)

        self.scroll = QScrollArea()
        self.scroll.setWidgetResizable(True)
        host = QWidget()
        self.rows = QVBoxLayout(host)
        self.rows.setContentsMargins(0, 0, theme.SPACE["m"], 0)
        self.rows.setSpacing(theme.SPACE["s"])
        self.scroll.setWidget(host)
        outer.addWidget(self.scroll, 1)

        self.empty = EmptyState(
            "No playtime yet",
            "Sessions you play are recorded by the control plane — this page "
            "shows exactly what the server observed.",
            "",
            self.scroll,
        )
        self.empty.hide()

    def refresh(self) -> None:
        self.refresh_btn.setEnabled(False)
        while self.rows.count():
            item = self.rows.takeAt(0)
            w = item.widget()
            if w:
                w.deleteLater()
        try:
            entries = self.client.playtime(self.settings.user_id)
        except ControlPlaneError as exc:
            self.feedback.setText(str(exc))
            self.feedback.show()
            self.refresh_btn.setEnabled(True)
            return
        self.feedback.hide()
        if not entries:
            self.rows.addWidget(self.empty)
            self.empty.show()
            self.refresh_btn.setEnabled(True)
            return
        self.empty.hide()
        header_bits = ("Game", "Last played", "Sessions", "Active time")
        header = QLabel("   ".join(f"{h:<22}" if i == 0 else f"{h:<12}" for i, h in enumerate(header_bits)))
        header.setStyleSheet(
            f"color: {theme.TEXT_FAINT.name()}; font-size: 11px; font-weight: 600;"
            " background: transparent; letter-spacing: 1px;")
        self.rows.addWidget(header)
        for e in sorted(entries, key=lambda x: x.last_played or "", reverse=True):
            row = QWidget()
            row.setProperty("role", "card")
            lay = QHBoxLayout(row)
            lay.setContentsMargins(theme.SPACE["l"], theme.SPACE["m"],
                                   theme.SPACE["l"], theme.SPACE["m"])
            name = QLabel(e.game_id)
            name.setProperty("role", "cardTitle")
            last = QLabel(e.last_played or "—")
            last.setProperty("role", "cardMeta")
            count = QLabel(str(e.sessions))
            count.setProperty("role", "cardMeta")
            total = QLabel(fmt_seconds(e.total_active_seconds))
            total.setProperty("role", "cardMeta")
            for w, stretch in ((name, 3), (last, 2), (count, 1), (total, 1)):
                lay.addWidget(w, stretch)
            self.rows.addWidget(row)
        self.rows.addStretch(1)
        self.refresh_btn.setEnabled(True)
