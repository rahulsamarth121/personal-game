"""Visual identity for the launcher.

A dark, calm, spacious look: deep slate surfaces, one accent, generous
whitespace. Original identity — inspired by the *category* of modern
launchers, not by any product's branding.
"""

from __future__ import annotations

from PySide6.QtGui import QColor, QFont, QPalette
from PySide6.QtCore import Qt

# Core palette ---------------------------------------------------------------

BG = QColor("#12161c")          # window
SURFACE = QColor("#171c24")     # sidebar / cards
SURFACE_ALT = QColor("#1c232d") # inputs
BORDER = QColor("#27303c")
TEXT = QColor("#e8edf2")
TEXT_DIM = QColor("#9aa7b4")
TEXT_FAINT = QColor("#6d7a87")
ACCENT = QColor("#4f8cff")      # single brand accent
ACCENT_HOVER = QColor("#6da0ff")
ACCENT_PRESSED = QColor("#3f77e0")
SUCCESS = QColor("#3ecf8e")
WARNING = QColor("#f5b454")
DANGER = QColor("#f06a6a")

FONT_FAMILY_CANDIDATES = ["Segoe UI Variable Display", "Segoe UI", "Inter", "System-ui"]

# 4pt spacing system
SPACE = {"xs": 4, "s": 8, "m": 12, "l": 16, "xl": 24, "xxl": 32, "xxxl": 48}
RADIUS = 10


def app_font() -> QFont:
    for family in FONT_FAMILY_CANDIDATES:
        f = QFont(family)
        if f.exactMatch():
            return f
    return QFont()


def apply_theme(app) -> None:
    """Apply palette + stylesheet to a QApplication."""
    app.setStyle("Fusion")
    palette = QPalette()
    palette.setColor(QPalette.Window, BG)
    palette.setColor(QPalette.WindowText, TEXT)
    palette.setColor(QPalette.Base, SURFACE_ALT)
    palette.setColor(QPalette.AlternateBase, SURFACE)
    palette.setColor(QPalette.Text, TEXT)
    palette.setColor(QPalette.Button, SURFACE_ALT)
    palette.setColor(QPalette.ButtonText, TEXT)
    palette.setColor(QPalette.ToolTipBase, QColor("#1f2630"))
    palette.setColor(QPalette.ToolTipText, TEXT)
    palette.setColor(QPalette.Highlight, ACCENT)
    palette.setColor(QPalette.HighlightedText, QColor("#ffffff"))
    palette.setColor(QPalette.PlaceholderText, TEXT_FAINT)
    palette.setColor(QPalette.Disabled, QPalette.Text, TEXT_FAINT)
    palette.setColor(QPalette.Disabled, QPalette.ButtonText, TEXT_FAINT)
    app.setPalette(palette)
    app.setStyleSheet(stylesheet())


def stylesheet() -> str:
    def rgb(c: QColor) -> str:
        return f"rgb({c.red()},{c.green()},{c.blue()})"

    return f"""
    QWidget {{
        background: {rgb(BG)};
        color: {rgb(TEXT)};
        font-family: "Segoe UI Variable Display", "Segoe UI", "Inter", sans-serif;
        font-size: 13px;
    }}
    QLabel {{ background: transparent; }}
    QLabel[role="pageTitle"] {{ font-size: 24px; font-weight: 600; }}
    QLabel[role="pageSubtitle"] {{ color: {rgb(TEXT_DIM)}; font-size: 13px; }}
    QLabel[role="cardTitle"] {{ font-size: 15px; font-weight: 600; background: transparent; }}
    QLabel[role="cardMeta"] {{ color: {rgb(TEXT_DIM)}; font-size: 12px; background: transparent; }}
    QLabel[role="badge"] {{
        color: {rgb(TEXT_DIM)}; font-size: 11px; font-weight: 600;
        background: {rgb(SURFACE_ALT)}; border: 1px solid {rgb(BORDER)};
        border-radius: 6px; padding: 2px 8px;
    }}

    QPushButton {{ background: {rgb(SURFACE_ALT)}; border: 1px solid {rgb(BORDER)};
        border-radius: 8px; padding: 8px 16px; font-weight: 500; }}
    QPushButton:hover {{ background: {rgb(QColor("#232c38"))}; border-color: {rgb(QColor("#33404f"))}; }}
    QPushButton:pressed {{ background: {rgb(QColor("#1a2129"))}; }}
    QPushButton:disabled {{ color: {rgb(TEXT_FAINT)}; }}

    QPushButton[role="accent"] {{
        background: {rgb(ACCENT)}; border: none; color: white; font-weight: 600;
    }}
    QPushButton[role="accent"]:hover {{ background: {rgb(ACCENT_HOVER)}; }}
    QPushButton[role="accent"]:pressed {{ background: {rgb(ACCENT_PRESSED)}; }}
    QPushButton[role="accent"]:disabled {{ background: {rgb(QColor("#2a3542"))}; color: {rgb(TEXT_FAINT)}; }}

    QPushButton[role="danger"] {{ color: {rgb(DANGER)}; }}

    QPushButton[role="nav"] {{
        background: transparent; border: none; border-radius: 8px;
        padding: 10px 14px; text-align: left; font-size: 14px; color: {rgb(TEXT_DIM)};
    }}
    QPushButton[role="nav"]:hover {{ background: {rgb(QColor("#1d242e"))}; color: {rgb(TEXT)}; }}
    QPushButton[role="nav"]:checked {{ background: {rgb(QColor("#232c38"))}; color: {rgb(TEXT)}; font-weight: 600; }}

    QLineEdit, QComboBox, QSpinBox, QPlainTextEdit, QTextEdit {{
        background: {rgb(SURFACE_ALT)}; border: 1px solid {rgb(BORDER)};
        border-radius: 8px; padding: 8px 10px;
        selection-background-color: {rgb(ACCENT)};
    }}
    QLineEdit:focus, QComboBox:focus, QSpinBox:focus, QPlainTextEdit:focus {{
        border-color: {rgb(ACCENT)};
    }}
    QLineEdit[role="search"] {{ border-radius: 16px; padding: 8px 14px; }}

    QPlainTextEdit[role="sources"] {{
        font-family: "Consolas", "Cascadia Mono", monospace; font-size: 12px;
        line-height: 1.5;
    }}

    QCheckBox {{ spacing: 8px; }}
    QCheckBox::indicator {{
        width: 16px; height: 16px; border-radius: 4px;
        border: 1px solid {rgb(BORDER)}; background: {rgb(SURFACE_ALT)};
    }}
    QCheckBox::indicator:checked {{ background: {rgb(ACCENT)}; border-color: {rgb(ACCENT)}; }}

    QScrollArea {{ border: none; background: transparent; }}
    QWidget[role="card"] {{
        background: {rgb(SURFACE)}; border: 1px solid {rgb(BORDER)};
        border-radius: 12px;
    }}
    QWidget[role="card"]:hover {{ border-color: {rgb(QColor("#3a4756"))}; }}
    QFrame[role="separator"] {{ background: {rgb(BORDER)}; max-height: 1px; border: none; }}
    QListWidget, QTableWidget {{
        background: transparent; border: none;
    }}
    QScrollBar:vertical {{ background: transparent; width: 10px; margin: 4px; }}
    QScrollBar::handle:vertical {{ background: {rgb(QColor("#2b3542"))}; border-radius: 5px; min-height: 30px; }}
    QScrollBar::handle:vertical:hover {{ background: {rgb(QColor("#3a4756"))}; }}
    QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {{ height: 0; }}
    QScrollBar:horizontal {{ background: transparent; height: 10px; margin: 4px; }}
    QScrollBar::handle:horizontal {{ background: {rgb(QColor("#2b3542"))}; border-radius: 5px; min-width: 30px; }}
    QScrollBar::add-line:horizontal, QScrollBar::sub-line:horizontal {{ width: 0; }}

    QProgressBar {{
        background: {rgb(SURFACE_ALT)}; border: none; border-radius: 6px;
        height: 12px; text-align: center; color: transparent;
    }}
    QProgressBar::chunk {{ background: {rgb(ACCENT)}; border-radius: 6px; }}

    QToolTip {{
        background: {rgb(QColor("#1f2630"))}; color: {rgb(TEXT)};
        border: 1px solid {rgb(BORDER)}; border-radius: 6px; padding: 6px 8px;
    }}

    QDialog {{ background: {rgb(BG)}; }}
    QTabWidget::pane {{ border: 1px solid {rgb(BORDER)}; border-radius: 8px; }}
    QTabBar::tab {{
        background: transparent; color: {rgb(TEXT_DIM)}; padding: 8px 14px;
    }}
    QTabBar::tab:selected {{ color: {rgb(TEXT)}; border-bottom: 2px solid {rgb(ACCENT)}; }}
    """
