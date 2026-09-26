"""Game card artwork.

Two sources, in order:
  1. a user-provided override (Settings -> artwork folder, <game-id>.png/jpg)
  2. a deterministic generated placeholder derived from the game title

No web scraping, no bundled copyrighted assets. The placeholder is a clean
two-tone gradient with the game's initials, hashed from the game id so every
game gets a stable, distinct color pair.
"""

from __future__ import annotations

import hashlib
import os
from pathlib import Path

from PySide6.QtCore import QRectF, Qt
from PySide6.QtGui import (
    QBrush,
    QColor,
    QFont,
    QLinearGradient,
    QPainter,
    QPainterPath,
    QPixmap,
    QPen,
)

from .settings import Settings, artwork_overrides

# Curated dark-friendly color pairs (top, bottom). Neutral identity.
_PALETTES = [
    ("#2f4550", "#1b2a32"),
    ("#3b2f50", "#221a32"),
    ("#274a3d", "#152b23"),
    ("#4a3b2f", "#2b2019"),
    ("#2f3b4a", "#19222b"),
    ("#4a2f3b", "#2b1922"),
    ("#3d4a2f", "#232b15"),
    ("#443355", "#241a2e"),
]


def _palette_for(game_id: str) -> tuple[str, str]:
    digest = hashlib.sha256(game_id.encode("utf-8")).digest()
    return _PALETTES[digest[0] % len(_PALETTES)]


def initials(name: str) -> str:
    words = [w for w in (name or "?").split() if w]
    if not words:
        return "?"
    if len(words) == 1:
        return words[0][:2].upper()
    return (words[0][0] + words[1][0]).upper()


def placeholder_pixmap(name: str, game_id: str, width: int, height: int) -> QPixmap:
    """Deterministic artwork for a game (same inputs -> same pixels)."""
    top, bottom = _palette_for(game_id or name)
    pm = QPixmap(width, height)
    pm.fill(Qt.transparent)
    p = QPainter(pm)
    p.setRenderHint(QPainter.Antialiasing)

    grad = QLinearGradient(0.0, 0.0, float(width), float(height))
    grad.setColorAt(0.0, QColor(top))
    grad.setColorAt(1.0, QColor(bottom))
    p.setBrush(QBrush(grad))
    p.setPen(Qt.NoPen)
    p.drawRoundedRect(QRectF(0, 0, width, height), 10, 10)

    # Faint diagonal sheen for depth (subtle, not gaudy).
    sheen = QLinearGradient(0.0, float(height), float(width), 0.0)
    sheen.setColorAt(0.0, QColor(255, 255, 255, 0))
    sheen.setColorAt(1.0, QColor(255, 255, 255, 14))
    p.setBrush(QBrush(sheen))
    p.drawRoundedRect(QRectF(0, 0, width, height), 10, 10)

    # Initials medallion.
    text = initials(name)
    path = QPainterPath()
    font = QFont("Segoe UI", int(height * 0.26))
    font.setBold(True)
    font.setLetterSpacing(QFont.PercentageSpacing, 104)
    path.addText(0, 0, font, text)
    br = path.boundingRect()
    cx, cy = width / 2, height / 2
    p.setPen(QPen(QColor(255, 255, 255, 200), 1.4))
    p.setBrush(QColor(255, 255, 255, 22))
    p.drawEllipse(QRectF(cx - br.width() * 0.62 - 14, cy - br.width() * 0.62 - 14,
                         br.width() * 1.24 + 28, br.width() * 1.24 + 28))
    p.setPen(QPen(QColor(240, 244, 247)))
    p.setBrush(Qt.NoBrush)
    p.drawPath(path.translated(cx - br.center().x(), cy - br.center().y()))
    p.end()
    return pm


def load_artwork(settings: Settings, game_id: str, name: str,
                 width: int, height: int) -> QPixmap:
    """Override image when present, else the generated placeholder."""
    for path in artwork_overrides(settings).get(game_id, "").split("\x00"):
        if path and os.path.isfile(path):
            pm = QPixmap(path)
            if not pm.isNull():
                return pm.scaled(width, height, Qt.KeepAspectRatioByExpanding,
                                 Qt.SmoothTransformation)
    return placeholder_pixmap(name, game_id, width, height)


def artwork_dir_candidates(settings: Settings) -> list[Path]:
    out = []
    if settings.artwork_dir:
        out.append(Path(settings.artwork_dir))
    return out
