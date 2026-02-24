# Gasboat

K8s agent controller for running agents using beads + coop + coopmux.

Gasboat is a reactive bridge between a beads daemon and Kubernetes. 
It watches NATS JetStream for agent bead lifecycle events (spawn, done, kill, stuck),
creates/deletes K8s pods in response, and reports pod status back to the daemon via gRPC.

## How It Works

Gasboat has no CLI.
The beads daemon is the control plane — gasboat just executes its decisions as pod operations.

Agents are expressed as **data pushed to beads** at startup (`bridge/init.go`)
and **shell hooks** baked into the agent container image:

- Agent taxonomy → `type:agent` bead with role/state/pod-metadata fields
- Decisions → `type:decision` bead + NATS watcher + Slack notifications
- Mail → `type:mail` bead + NATS watcher that nudges agents
- Advice → `type:advice` bead with hook fields, targeted via labels
- Projects → `type:project` bead with git/image/storage fields
- Agent priming → `context:captain`, `context:mate`, and `context:deckhand` configs rendered by `bd context <role>`

Agent pod hooks (`images/agent/hooks/`) handle the last mile inside each agent container:

- `prime.sh` — SessionStart: renders role dashboard + shows agent's assignment
- `check-mail.sh` — SessionStart/UserPromptSubmit: queries unread mail, queues as system-reminders
- `drain-queue.sh` — PostToolUse: drains inject queue into Claude's context
- `stop-decision.sh` — Stop gate: blocks stop unless agent has an open decision bead

## Structure

```
gasboat/
├── controller/              # Go module — K8s agent controller
│   ├── cmd/controller/      # Entry point
│   └── internal/
│       ├── client/        # gRPC client to beads daemon
│       ├── bridge/          # Bead type/view/context registration, decision + mail watchers
│       ├── config/          # Env var parsing
│       ├── podmanager/      # Pod spec construction & CRUD
│       ├── reconciler/      # Periodic desired-vs-actual sync
│       ├── statusreporter/  # Pod phase → bead state updates
│       └── subscriber/      # NATS JetStream event listener
│
├── images/agent/            # Agent container image
│   ├── Dockerfile           # Multi-stage (slim + omnibus targets)
│   ├── entrypoint.sh        # Agent pod startup
│   └── hooks/               # Claude Code hooks (prime, check-mail, drain-queue, stop-decision)
│
├── helm/gasboat/            # Helm chart
│   └── templates/
│       ├── controller/      # Controller deployment + RBAC
│       ├── beads/           # Beads daemon deployment + service
│       ├── nats/            # JetStream StatefulSet
│       ├── postgres/        # PostgreSQL StatefulSet
│       ├── coopmux/         # Coopmux deployment + service
│       └── ...              # ExternalSecrets, git credentials
│
├── images/
│   ├── agent/               # Agent pod image + entrypoint
│   └── slack-bridge/        # Slack bridge Dockerfile
│
├── Makefile                  # Top-level build
└── README.md
```

## Prerequisites

Gasboat deploys its own beads daemon, NATS, and PostgreSQL via the Helm chart. For managed PostgreSQL, disable the in-chart instance and set `externalDatabase` values (see `deploy/values-do.yaml` for an example).

## Quick Start

```bash
# Build controller binary
make build

# Run tests
make test

# Build Docker images
make image        # controller
make image-agent  # agent container

# Render helm templates (dry-run)
make helm-template

# Package helm chart
make helm-package
```

## Deployment

```bash
helm install gasboat helm/gasboat/ \
  --namespace agents \
  --values deploy/values-do.yaml
```
