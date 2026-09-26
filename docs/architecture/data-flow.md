# Data flow

## Session lifecycle (with saves)

```
REQUESTED -> NODE_ASSIGNED -> PREPARING -> RESTORE SAVE -> READY
  -> STREAMING <-> DEGRADED (client blip, grace period, game kept)
  -> DRAINING -> FINAL SAVE -> CLOSED | FAILED
  lease loss from NODE_ASSIGNED onward -> FENCED -> CLOSED
```

Game crash: final save attempt -> close with crash reason.
Node fencing: game termination -> save commits rejected.

Playtime derives server-side from state/heartbeat/process observation;
client disconnect pauses active accrual after the grace window.

## Save lifecycle (highest priority)

```
begin (control validates lease, issues gen + presigned PUT)
 -> quiescence wait -> package + sha256 -> upload immutable blob
 -> commit (control verifies fence + ordering + sha)
 -> VALID (post-exit) / CHECKPOINT (mid-session) + advance pointer
 -> fence mismatch => ORPHANED, pointer untouched
```

Restore: latest pointer -> presigned GET -> temp download -> checksum ->
unpack -> stage -> atomic swap (rollback on error) -> launch.
Never write partial data live.

## Acquisition lifecycle

Prebuilt: PLANNED -> SPACE_CHECKED -> DOWNLOADING -> DOWNLOAD_VERIFIED
-> ARCHIVE_READY -> ARCHIVE_EXTRACTED (place + validate) ->
SOURCE_CLEANED (delete source) -> READY.

Installer: ... -> ARCHIVE_EXTRACTED -> SOURCE_CLEANED (delete parts) ->
[ISO stages] -> INSTALLING -> INSTALL_VALIDATED -> INSTALLER_CLEANED
(delete staging) -> READY.

## Node lifecycle

```
BOOT -> PROBING -> REGISTERING -> IDLE -> PREPARING -> BUSY
  -> DRAINING -> TERMINATED   (+ control overlay SUSPECT/DEAD)
```

Re-enrollment with a fresh token fences sessions pinned to the old token.
Nodes dial out; the control plane never assumes inbound reachability.
