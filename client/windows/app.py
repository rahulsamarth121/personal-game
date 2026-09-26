"""Main window: sidebar navigation, page host, top bar, play orchestration."""

from __future__ import annotations

from PySide6.QtCore import Qt, QThread, QTimer, Signal
from PySide6.QtWidgets import (
    QApplication,
    QButtonGroup,
    QFrame,
    QHBoxLayout,
    QLabel,
    QMainWindow,
    QMessageBox,
    QPushButton,
    QStackedWidget,
    QVBoxLayout,
    QWidget,
)

from . import theme
from .api import ControlClient, ControlPlaneError
from .models import AddGameSpec
from .services.play_client import PlayClient, PlayEvent
from .services.settings import Settings
from .views.add_game import AddGameWizard
from .views.diagnostics import DiagnosticsPage
from .views.history import HistoryPage
from .views.library import LibraryPage
from .views.settings import SettingsPage
from .widgets import StatusPill, Toast

APP_TITLE = "Personal Game"


class _PlayWorker(QThread):
    """Runs the Go play client on a worker thread; events hop to the UI
    thread through the Qt signal."""

    event = Signal(object)  # PlayEvent

    def __init__(self, client: PlayClient, game_id: str, parent=None) -> None:
        super().__init__(parent)
        self.client = client
        self.game_id = game_id

    def run(self) -> None:  # pragma: no cover - thread body
        self.client.start(self.game_id, self.event.emit)


class MainWindow(QMainWindow):
    status_message = Signal(str, bool)  # text, is_error

    def __init__(self, settings: Settings) -> None:
        super().__init__()
        self.settings = settings
        self.client = ControlClient(settings.control_url)
        self._play_worker: _PlayWorker | None = None
        self._play_client: PlayClient | None = None
        self._toast_timer = QTimer(self)
        self._toast_timer.setSingleShot(True)
        self._toast_timer.timeout.connect(self._hide_toast)

        self.setWindowTitle(APP_TITLE)
        self.resize(1180, 760)
        self.setMinimumSize(960, 620)

        root = QWidget()
        self.setCentralWidget(root)
        outer = QHBoxLayout(root)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(0)

        # ---- Sidebar -----------------------------------------------------
        sidebar = QWidget()
        sidebar.setFixedWidth(220)
        sidebar_lay = QVBoxLayout(sidebar)
        sidebar_lay.setContentsMargins(theme.SPACE["l"], theme.SPACE["xxl"],
                                       theme.SPACE["l"], theme.SPACE["l"])
        sidebar_lay.setSpacing(theme.SPACE["s"])

        brand = QLabel("PERSONAL GAME")
        brand.setStyleSheet(
            f"color: {theme.ACCENT.name()}; font-weight: 700; letter-spacing: 2px;"
            " font-size: 13px; background: transparent;")
        brand_sub = QLabel("cloud gaming")
        brand_sub.setStyleSheet(
            f"color: {theme.TEXT_FAINT.name()}; font-size: 11px; background: transparent;")
        sidebar_lay.addWidget(brand)
        sidebar_lay.addWidget(brand_sub)
        sidebar_lay.addSpacing(theme.SPACE["xl"])

        self.nav_group = QButtonGroup(self)
        self.nav_group.setExclusive(True)
        self.nav_buttons: dict[str, QPushButton] = {}
        for key, label in (("library", "Library"), ("add", "Add Game"),
                           ("history", "History"), ("settings", "Settings"),
                           ("diagnostics", "Diagnostics")):
            btn = QPushButton(label)
            btn.setProperty("role", "nav")
            btn.setCheckable(True)
            btn.setCursor(Qt.PointingHandCursor)
            btn.clicked.connect(lambda _=False, k=key: self.navigate(k))
            self.nav_group.addButton(btn)
            self.nav_buttons[key] = btn
            sidebar_lay.addWidget(btn)
        sidebar_lay.addStretch(1)

        self.version_label = QLabel("v0.1 · UI")
        self.version_label.setStyleSheet(
            f"color: {theme.TEXT_FAINT.name()}; font-size: 11px; background: transparent;")
        sidebar_lay.addWidget(self.version_label)
        sidebar.setStyleSheet(f"background: {theme.SURFACE.name()};")
        outer.addWidget(sidebar)

        # ---- Main column ---------------------------------------------------
        main_col = QVBoxLayout()
        main_col.setContentsMargins(theme.SPACE["xxl"], theme.SPACE["l"], theme.SPACE["xxl"], theme.SPACE["l"])
        main_col.setSpacing(theme.SPACE["m"])
        outer.addLayout(main_col, 1)

        topbar = QHBoxLayout()
        topbar.setSpacing(theme.SPACE["m"])
        self.page_title = QLabel("Library")
        self.page_title.setProperty("role", "pageTitle")
        topbar.addWidget(self.page_title)
        topbar.addStretch(1)
        self.status_pill = StatusPill()
        topbar.addWidget(self.status_pill)
        main_col.addLayout(topbar)

        self.stack = QStackedWidget()
        main_col.addWidget(self.stack, 1)

        self.toast = Toast(self)
        self.toast.hide()

        # ---- Pages ---------------------------------------------------------
        self.library_page = LibraryPage(self.client, self.settings, self)
        self.add_page = AddGameWizard(self.client, self.settings, self)
        self.history_page = HistoryPage(self.client, self.settings, self)
        self.settings_page = SettingsPage(self.client, self.settings, self)
        self.diagnostics_page = DiagnosticsPage(self.client, self.settings, self)
        for page in (self.library_page, self.add_page, self.history_page,
                     self.settings_page, self.diagnostics_page):
            self.stack.addWidget(page)

        self.library_page.play_requested.connect(self.start_play)
        self.library_page.connection_changed.connect(self.status_pill.set_state)
        self.library_page.empty.action_clicked.connect(lambda: self.navigate("add"))
        self.add_page.game_added.connect(self._on_game_added)
        self.settings_page.settings_changed.connect(self._apply_settings)

        self.nav_buttons["library"].setChecked(True)
        self._apply_settings(initial=True)
        self.navigate("library")

    # ---- Navigation ------------------------------------------------------

    def navigate(self, key: str) -> None:
        titles = {"library": "Library", "add": "Add Game",
                  "history": "History", "settings": "Settings",
                  "diagnostics": "Diagnostics"}
        self.page_title.setText(titles.get(key, key.title()))
        pages = {"library": self.library_page, "add": self.add_page,
                 "history": self.history_page, "settings": self.settings_page,
                 "diagnostics": self.diagnostics_page}
        page = pages[key]
        if not self.nav_buttons[key].isChecked():
            self.nav_buttons[key].setChecked(True)
        self.stack.setCurrentWidget(page)
        page.refresh()

    def _on_game_added(self, spec: AddGameSpec, persisted: bool) -> None:
        self.status_message.emit(f"Added {spec.name}.", False)
        if not persisted:
            self.status_message.emit(
                "Added, but the control plane could not persist the manifest — "
                "it will disappear on restart.", True)
        self.navigate("library")

    def _apply_settings(self, initial: bool = False, **_kwargs) -> None:
        self.client = ControlClient(self.settings.control_url, api_token=self.settings.api_token)
        for page in (self.library_page, self.history_page, self.settings_page,
                     self.diagnostics_page):
            page.client = self.client
        self.add_page.client = self.client

    # ---- Play flow ---------------------------------------------------------

    def start_play(self, game_id: str) -> None:
        if self._play_worker is not None:
            QMessageBox.information(self, APP_TITLE,
                                    "A play session is already starting or running.")
            return
        self.library_page.begin_play(game_id)
        self.navigate("library")
        client = PlayClient(
            self.settings.control_url, self.settings.user_id,
            self.settings.moonlight_path, self.settings.play_command,
            self.settings.api_token)
        self._play_client = client
        self._play_worker = _PlayWorker(client, game_id, self)
        self._play_worker.event.connect(self._on_play_event)
        self.status_message.emit("Creating session…", False)
        self._play_worker.start()

    # Real observed backend states -> friendly journey language. Never fake
    # percentages; unknown lines pass through as-is.
    STAGE_LABELS = {
        "Creating session...": "Checking connection…",
        "session_created": "Finding available gaming node…",
        "Node assigned, preparing...": "Preparing game…",
        "Checking game cache, restoring save, starting backend...": "Restoring save · starting streaming backend…",
        "Ready.": "Starting game streaming…",
        "Ready — launching Moonlight.": "Connecting…",
        "Streaming.": "Connected — have fun!",
        "Finishing up...": "Saving game…",
    }

    def _on_play_event(self, ev: PlayEvent) -> None:
        mapping = {
            "progress": (self.STAGE_LABELS.get(ev.message, ev.message), False),
            "session_created": ("Finding available gaming node…", False),
            "node_assigned": ("Preparing game…", False),
            "preparing": ("Restoring save · starting streaming backend…", False),
            "ready": ("Connecting… Moonlight is opening", False),
            "moonlight_missing": (ev.message, True),
            "error": (ev.message, True),
            "exited": ("Session finished. Progress saved.", False),
        }
        text, is_err = mapping.get(ev.kind, (ev.message, False))
        self.library_page.play_progress(ev.kind, text, is_err)
        if ev.kind in ("error", "exited"):
            self.status_message.emit(text, is_err)

    # ---- Toast -----------------------------------------------------------

    def show_toast(self, text: str, error: bool = False) -> None:
        self.toast.show_message(text, error)
        self._toast_timer.start(6000)

    def _hide_toast(self) -> None:
        self.toast.hide()

    def resizeEvent(self, event) -> None:  # noqa: N802 - Qt naming
        super().resizeEvent(event)
        if self.toast.isVisible():
            self.toast.move(
                (self.width() - self.toast.width()) // 2,
                self.height() - self.toast.height() - theme.SPACE["xl"])

    def closeEvent(self, event) -> None:  # noqa: N802 - Qt naming
        if self._play_client is not None:
            self._play_client.abort()
        if self._play_worker is not None and self._play_worker.isRunning():
            self._play_worker.wait(5000)
        super().closeEvent(event)
