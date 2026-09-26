"""Library page: the primary surface. Cards from GET /v1/games, search, play."""

from __future__ import annotations

from PySide6.QtCore import Qt, Signal
from PySide6.QtWidgets import (
    QGridLayout,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QPushButton,
    QScrollArea,
    QVBoxLayout,
    QWidget,
)

from .. import theme
from ..api import ControlClient, ControlPlaneError, Game, PlaytimeEntry
from ..services.settings import Settings
from ..widgets import EmptyState, GameCard, StatusPill, fmt_seconds


class LibraryPage(QWidget):
    play_requested = Signal(str)
    connection_changed = Signal(bool, str)

    def __init__(self, client: ControlClient, settings: Settings, parent=None) -> None:
        super().__init__(parent)
        self.client = client
        self.settings = settings
        self._games: list[Game] = []
        self._playtime: dict[str, PlaytimeEntry] = {}
        self._playing_id: str = ""

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(theme.SPACE["m"])

        toolbar = QHBoxLayout()
        self.search = QLineEdit()
        self.search.setPlaceholderText("Search games…")
        self.search.setProperty("role", "search")
        self.search.setFixedWidth(280)
        self.search.setClearButtonEnabled(True)
        self.search.textChanged.connect(self._render_cards)
        toolbar.addWidget(self.search)
        toolbar.addStretch(1)
        self.refresh_btn = QPushButton("⟳  Refresh")
        self.refresh_btn.clicked.connect(self.refresh)
        toolbar.addWidget(self.refresh_btn)
        outer.addLayout(toolbar)

        self.feedback = QLabel("")
        self.feedback.setProperty("role", "pageSubtitle")
        self.feedback.setWordWrap(True)
        self.feedback.hide()
        outer.addWidget(self.feedback)

        self.scroll = QScrollArea()
        self.scroll.setWidgetResizable(True)
        self.cards_host = QWidget()
        self.cards_layout = QGridLayout(self.cards_host)
        self.cards_layout.setContentsMargins(0, 0, theme.SPACE["m"], 0)
        self.cards_layout.setSpacing(theme.SPACE["l"])
        self.scroll.setWidget(self.cards_host)
        outer.addWidget(self.scroll, 1)

        self.empty = EmptyState(
            "Your library is empty",
            "Add your first game to start playing. Games you add are stored as "
            "manifests and prepared on your GPU node when you press Play.",
            "＋ Add Game",
            self.scroll,
        )
        self.empty.hide()

    def refresh(self) -> None:
        """Reload library + playtime from the control plane (honest errors)."""
        self.refresh_btn.setEnabled(False)
        try:
            self._games = self.client.games()
        except ControlPlaneError as exc:
            self._games = []
            self.feedback.setText(str(exc))
            self.feedback.show()
            self.connection_changed.emit(False, "control plane unreachable")
            self._render_cards()
            self.refresh_btn.setEnabled(True)
            return
        try:
            entries = self.client.playtime(self.settings.user_id)
            self._playtime = {e.game_id: e for e in entries}
        except ControlPlaneError:
            self._playtime = {}
        self.feedback.hide()
        self.connection_changed.emit(True, "")
        self._render_cards()
        self.refresh_btn.setEnabled(True)

    def _add_clicked_default(self) -> None:  # pragma: no cover - replaced in prod
        pass

    def _render_cards(self) -> None:
        # Clear existing cards.
        while self.cards_layout.count():
            item = self.cards_layout.takeAt(0)
            w = item.widget()
            if w:
                w.deleteLater()
        if not self._games:
            if self.feedback.isHidden():
                self.empty.show()
                self.cards_layout.addWidget(self.empty, 0, 0)
            else:
                self.empty.hide()
            return
        self.empty.hide()
        query = self.search.text().strip().lower()
        shown = 0
        for g in self._games:
            if query and query not in g.name.lower() and query not in g.game_id.lower():
                continue
            entry = self._playtime.get(g.game_id)
            label = fmt_seconds(entry.total_active_seconds) if entry else ""
            card = GameCard(g, label, self.settings)
            card.play_clicked.connect(self.play_requested)
            self.cards_layout.addWidget(card, shown // 4, shown % 4)
            shown += 1
        if shown == 0 and query:
            nothing = QLabel(f"No games match “{self.search.text().strip()}”.")
            nothing.setProperty("role", "pageSubtitle")
            self.cards_layout.addWidget(nothing, 0, 0)

    # ---- Play progress -----------------------------------------------------

    def begin_play(self, game_id: str) -> None:
        self._playing_id = game_id
        self.feedback.setText("Creating session…")
        self.feedback.setStyleSheet(f"color: {theme.ACCENT.name()}; background: transparent;")
        self.feedback.show()

    def play_progress(self, kind: str, text: str, is_error: bool) -> None:
        if self._playing_id and kind not in ("exited", "error") or not self._playing_id:
            color = theme.DANGER if is_error else theme.ACCENT
            self.feedback.setStyleSheet(f"color: {color.name()}; background: transparent;")
            self.feedback.setText(text)
            self.feedback.show()
        if kind in ("error", "exited"):
            self._playing_id = ""
            color = theme.DANGER if is_error else theme.SUCCESS
            self.feedback.setStyleSheet(f"color: {color.name()}; background: transparent;")
            self.feedback.setText(text)
            self.feedback.show()
            # Refresh playtime (server-observed) after a session ends.
            self.refresh()
