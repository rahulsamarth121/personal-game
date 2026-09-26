"""Add Game wizard.

Six steps: Basics -> Package -> Sources -> Details -> Saves -> Review.
The sources field is one URL per line (1..N, no fixed field count). The
produced manifest uses the backend's real v3 package semantics — the wizard
can only express combinations the Go validator accepts, and shows its errors
verbatim at review time and on submit.
"""

from __future__ import annotations

from PySide6.QtCore import Qt, Signal
from PySide6.QtWidgets import (
    QButtonGroup,
    QCheckBox,
    QComboBox,
    QFileDialog,
    QFormLayout,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QMessageBox,
    QPushButton,
    QRadioButton,
    QSpinBox,
    QPlainTextEdit,
    QStackedWidget,
    QVBoxLayout,
    QWidget,
)

from .. import theme
from ..api import ControlClient, ControlPlaneError
from ..models import (
    AddGameSpec,
    ValidationError,
    build_manifest,
    is_probable_archive,
    is_probable_exe,
    normalize_game_id,
    parse_size,
    parse_source_lines,
    validate_spec,
)

STEPS = ["Basics", "Package", "Sources", "Details", "Saves", "Review"]


class _StepPage(QWidget):
    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.lay = QVBoxLayout(self)
        self.lay.setContentsMargins(0, 0, 0, 0)
        self.lay.setSpacing(theme.SPACE["m"])


class AddGameWizard(QWidget):
    game_added = Signal(object, bool)  # (AddGameSpec, persisted)

    def __init__(self, client: ControlClient, settings: Settings, parent=None) -> None:
        super().__init__(parent)
        self.client = client
        self.settings = settings
        self.spec = AddGameSpec()

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.setSpacing(theme.SPACE["m"])

        self.step_label = QLabel("")
        self.step_label.setProperty("role", "pageSubtitle")
        outer.addWidget(self.step_label)

        self.stack = QStackedWidget()
        outer.addWidget(self.stack, 1)
        self._pages: list[_StepPage] = []
        self._build_step1()
        self._build_step2()
        self._build_step3()
        self._build_step4()
        self._build_step5()
        self._build_step6()
        for p in self._pages:
            self.stack.addWidget(p)

        nav = QHBoxLayout()
        self.back_btn = QPushButton("← Back")
        self.back_btn.clicked.connect(self._go_back)
        self.next_btn = QPushButton("Next →")
        self.next_btn.setProperty("role", "accent")
        self.next_btn.clicked.connect(self._go_next)
        self.submit_btn = QPushButton("Add Game")
        self.submit_btn.setProperty("role", "accent")
        self.submit_btn.clicked.connect(self._submit)
        self.submit_btn.hide()
        nav.addWidget(self.back_btn)
        nav.addStretch(1)
        nav.addWidget(self.next_btn)
        nav.addWidget(self.submit_btn)
        outer.addLayout(nav)

        self._errors = QLabel("")
        self._errors.setWordWrap(True)
        self._errors.setStyleSheet(f"color: {theme.DANGER.name()}; background: transparent;")
        self._errors.hide()
        outer.addWidget(self._errors)

        self._step = 0
        self._show_step(0)

    # ------------------------------------------------------------------ step 1
    def _build_step1(self) -> None:
        page = _StepPage()
        form = QFormLayout()
        form.setSpacing(theme.SPACE["m"])
        self.name_edit = QLineEdit()
        self.name_edit.setPlaceholderText("e.g. DOOM II")
        form.addRow("Game name", self.name_edit)
        id_row = QHBoxLayout()
        self.id_edit = QLineEdit()
        self.id_edit.setPlaceholderText("auto-generated from the name")
        id_row.addWidget(self.id_edit, 1)
        form.addRow("Game ID", id_row)
        self.version_edit = QLineEdit("1.0")
        form.addRow("Version", self.version_edit)
        self.name_edit.textEdited.connect(self._autofill_id)
        page.lay.addLayout(form)
        hint = QLabel("The Game ID names the manifest file and the game folder on the "
                      "node; letters, digits, dots, hyphens and underscores only.")
        hint.setProperty("role", "pageSubtitle")
        hint.setWordWrap(True)
        page.lay.addWidget(hint)
        page.lay.addStretch(1)
        self._pages.append(page)

    def _autofill_id(self, text: str) -> None:
        if not self.id_edit.text().strip() or self.id_edit.property("auto") is True:
            self.id_edit.setText(normalize_game_id(text))
            self.id_edit.setProperty("auto", True)

    # ------------------------------------------------------------------ step 2
    def _build_step2(self) -> None:
        page = _StepPage()
        what = QLabel("What are you adding?")
        what.setProperty("role", "cardTitle")
        page.lay.addWidget(what)

        self.kind_group = QButtonGroup(self)
        self.rb_prebuilt = QRadioButton("Prebuilt game — the download is already the game")
        self.rb_installer = QRadioButton("Installer game — run a setup EXE to install it")
        self.kind_group.addButton(self.rb_prebuilt)
        self.kind_group.addButton(self.rb_installer)
        self.kind_group.buttonToggled.connect(self._kind_changed)
        page.lay.addWidget(self.rb_prebuilt)
        page.lay.addWidget(self.rb_installer)

        self.kind_detail = QLabel("")
        self.kind_detail.setProperty("role", "pageSubtitle")
        self.kind_detail.setWordWrap(True)
        page.lay.addWidget(self.kind_detail)

        layout_title = QLabel("How many source links do you have?")
        layout_title.setProperty("role", "cardTitle")
        layout_title.setContentsMargins(0, theme.SPACE["m"], 0, 0)
        page.lay.addWidget(layout_title)

        self.src_group = QButtonGroup(self)
        self.rb_single = QRadioButton("Single source (one URL)")
        self.rb_multi = QRadioButton("Multiple parts (one URL per part)")
        self.src_group.addButton(self.rb_single)
        self.src_group.addButton(self.rb_multi)
        page.lay.addWidget(self.rb_single)
        page.lay.addWidget(self.rb_multi)

        iso_row = QHBoxLayout()
        self.iso_check = QCheckBox("ISO stage (archive reconstruction produces an ISO)")
        self.iso_check.toggled.connect(self._iso_toggled)
        iso_row.addWidget(self.iso_check)
        iso_row.addStretch(1)
        page.lay.addLayout(iso_row)
        page.lay.addStretch(1)
        self._pages.append(page)

    def _kind_changed(self, *_a) -> None:
        self.rb_single.setEnabled(True)
        self.rb_multi.setEnabled(True)
        if self.rb_prebuilt.isChecked():
            self.kind_detail.setText(
                "Prebuilt: extract/copy, validate, play. No installer stage is ever run. "
                "Multiple parts are not supported for prebuilt packages (backend rule).")
            if self.rb_multi.isChecked():
                self.rb_single.setChecked(True)
            self.rb_multi.setEnabled(False)
            self.iso_check.setChecked(False)
            self.iso_check.setEnabled(False)
        else:
            self.kind_detail.setText(
                "Installer: download, extract, (ISO), run the setup EXE, validate. "
                "Multiple archive parts are the classic multipart installer flow.")
            self.iso_check.setEnabled(True)

    def _iso_toggled(self, on: bool) -> None:
        # iso_installer when ISO stage is on and it is an installer game.
        if self.rb_prebuilt.isChecked():
            self.iso_check.setChecked(False)
            return
        self.spec.package_type = self._derive_package_type()

    def _derive_package_type(self) -> str:
        if self.rb_prebuilt.isChecked():
            if is_probable_exe(self._first_url()) and not is_probable_archive(self._first_url()):
                return "direct_prebuilt"
            return "archive_prebuilt"
        if self.iso_check.isChecked():
            return "iso_installer"
        return "archive_installer"

    def _first_url(self) -> str:
        return next((s for s in self._sources_text().splitlines() if s.strip()), "")

    # ------------------------------------------------------------------ step 3
    def _build_step3(self) -> None:
        page = _StepPage()
        title = QLabel("Sources")
        title.setProperty("role", "cardTitle")
        page.lay.addWidget(title)
        hint = QLabel("Paste one URL per line — one source or many parts. Blank lines "
                      "and # comments are ignored. Order matters for multipart archives.")
        hint.setProperty("role", "pageSubtitle")
        hint.setWordWrap(True)
        page.lay.addWidget(hint)

        self.sources_edit = QPlainTextEdit()
        self.sources_edit.setProperty("role", "sources")
        self.sources_edit.setPlaceholderText(
            "https://example.com/game.part01.zip\n"
            "https://example.com/game.part02.zip\n"
            "https://example.com/game.part03.zip")
        self.sources_edit.setMinimumHeight(180)
        page.lay.addWidget(self.sources_edit, 1)

        self.sources_summary = QLabel("")
        self.sources_summary.setProperty("role", "pageSubtitle")
        page.lay.addWidget(self.sources_summary)
        self.sources_edit.textChanged.connect(self._sources_changed)
        self._pages.append(page)

    def _sources_text(self) -> str:
        return self.sources_edit.toPlainText() if hasattr(self, "sources_edit") else ""

    def _sources_changed(self) -> None:
        try:
            sources = parse_source_lines(self._sources_text())
        except ValidationError as exc:
            self.sources_summary.setText(f"⚠ {exc}")
            return
        if not sources:
            self.sources_summary.setText("No sources yet.")
            return
        kind = "part" if len(sources) > 1 else "source"
        self.sources_summary.setText(
            f"{len(sources)} {kind}{'s' if len(sources) > 1 else ''} parsed, in order.")

    # ------------------------------------------------------------------ step 4
    def _build_step4(self) -> None:
        page = _StepPage()
        details_form = QFormLayout()
        details_form.setSpacing(theme.SPACE["m"])

        self.archive_combo = QComboBox()
        self.archive_combo.addItems(["zip", "rar", "7z"])
        row_archive = [QLabel("Archive type"), self.archive_combo]
        details_form.addRow(row_archive[0], row_archive[1])

        self.installer_edit = QLineEdit()
        self.installer_edit.setPlaceholderText("e.g. setup.exe — path inside the archive")
        row_inst = self._add_row(details_form, "Installer executable", self.installer_edit)
        self.installer_args_edit = QLineEdit()
        self.installer_args_edit.setPlaceholderText("e.g. /SILENT (space-separated, optional)")
        row_inst_args = self._add_row(details_form, "Installer arguments", self.installer_args_edit)
        self.installer_timeout = QSpinBox()
        self.installer_timeout.setRange(30, 24 * 3600)
        self.installer_timeout.setValue(3600)
        row_inst_timeout = self._add_row(details_form, "Installer timeout (s)", self.installer_timeout)
        self.installer_dir_edit = QLineEdit()
        self.installer_dir_edit.setPlaceholderText("optional: expected install folder name")
        row_inst_dir = self._add_row(details_form, "Expected installed folder", self.installer_dir_edit)

        self.exe_edit = QLineEdit()
        self.exe_edit.setPlaceholderText("e.g. game.exe — path inside the game folder")
        row_exe = self._add_row(details_form, "Game executable", self.exe_edit)
        self.args_edit = QLineEdit()
        self.args_edit.setPlaceholderText("space-separated launch arguments (optional)")
        row_args = self._add_row(details_form, "Launch arguments", self.args_edit)

        self.game_root_edit = QLineEdit()
        self.game_root_edit.setPlaceholderText("optional subfolder, e.g. Bin/Win64")
        row_root = self._add_row(details_form, "Prebuilt game root", self.game_root_edit)
        self.expected_edit = QLineEdit()
        self.expected_edit.setPlaceholderText("optional required files, comma-separated")
        row_expected = self._add_row(details_form, "Expected files (prebuilt)", self.expected_edit)

        self.iso_path_edit = QLineEdit()
        self.iso_path_edit.setPlaceholderText("e.g. game.iso (the ISO produced by the archive)")
        row_iso = self._add_row(details_form, "ISO result path", self.iso_path_edit)

        self.download_size_edit = QLineEdit()
        self.download_size_edit.setPlaceholderText("optional: total download size in bytes")
        row_dl = self._add_row(details_form, "Download size (bytes)", self.download_size_edit)
        self.installed_size_edit = QLineEdit()
        self.installed_size_edit.setPlaceholderText("optional: installed size in bytes")
        row_installed = self._add_row(details_form, "Installed size (bytes)", self.installed_size_edit)

        page.lay.addLayout(details_form)
        page.lay.addStretch(1)
        self._pages.append(page)

        # Field visibility per package mode (only relevant fields are shown).
        self._rows_installer_only = [row_inst, row_inst_args, row_inst_timeout, row_inst_dir]
        self._rows_prebuilt_only = [row_root, row_expected]
        self._rows_iso_only = [row_iso]
        self._always_visible = [row_archive, row_exe, row_args, row_dl, row_installed]

    def _add_row(self, form: QFormLayout, label: str, field: QWidget):
        lbl = QLabel(label)
        form.addRow(lbl, field)
        return [lbl, field]

    def _apply_field_visibility(self) -> None:
        pt = self._derive_package_type()
        installer_mode = pt in ("archive_installer", "iso_installer")
        prebuilt_mode = pt in ("archive_prebuilt", "direct_prebuilt")
        direct = pt == "direct_prebuilt"
        iso_on = installer_mode and self.iso_check.isChecked()
        self._set_row_visible(self._rows_installer_only, installer_mode)
        self._set_row_visible(self._rows_prebuilt_only, prebuilt_mode)
        self._set_row_visible(self._rows_iso_only, iso_on)
        self._set_row_visible(self._always_visible, True)
        # Archive type applies to archive modes only (direct EXE has none).
        self.archive_combo.setEnabled(not direct)

    def _set_row_visible(self, rows, visible: bool) -> None:
        for row in rows:
            for w in row:
                w.setVisible(visible)

    # ------------------------------------------------------------------ step 5
    def _build_step5(self) -> None:
        page = _StepPage()
        title = QLabel("Saves")
        title.setProperty("role", "cardTitle")
        page.lay.addWidget(title)
        form = QFormLayout()
        form.setSpacing(theme.SPACE["m"])
        self.ludusavi_edit = QLineEdit()
        self.ludusavi_edit.setPlaceholderText("optional: title as Ludusavi knows it")
        form.addRow("Ludusavi title", self.ludusavi_edit)
        self.save_paths_edit = QLineEdit()
        self.save_paths_edit.setPlaceholderText(
            "optional overrides, comma-separated, e.g. %APPDATA%/MyGame")
        form.addRow("Save path overrides", self.save_paths_edit)
        page.lay.addLayout(form)
        hint = QLabel("Restore-before-launch and snapshot-after-exit stay in the "
                      "backend; these fields only point it at the right data.")
        hint.setProperty("role", "pageSubtitle")
        hint.setWordWrap(True)
        page.lay.addWidget(hint)
        page.lay.addStretch(1)
        self._pages.append(page)

    # ------------------------------------------------------------------ step 6
    def _build_step6(self) -> None:
        page = _StepPage()
        title = QLabel("Review")
        title.setProperty("role", "cardTitle")
        page.lay.addWidget(title)
        self.review_text = QPlainTextEdit()
        self.review_text.setReadOnly(True)
        self.review_text.setStyleSheet(
            "font-family: Consolas, 'Cascadia Mono', monospace; font-size: 12px;")
        page.lay.addWidget(self.review_text, 1)
        self._pages.append(page)

    # ------------------------------------------------------------------ flow
    def refresh(self) -> None:  # page protocol
        self._show_step(self._step)

    def _show_step(self, idx: int) -> None:
        self._step = idx
        self.stack.setCurrentIndex(idx)
        self.step_label.setText(f"Step {idx + 1} of {len(STEPS)} — {STEPS[idx]}")
        self.back_btn.setVisible(idx > 0)
        last = idx == len(STEPS) - 1
        self.next_btn.setVisible(not last)
        self.submit_btn.setVisible(last)
        if idx == 2:
            self._sources_changed()
        if idx == 3:
            self._apply_field_visibility()
        if idx == 5:
            self._render_review()

    def _go_back(self) -> None:
        self._errors.hide()
        self._show_step(max(0, self._step - 1))

    def _collect(self) -> AddGameSpec:
        spec = AddGameSpec()
        spec.game_id = normalize_game_id(self.id_edit.text())
        spec.name = self.name_edit.text().strip() or spec.game_id
        spec.version = self.version_edit.text().strip() or "1.0"
        spec.package_type = self._derive_package_type()
        spec.archive_type = self.archive_combo.currentText()
        spec.sources = parse_source_lines(self._sources_text())
        spec.exe = self.exe_edit.text().strip()
        spec.launch_args = self.args_edit.text().split()
        if spec.package_type in ("archive_installer", "iso_installer"):
            spec.installer_path = self.installer_edit.text().strip()
            spec.installer_args = self.installer_args_edit.text().split()
            spec.installer_timeout = int(self.installer_timeout.value())
            spec.installer_expected_dir = self.installer_dir_edit.text().strip()
            spec.iso_result_path = self.iso_path_edit.text().strip()
            spec.iso_extract = self.iso_check.isChecked() and bool(spec.iso_result_path)
        if spec.package_type in ("archive_prebuilt", "direct_prebuilt"):
            spec.game_root = self.game_root_edit.text().strip()
            spec.expected_files = [
                f.strip() for f in self.expected_edit.text().split(",") if f.strip()]
        spec.ludusavi_title = self.ludusavi_edit.text().strip()
        spec.save_paths = [p.strip() for p in self.save_paths_edit.text().split(",") if p.strip()]
        spec.download_bytes = parse_size(self.download_size_edit.text())
        spec.installed_bytes = parse_size(self.installed_size_edit.text())
        return spec

    def _go_next(self) -> None:
        try:
            if self._step == 0:
                from ..models import validate_game_id
                validate_game_id(normalize_game_id(self.id_edit.text()))
                if not self.name_edit.text().strip():
                    raise ValidationError("Game name is required.")
        except ValidationError as exc:
            self._show_errors([str(exc)])
            return
        if self._step == 1 and not self.kind_group.checkedButton():
            self._show_errors(["Choose what you are adding."])
            return
        if self._step == 2:
            try:
                sources = parse_source_lines(self._sources_text())
            except ValidationError as exc:
                self._show_errors([str(exc)])
                return
            if not sources:
                self._show_errors(["Add at least one source URL (one per line)."])
                return
        if self._step == 3:
            errors = validate_spec(self._collect())
            if errors:
                self._show_errors(errors)
                return
        self._errors.hide()
        self._show_step(self._step + 1)

    def _show_errors(self, errors: list[str]) -> None:
        self._errors.setText("\n".join(f"• {e}" for e in errors))
        self._errors.show()

    def _render_review(self) -> None:
        spec = self._collect()
        errors = validate_spec(spec)
        lines = [
            f"Game:    {spec.name}",
            f"ID:      {spec.game_id}",
            f"Version: {spec.version}",
            f"Mode:    {spec.mode_label} ({spec.package_type})",
            f"Sources: {len(spec.sources)}",
            f"Archive: {spec.archive_type if spec.archive_type else '—'}",
        ]
        if spec.is_installer_mode:
            lines += [
                f"Installer: {spec.installer_path}",
                f"Installer timeout: {spec.installer_timeout}s",
                f"ISO stage: {'Enabled' if spec.iso_extract else 'Disabled'}",
            ]
            if spec.installer_expected_dir:
                lines.append(f"Expected install dir: {spec.installer_expected_dir}")
        lines += [
            f"Launch:  {spec.exe}",
            f"Expected download size: {spec.download_bytes or 'unknown'}",
            f"Expected installed size: {spec.installed_bytes or 'unknown'}",
            f"Saves: ludusavi{(' (' + spec.ludusavi_title + ')') if spec.ludusavi_title else ''}",
        ]
        if errors:
            lines += ["", "PROBLEMS:", *[f"  • {e}" for e in errors]]
        self.review_text.setPlainText("\n".join(lines))
        self._errors.hide()

    def _submit(self) -> None:
        spec = self._collect()
        errors = validate_spec(spec)
        if errors:
            self._show_errors(errors)
            return
        try:
            manifest = build_manifest(spec)
        except ValidationError as exc:
            self._show_errors([str(exc)])
            return
        self.submit_btn.setEnabled(False)
        try:
            resp = self.client.add_game(manifest)
        except ControlPlaneError as exc:
            self._show_errors([f"The control plane rejected the game: {exc}"])
            self.submit_btn.setEnabled(True)
            return
        persisted = bool(resp.get("persisted", False)) if isinstance(resp, dict) else False
        self.submit_btn.setEnabled(True)
        self.game_added.emit(spec, persisted)
        self._reset()

    def _reset(self) -> None:
        for w in (self.name_edit, self.id_edit, self.sources_edit, self.exe_edit,
                  self.args_edit, self.installer_edit, self.installer_args_edit,
                  self.installer_dir_edit, self.iso_path_edit, self.game_root_edit,
                  self.expected_edit, self.ludusavi_edit, self.save_paths_edit,
                  self.download_size_edit, self.installed_size_edit):
            w.clear()
        self.version_edit.setText("1.0")
        self.iso_check.setChecked(False)
        self.kind_group.setExclusive(False)
        for b in (self.rb_prebuilt, self.rb_installer, self.rb_single, self.rb_multi):
            b.setChecked(False)
        self.kind_group.setExclusive(True)
        self._show_step(0)
