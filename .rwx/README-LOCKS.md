# RWX Directory Guide

## WARNING: RWX reads ALL .yml files in this directory

RWX automatically picks up every `.yml` file in `.rwx/` as a workflow
definition. **Do NOT store backup, draft, or old workflow files here** —
they will trigger duplicate runs on every push.

If you need to keep old versions for reference, move them outside `.rwx/`
(e.g. `docs/rwx-archive/`) or use git history instead.

## Cache Control Lock Files

These lock files control when cached dependencies are rebuilt:

- **cache-config.lock** - Go + golangci-lint + coop/bd versions (touch to rebuild)
- **system-deps.lock** - System packages (touch weekly)
- **e2e-system-deps.lock** - E2E system deps: aws-cli, helm, kubectl (touch to rebuild)
- **helm-version.lock** - Helm CLI installation (touch to update version)
- **agent-syspackages.lock** - Agent image: system packages (git, gcc, cmake, ffmpeg, etc.)
- **agent-node.lock** - Agent image: Node.js + Claude Code
- **agent-playwright.lock** - Agent image: Playwright + Chromium
- **agent-go.lock** - Agent image: Go toolchain + golangci-lint
- **agent-rust.lock** - Agent image: Rust toolchain + quench
- **agent-clis.lock** - Agent image: CLI tools (kubectl, gh, terraform, etc.)

To force a cache rebuild, simply touch the corresponding lock file:
```bash
touch .rwx/system-deps.lock
git add .rwx/system-deps.lock
git commit -m "chore: rebuild system dependency cache"
```

## Version Sync Requirements

Some versions appear in multiple pipeline files and must be updated together:

### Go version
Update `controller/go.mod` — CI workflows read the Go version from `go-version` in the workflow.

Files to update: `controller/go.mod`, CI workflow `go-version`, touch `cache-config.lock`

### golangci-lint version
Built from source in `.rwx/ci.yml` (`v1.64.8`).

Files to update: `.rwx/ci.yml`, touch `cache-config.lock`

### Helm version
Installed via `get-helm-3` script (latest Helm 3).

Files to update: touch `helm-version.lock`

### Coop / Beads binary versions
Downloaded in docker workflow for agent image builds. Versions tracked in `cache-config.lock`.

Files to update: `cache-config.lock` (coop-version-latest, beads-version-latest)
