# Gasboat

K8s agent controller and Slack bridge for beads automation — extracted from Gastown.

## Architecture

- **controller/** — Go module. K8s agent controller that translates bead lifecycle events into pod operations.
- **controller/internal/bridge/** — Slack notification bridge (decisions watcher, mail watcher, Slack interactions). Zero K8s dependencies.
- **controller/cmd/controller/** — Controller entry point (single binary).
- **controller/cmd/slack-bridge/** — Standalone Slack bridge binary.
- **helm/gasboat/** — Helm chart for all components (controller, coopmux, slack-bridge, PostgreSQL, NATS).
- **images/** — Dockerfiles for agent pods and slack-bridge.

## Directory Structure

```
gasboat/
├── controller/              # Go module — agent controller + slack bridge
│   ├── cmd/controller/      # Controller entry point
│   ├── cmd/slack-bridge/    # Standalone Slack bridge binary
│   └── internal/
│       ├── beadsapi/        # HTTP client to beads daemon
│       ├── bridge/          # Slack notifications (decisions, mail, interactions)
│       ├── config/          # Env var parsing
│       ├── podmanager/      # Pod spec construction & CRUD
│       ├── reconciler/      # Periodic desired-vs-actual sync
│       ├── statusreporter/  # Pod phase → bead state updates
│       └── subscriber/      # SSE/NATS event listener
├── helm/gasboat/            # Helm chart (controller, coopmux, slack-bridge, postgres, nats)
├── images/
│   ├── agent/               # Agent pod image + entrypoint
│   └── slack-bridge/        # Slack bridge Dockerfile
├── Makefile                 # Top-level build
└── quench.toml              # Quality checks
```

## Build

```sh
cd controller && go build ./cmd/controller/    # controller binary
cd controller && go build ./cmd/slack-bridge/   # slack bridge binary
make test                                       # run all tests
quench check                                    # quality checks
```

## Key patterns

- **beadsapi client** (`internal/beadsapi/`) — HTTP/JSON client to beads daemon. Used by both controller and bridge.
- **podmanager** (`internal/podmanager/`) — Pod spec construction and CRUD against K8s API.
- **reconciler** (`internal/reconciler/`) — Periodic desired-vs-actual sync loop.
- **subscriber** (`internal/subscriber/`) — SSE/NATS event listener for bead lifecycle events.
- **bridge** (`internal/bridge/`) — Standalone notification subsystem: NATS subscriptions for decisions/mail beads, Slack HTTP interactions.

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `BEADS_HTTP_ADDR` | `http://localhost:8080` | Beads daemon HTTP address |
| `NATS_URL` | `nats://localhost:4222` | NATS server URL |
| `SLACK_BOAT_TOKEN` | *(optional)* | Slack bot OAuth token |
| `SLACK_CHANNEL` | *(optional)* | Slack channel for notifications |

## Before Picking Up Any Task (Agents)

When multiple agents are running, always follow this sequence to avoid duplicating work:

1. `gb news` — Check what teammates are actively working on
2. `kd claim <id>` — Claim BEFORE starting (atomically sets `in_progress` + assignee)

`gb ready` returns beads across all projects. Only work on tasks in your assigned project unless explicitly instructed otherwise.

## Release (Golden Release Path)

One canonical path. Template: `kd template apply kd-GwMFKXnPvR --var version=YYYY.DDD.N`

```bash
make release                          # bump Chart.yaml, commit, tag
git push origin main <TAG>            # triggers CI (build + push images + push chart)
gh release create <TAG> --generate-notes  # publish release notes
# RWX E2E auto-dispatches on tag; failures become bug beads
# fics-helm-chart auto-updates subchart; GitLab CI deploys
```

**Manual fallback** (if CI doesn't push yet):
```bash
make push-all                         # build + push all 6 images
helm package helm/gasboat/ --version <TAG> --app-version <TAG>
helm push gasboat-<TAG>.tgz oci://ghcr.io/groblegark/charts
```

**Deployment repo**: `~/book/fics-helm-chart/charts/gasboat/` — wrapper chart that depends on upstream OCI chart.

## Agent Image

The agent image is the container that runs Claude Code agents in K8s pods. It has two build systems that **must be kept in sync**.

### Two Build Systems

| System | File | When | How |
|---|---|---|---|
| **Dockerfile** | `images/agent/Dockerfile` | `docker build` / local dev | Multi-stage Docker build |
| **RWX CI** | `.rwx/docker.yml` | Push to main / tag | `crane append` with parallel cached layers |

**Both produce `ghcr.io/groblegark/gasboat/agent:<tag>`**. Production uses RWX CI. The Dockerfile is the reference spec; RWX CI mirrors it with a layer-based approach for speed.

### RWX CI Architecture

RWX splits the agent image into 6 parallel install tasks, each cached independently:

| Task | What it installs | Cache key |
|---|---|---|
| `agent-install-syspackages` | apt packages (git, curl, python3, gcc, ffmpeg, tmux, cmake...) | `.rwx/agent-versions.lock` |
| `agent-install-node` | Node.js, Claude Code, Playwright, @playwright/mcp, Chromium | `.rwx/agent-versions.lock` |
| `agent-install-go` | Go, gopls, golangci-lint, Task CLI | `.rwx/agent-versions.lock` |
| `agent-install-rust` | Rust, rust-analyzer, quench | `.rwx/agent-versions.lock` |
| `agent-install-clis` | kubectl, gh, glab, docker, aws, helm, terraform, terragrunt, uv, bun, rtk, whisper-cli, etc. | `.rwx/agent-versions.lock` |
| `push-agent` (assembly) | Merges all layers + gb, coop, kd binaries → `crane append` | never cached |

Each task runs in its own container on `ubuntu:24.04`. They **cannot share installed packages** — if task A installs cmake, task B cannot use it. Install dependencies locally within each task.

### How to Add a Tool

1. **Add to `images/agent/Dockerfile`** in the appropriate stage (`base` for essentials, `agent` for dev tools)
2. **Add to the matching RWX install task in `.rwx/docker.yml`**:
   - apt packages → `agent-install-syspackages` (also add to dpkg copy loop and ldd binary resolution)
   - npm packages → `agent-install-node`
   - Go tools → `agent-install-go`
   - Rust tools → `agent-install-rust`
   - Binary downloads → `agent-install-clis`
3. **If the tool needs build deps** (e.g. cmake for whisper-cli), install them within the same RWX task — they won't be available from other tasks
4. **Pin versions** in `.rwx/agent-versions.lock` if applicable (changing this file busts all install task caches)
5. **Verify both build paths**:
   - Local: `make image-agent` (docker build)
   - CI: `rwx run --file .rwx/docker.yml` (or push to main)

### How to Update a Tool Version

1. Update the version in `images/agent/Dockerfile` (look for `ARG` lines)
2. Update the matching version in `.rwx/docker.yml` (look in the relevant install task)
3. If the version is in `.rwx/agent-versions.lock`, update it there (this busts all install task caches)
4. Commit, tag, push — RWX CI will rebuild

### How to Verify the Agent Image

After a build, check that tools are present:

```bash
# Export and inspect (without pulling the full image)
crane export ghcr.io/groblegark/gasboat/agent:<tag> - | tar -tf - | grep <binary-name>

# Or run a container
docker run --rm ghcr.io/groblegark/gasboat/agent:<tag> which <tool-name>
```

Key tools to verify: `claude`, `coop`, `kd`, `gb`, `playwright`, `npx`, `ffmpeg`, `tmux`, `whisper-cli`, `go`, `rustc`, `gh`, `glab`, `helm`, `terraform`, `kubectl`

### Common Pitfalls

- **Dockerfile and RWX CI out of sync**: Adding a tool to one but not the other. Always update both.
- **Cross-task dependencies in RWX**: Each `agent-install-*` task runs in a fresh container. If task B needs cmake, install cmake in task B — don't rely on it being in `agent-install-syspackages`.
- **dpkg copy loop**: When adding apt packages to `agent-install-syspackages`, also add the package name to the `for pkg in ...` dpkg loop AND add the binary to the `for bin in ...` ldd loop. Missing these causes shared library errors at runtime.
- **Go template `default` with empty strings**: Helm's `default` function does NOT treat empty string as falsy. Use `{{ if .Values.x }}{{ .Values.x }}{{ else }}{{ .Chart.AppVersion }}{{ end }}` instead of `{{ .Values.x | default .Chart.AppVersion }}`.

### External Dependencies (coopmux, kbeads, beads3d)

These are images from other repos, re-tagged with gasboat's calver during RWX CI:

| Image | Source | RWX task | Helm default tag |
|---|---|---|---|
| `ghcr.io/groblegark/coop:coopmux` | groblegark/coop | `retag-coopmux` | `Chart.AppVersion` |
| `ghcr.io/groblegark/kbeads:latest` | groblegark/kbeads | `retag-kbeads` | `Chart.AppVersion` |
| `ghcr.io/groblegark/beads3d:latest` | groblegark/beads3d | `retag-beads3d` | `Chart.AppVersion` |

The re-tag tasks copy the rolling tag (e.g. `:coopmux`, `:latest`) to the calver tag (e.g. `:2026.62.6`) so all images in the deployment share the same version.

## Commits

Use short, imperative subject lines. Scope in parentheses: `fix(bridge): handle nil bead metadata`.

## Landing the Plane

When finishing work on this codebase:

1. **Build** — `make build` and `make build-bridge` must succeed.
2. **Run tests** — `make test` must pass.
3. **Run quench** — `quench check` must pass.
4. **Helm lint** — `helm lint helm/gasboat/` must pass.
5. **Follow existing patterns** — bridge code lives in `internal/bridge/`, K8s logic in `internal/podmanager/` and `internal/reconciler/`.
