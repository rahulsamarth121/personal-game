# Storage

Three classes, never mixed:

1. PERSISTENT — saves, generations, pointers, history, playtime,
   metadata, settings. PostgreSQL owns ordering/pointers
   (`save_pointers`, `save_snapshots`, `sessions`, ...); R2 holds
   immutable blobs at `saves/{user}/{game}/{generation}/blob`.
2. WARM GAME CACHE — installed/prebuilt games under `/games`
   (only evictable tier; LRU, active-session protected). Rebuildable
   from authorized sources when lost; persistent block volume preferred.
3. EPHEMERAL — `cache/downloads`, `cache/extract`, ISOs, installers,
   save staging, temp. Deleted stage-gated after the next stage verifies.

Pre-acquisition check is package-type aware (installer budgets
ISO/installer overhead; prebuilt/direct do not); evict LRU
non-protected installs or abort cleanly with a required/available/
recoverable/missing message. Temp deleted only after READY.
Saves never evicted. See migrations `0001_init.sql` and `storage.md`
save-generation rules: control-issued, monotonic, fence-checked.
