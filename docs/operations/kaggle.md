# Kaggle operations (adapter)

One runner: `kaggle/runner.py` (`all`, or stepwise
`clone -> diagnostics -> setup -> run -> cleanup`).

```bash
python kaggle/runner.py all --repo-url https://github.com/rahulsamarth121/personal-game.git
```

On a fresh Kaggle kernel, bootstrap with notebook cell 1
(`kaggle/notebook/personal_game.ipynb`): it loads Secrets (`GITHUB_TOKEN`
for the private-repo clone — served via a temporary git askpass helper,
never printed), clones safely, then executes this runner. Config via env
(`GITHUB_REPO_URL`, `GITHUB_BRANCH`, `WORK_ROOT`, `CONTROL_PLANE_URL`,
`NODE_ENROLLMENT_TOKEN`, `NODE_NAME`) or CLI flags — never hardcode
secrets into notebook cells. Clone is safe (fast-forward
only, dirty trees left alone unless `--update-repo always`).

Assume no permanent disk, no inbound, no desktop; capability discovery
reports what exists (`unavailable` otherwise, never faked). Outbound
control only. Cleanup removes ephemeral staging, never saves.
Notebook `kaggle/notebook/personal_game.ipynb` is a 6-cell thin wrapper
(markdown intro + bootstrap cell + diagnostics/setup/run/cleanup).

Go is NOT preinstalled on Kaggle images: when `go` is missing from PATH,
`setup`/`diagnostics` bootstrap a pinned, sha256-verified official Go
tarball into `$WORK_ROOT/go` and prepend it to the build PATH (env copy
only — no host modification; `GOROOT_SHA256=never` disables the
bootstrap). Preinstalled Go is preferred when present; reruns reuse the
cached toolchain. See `kaggle/README.md` for the exact behavior.
