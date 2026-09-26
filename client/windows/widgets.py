"""Reusable widgets: status pill, empty states, game cards, toast banner."""

from __future__ import annotations

from PySide6.QtCore import Qt, Signal
from PySide6.QtGui import QPixmap
from PySide6.QtWidgets import (
    QFrame,
    QGraphicsOpacityEffect,
    QHBoxLayout,
    QLabel,
    QPushButton,
    QSizePolicy,
    QVBoxLayout,
    QWidget,
)

from . import theme
from .api import Game
from .services.artwork import load_artwork
from .services.settings import Settings

MODE_BADGES = {
    "archive_installer": "INSTALLER",
    "iso_installer": "ISO INSTALLER",
    "archive_prebuilt": "PREBUILT",
    "direct_prebuilt": "DIRECT EXE",
    "provider": "PROVIDER",
}

# Cache/preparation language: the installed game lives on the node, not on
# this PC — wording reflects that honestly.
def cache_label(unknown: bool = True) -> str:
    return "Ready to stream" if not unknown else "Cache: check on play"


def fmt_bytes(n: int) -> str:
    if n <= 0:
        return ""
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if n < 1024:
            return f"{n:.0f} {unit}" if unit in ("B", "KB") else f"{n:.1f} {unit}"
        n /= 1024.0
    return f"{n:.1f} PB"


def fmt_seconds(total: int) -> str:
    if total <= 0:
        return "0h 00m"
    h, rem = divmod(total, 3600)
    m = rem // 60
    if h:
        return f"{h}h {m:02d}m"
    return f"{m}m"


class StatusPill(QFrame):
    """Connection indicator in the top bar: dot + text."""

    def __init__(self, parent: QWidget | None = None) -> None:
        super().__init__(parent)
        layout = QHBoxLayout(self)
        layout.setContentsMargins(10, 4, 10, 4)
        layout.setSpacing(6)
        self.dot = QLabel("●")
        self.dot.setStyleSheet(f"color: {theme.TEXT_FAINT.name()}; font-size: 10px; background: transparent;")
        self.label = QLabel("Offline")
        self.label.setStyleSheet(
            f"color: {theme.TEXT_DIM.name()}; font-size: 12px; background: transparent;")
        layout.addWidget(self.dot)
        layout.addWidget(self.label)
        self.setStyleSheet(
            f"QFrame {{ background: {theme.SURFACE_ALT.name()}; border: 1px solid {theme.BORDER.name()};"
            f" border-radius: 12px; }}")

    def set_state(self, online: bool, detail: str = "") -> None:
        color = theme.SUCCESS.name() if online else theme.DANGER.name()
        self.dot.setStyleSheet(f"color: {color}; font-size: 10px; background: transparent;")
        text = "Connected" if online else "Offline"
        if detail:
            text = f"{text} — {detail}"
        self.label.setText(text)
        self.label.setToolTip(detail or text)


class EmptyState(QWidget):
    """Friendly empty state with an optional primary action."""

    action_clicked = Signal()

    def __init__(self, title: str, subtitle: str, action_text: str = "",
                 parent: QWidget | None = None) -> None:
        super().__init__(parent)
        lay = QVBoxLayout(self)
        lay.setAlignment(Qt.AlignCenter)
        lay.setSpacing(theme.SPACE["m"])
        icon = QLabel("🎮")
        icon.setAlignment(Qt.AlignCenter)
        icon.setStyleSheet("font-size: 42px; background: transparent;")
        t = QLabel(title)
        t.setAlignment(Qt.AlignCenter)
        t.setProperty("role", "pageTitle")
        s = QLabel(subtitle)
        s.setAlignment(Qt.AlignCenter)
        s.setProperty("role", "pageSubtitle")
        s.setWordWrap(True)
        lay.addWidget(icon)
        lay.addWidget(t)
        lay.addWidget(s)
        if action_text:
            btn = QPushButton(action_text)
            btn.setProperty("role", "accent")
            btn.clicked.connect(self.action_clicked)
            wrap = QHBoxLayout()
            wrap.addStretch(1)
            wrap.addWidget(btn)
            wrap.addStretch(1)
            lay.addLayout(wrap)


class GameCard(QFrame):
    """One library tile: artwork, title, meta, PLAY."""

    play_clicked = Signal(str)
    property_clicked = Signal(str)

    ART_W, ART_H = 264, 148

    def __init__(self, game: Game, playtime_label: str, settings: Settings,
                 parent: QWidget | None = None) -> None:
        super().__init__(parent)
        self.game = game
        self.setProperty("role", "card")
        self.setCursor(Qt.PointingHandCursor)
        self.setSizePolicy(QSizePolicy.Fixed, QSizePolicy.Fixed)
        self.setFixedSize(self.ART_W + 2 * theme.SPACE["l"], 300)

        lay = QVBoxLayout(self)
        lay.setContentsMargins(theme.SPACE["l"], theme.SPACE["l"],
                               theme.SPACE["l"], theme.SPACE["l"])
        lay.setSpacing(theme.SPACE["s"])

        art = QLabel()
        art.setFixedSize(self.ART_W, self.ART_H)
        art.setAlignment(Qt.AlignCenter)
        pm: QPixmap = load_artwork(settings, game.game_id, game.name, self.ART_W, self.ART_H)
        art.setPixmap(pm)
        art.setStyleSheet("border-radius: 8px; background: transparent;")
        lay.addWidget(art)

        title = QLabel(game.name)
        title.setProperty("role", "cardTitle")
        title.setWordWrap(True)
        title.setMaximumHeight(44)
        lay.addWidget(title)

        meta_bits = []
        if game.version:
            meta_bits.append(f"v{game.version}")
        badge = MODE_BADGES.get(game.package_type)
        if badge:
            meta_bits.append(badge)
        if playtime_label:
            meta_bits.append(playtime_label)
        meta = QLabel("  ·  ".join(meta_bits))
        meta.setProperty("role", "cardMeta")
        meta.setWordWrap(True)
        lay.addWidget(meta)

        lay.addStretch(1)

        btn_row = QHBoxLayout()
        self.play_btn = QPushButton("▶  Play")
        self.play_btn.setProperty("role", "accent")
        self.play_btn.clicked.connect(lambda: self.play_clicked.emit(self.game.game_id))
        btn_row.addWidget(self.play_btn, 1)
        lay.addLayout(btn_row)


class Toast(QFrame):
    """Transient banner for honest, non-blocking errors/info."""

    def __init__(self, parent: QWidget) -> None:
        super().__init__(parent)
        self.setProperty("role", "card")
        lay = QHBoxLayout(self)
        lay.setContentsMargins(theme.SPACE["l"], theme.SPACE["m"], theme.SPACE["l"], theme.SPACE["m"])
        self.label = QLabel("")
        self.label.setWordWrap(True)
        self.close_btn = QPushButton("✕")
        self.close_btn.setFixedSize(24, 24)
        self.close_btn.setFlat(True)
        self.close_btn.clicked.connect(self.hide)
        lay.addWidget(self.label, 1)
        lay.addWidget(self.close_btn)
        self.hide()

    def show_message(self, text: str, error: bool = False) -> None:
        self.label.setText(text)
        color = theme.DANGER if error else theme.ACCENT
        self.setStyleSheet(
            f"QFrame[role='card'] {{ background: {color.darker(300).name()};"
            f" border: 1px solid {color.name()}; border-radius: 10px; }}")
        self.show()
        self.raise_()
