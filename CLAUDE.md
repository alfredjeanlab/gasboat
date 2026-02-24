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

## Commits

Use short, imperative subject lines. Scope in parentheses: `fix(bridge): handle nil bead metadata`.

## Landing the Plane

When finishing work on this codebase:

1. **Build** — `make build` and `make build-bridge` must succeed.
2. **Run tests** — `make test` must pass.
3. **Run quench** — `quench check` must pass.
4. **Helm lint** — `helm lint helm/gasboat/` must pass.
5. **Follow existing patterns** — bridge code lives in `internal/bridge/`, K8s logic in `internal/podmanager/` and `internal/reconciler/`.
