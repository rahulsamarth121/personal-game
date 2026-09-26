# Kaggle operations (adapter)

One runner: `kaggle/runner.py` (`all`, or stepwise
`clone -> diagnostics -> setup -> run -> cleanup`).

```bash
python kaggle/runner.py all --repo-url https://github.com/YOU/personal-game.git
```

Config via env (`GITHUB_REPO_URL`, `GITHUB_BRANCH`, `WORK_ROOT`,
`CONTROL_PLANE_URL`, `NODE_ENROLLMENT_TOKEN`, `NODE_NAME`) or CLI flags —
never hardcode secrets into notebook cells. Clone is safe (fast-forward
only, dirty trees left alone unless `--update-repo always`).

Assume no permanent disk, no inbound, no desktop; capability discovery
reports what exists (`unavailable` otherwise, never faked). Outbound
control only. Cleanup removes ephemeral staging, never saves.
Notebook `kaggle/notebook/personal_game.ipynb` is a 5-cell thin wrapper.
