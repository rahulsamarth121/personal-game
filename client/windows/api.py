"""Control-plane API client.

Thin synchronous wrapper over the real personal-game control API
(``internal/control/api/api.go``). The UI is a client of the existing Go
backend — no second API, no business logic here beyond parsing.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any

DEFAULT_CONTROL_URL = "http://127.0.0.1:8080"
DEFAULT_USER_ID = "player1"
REQUEST_TIMEOUT = 30.0


class ControlPlaneError(Exception):
    """A control-plane request failed in a user-readable way."""

    def __init__(self, message: str, status: int | None = None) -> None:
        super().__init__(message)
        self.status = status


@dataclass
class Game:
    """One entry of GET /v1/games (a GameManifest)."""

    game_id: str
    name: str
    version: str
    package_type: str
    executable: str = ""
    os_name: str = ""

    @classmethod
    def from_json(cls, data: dict[str, Any]) -> "Game":
        acq = data.get("acquisition") or {}
        runtime = data.get("runtime") or {}
        launch = data.get("launch") or {}
        return cls(
            game_id=str(data.get("game_id") or ""),
            name=str(data.get("name") or data.get("game_id") or "Untitled"),
            version=str(data.get("version") or ""),
            package_type=str(acq.get("package_type") or acq.get("provider") or ""),
            executable=str(launch.get("executable") or ""),
            os_name=str(runtime.get("os") or ""),
        )


@dataclass
class Session:
    """One session record (GET /v1/sessions/{id})."""

    session_id: str
    game_id: str
    node_id: str
    state: str
    end_reason: str = ""

    @classmethod
    def from_json(cls, data: dict[str, Any]) -> "Session":
        return cls(
            session_id=str(data.get("session_id") or ""),
            game_id=str(data.get("game_id") or ""),
            node_id=str(data.get("node_id") or ""),
            state=str(data.get("state") or ""),
            end_reason=str(data.get("end_reason") or ""),
        )


@dataclass
class PlaytimeEntry:
    """One row of GET /v1/stats/playtime (server-observed numbers only)."""

    game_id: str
    total_active_seconds: int
    sessions: int
    last_played: str = ""

    @classmethod
    def from_json(cls, data: dict[str, Any]) -> "PlaytimeEntry":
        def as_int(value: Any) -> int:
            try:
                return int(value)
            except (TypeError, ValueError):
                return 0

        return cls(
            game_id=str(data.get("game_id") or ""),
            total_active_seconds=as_int(data.get("total_active_seconds")),
            sessions=as_int(data.get("sessions")),
            last_played=str(data.get("last_played") or ""),
        )


class ControlClient:
    """HTTP client for the control plane. Raises ControlPlaneError with
    human-readable messages; callers never see raw exceptions."""

    def __init__(self, base_url: str = DEFAULT_CONTROL_URL, timeout: float = REQUEST_TIMEOUT,
                 api_token: str = "") -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.api_token = api_token

    # -- internals ---------------------------------------------------------

    def _request(self, method: str, path: str, body: dict[str, Any] | None = None) -> Any:
        url = f"{self.base_url}{path}"
        data = None
        headers = {"Accept": "application/json"}
        if self.api_token:
            headers["Authorization"] = f"Bearer {self.api_token}"
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as exc:
            detail = ""
            try:
                payload = json.loads(exc.read().decode("utf-8", "replace"))
                detail = str(payload.get("error") or "")
            except Exception:  # noqa: BLE001 - error body is best-effort
                detail = ""
            if detail:
                raise ControlPlaneError(f"Control plane returned {exc.code}: {detail}", exc.code) from exc
            raise ControlPlaneError(f"Control plane returned HTTP {exc.code}", exc.code) from exc
        except urllib.error.URLError as exc:
            raise ControlPlaneError(
                f"Cannot reach the control plane at {self.base_url} ({exc.reason}). "
                "Is it running?"
            ) from exc
        except TimeoutError as exc:
            raise ControlPlaneError(
                f"Timed out talking to the control plane at {self.base_url}."
            ) from exc
        if not raw:
            return None
        try:
            return json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError as exc:
            raise ControlPlaneError("Control plane sent a malformed JSON response.") from exc

    # -- API surface -------------------------------------------------------

    def health(self) -> bool:
        """True when GET /healthz answers ok."""
        try:
            out = self._request("GET", "/healthz")
        except ControlPlaneError:
            return False
        return isinstance(out, dict) and out.get("status") == "ok"

    def games(self) -> list[Game]:
        out = self._request("GET", "/v1/games") or {}
        return [Game.from_json(g) for g in (out.get("games") or [])]

    def add_game(self, manifest: dict[str, Any]) -> dict[str, Any]:
        """POST /v1/games. Returns the API response (manifest + persisted flag)."""
        return self._request("POST", "/v1/games", manifest)

    def create_session(self, user_id: str, game_id: str) -> Session:
        out = self._request("POST", "/v1/sessions", {"user_id": user_id, "game_id": game_id})
        return Session.from_json(out or {})

    def get_session(self, session_id: str) -> Session:
        out = self._request("GET", f"/v1/sessions/{session_id}")
        return Session.from_json(out or {})

    def nodes_snapshot(self) -> list[dict[str, Any]]:
        """Best-effort node capability read for the Diagnostics panel.

        The control plane does not currently expose a public nodes list;
        this returns [] when the route is absent so diagnostics degrade
        honestly instead of erroring.
        """
        try:
            out = self._request("GET", "/v1/nodes") or {}
        except ControlPlaneError:
            return []
        return list(out.get("nodes") or [])

    def playtime(self, user_id: str) -> list[PlaytimeEntry]:
        out = self._request("GET", f"/v1/stats/playtime?user_id={urllib.parse.quote(user_id)}") or {}
        return [PlaytimeEntry.from_json(p) for p in (out.get("playtime") or [])]
