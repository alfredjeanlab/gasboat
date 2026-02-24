# gasboat E2E Tests

Integration tests for the gasboat/kbeads stack.

## Prerequisites

- `kubectl` context pointing at `america-e2e-eks`
- `kd` binary from `~/kbeads` (built with gate system: `bd-pe028`)
- `jq` and `python3` installed
- `gasboat-e2e` namespace deployed (see `fics-helm-chart/charts/gasboat/values/gasboat-e2e.yaml`)

## Gate System Tests (`test-gate-system.sh`)

Tests `kd bus emit --hook=Stop` gate enforcement from the `bd-pe028` epic.

**Requires** a kd server built from `~/kbeads` at commit `8c92e4e` or later (gate system).
The `gasboat-e2e` namespace must be running this version.

### Quick run (port-forward auto-setup):

```bash
KD_BIN=/tmp/kd-gate \
  ./tests/e2e/scripts/test-gate-system.sh
```

### With explicit daemon URL:

```bash
KD_BIN=/tmp/kd-gate \
BEADS_HTTP_URL=http://localhost:19090 \
  ./tests/e2e/scripts/test-gate-system.sh
```

### Scenarios covered:

1. **Decision gate blocks Stop** — no decision offered → exit 2
2. **Decision created, not responded** → Stop still blocks → exit 2
3. **Decision closed (responded)** → gate satisfied → Stop allowed → exit 0
4. **No agent identity** → fails open → exit 0
5. **Dirty git tree** → commit-push soft warning → exit 0 with `<system-reminder>`
6. **Gate status transitions** — pending → satisfied → pending via `kd gate status/mark/clear`

### Claudeless scenarios (`claudeless/`)

TOML scenarios for claudeless-based full session lifecycle tests.
These simulate a complete Claude Code session and require claudeless in PATH.

```bash
# Run a claudeless scenario
claudeless run tests/e2e/claudeless/gate-decision-flow.toml \
  --settings .claude/settings.json
```

Claudeless is installed in the `ghcr.io/groblegark/gasboat/agent:nightly-omnibus` image.

## Deploying gasboat-e2e namespace

```bash
cd ~/book/fics-helm-chart/charts/gasboat
helm upgrade --install gasboat-e2e ./ -n gasboat-e2e --create-namespace \
  --values values/gasboat.yaml \
  --values values/gasboat-e2e.yaml
```

Port-forward for local testing:
```bash
kubectl -n gasboat-e2e port-forward svc/gasboat-e2e-beads 19090:8080
# Then: BEADS_HTTP_URL=http://localhost:19090 kd list
```
