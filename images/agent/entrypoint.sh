#!/bin/bash
# Gasboat agent entrypoint: starts a coop session with Claude.
#
# This entrypoint handles all agent roles. The controller sets role-specific
# env vars before the pod starts; this script reads BOAT_ROLE to configure
# the workspace and launch Claude with the correct context.
#
# Required environment variables (set by pod manager):
#   BOAT_ROLE       - agent role
#   BOAT_PROJECT        - project name (empty for town-level roles)
#   BOAT_AGENT      - agent name
#
# Optional:
#   BOAT_COMMAND    - command to run in screen (default: "claude --dangerously-skip-permissions")
#   BEADS_DAEMON_HOST - beads daemon URL
#   BEADS_DAEMON_PORT - beads daemon port
#   BOAT_SESSION_RESUME - set to "1" to auto-resume previous Claude session on restart

set -euo pipefail

ROLE="${BOAT_ROLE:-unknown}"
PROJECT="${BOAT_PROJECT:-}"
MODE="${BOAT_MODE:-crew}"
AGENT="${BOAT_AGENT:-unknown}"
WORKSPACE="/home/agent/workspace"
SESSION_RESUME="${BOAT_SESSION_RESUME:-1}"

# Export platform version for kd version commands
if [ -f /etc/platform-version ]; then
    export BEADS_PLATFORM_VERSION
    BEADS_PLATFORM_VERSION=$(cat /etc/platform-version)
fi

echo "[entrypoint] Starting ${ROLE} agent (mode: ${MODE}): ${AGENT} (project: ${PROJECT:-none})"

# ── Workspace setup ──────────────────────────────────────────────────────

# Set global git config FIRST so safe.directory is set before any repo ops.
# The workspace volume mount is owned by root (EmptyDir/PVC) but we run as
# UID 1000 — git's dubious-ownership check would block all operations without this.
git config --global user.name "${GIT_AUTHOR_NAME:-${ROLE}}"
git config --global user.email "${ROLE}@gasboat.local"
git config --global --add safe.directory '*'

# ── Git credentials ────────────────────────────────────────────────────
# If GIT_USERNAME and GIT_TOKEN are set (from ExternalSecret), configure
# git credential-store so clone/push to github.com works automatically.
CRED_FILE="${HOME}/.git-credentials"
CRED_WRITTEN=0

if [ -n "${GIT_USERNAME:-}" ] && [ -n "${GIT_TOKEN:-}" ]; then
    echo "https://${GIT_USERNAME}:${GIT_TOKEN}@github.com" > "${CRED_FILE}"
    CRED_WRITTEN=1
    echo "[entrypoint] Git credentials configured for ${GIT_USERNAME}@github.com"
fi

# Append GitLab credentials if GITLAB_TOKEN is set.
# Uses GITLAB_USERNAME env var if provided, otherwise defaults to "oauth2" (PAT auth).
if [ -n "${GITLAB_TOKEN:-}" ]; then
    GL_USER="${GITLAB_USERNAME:-oauth2}"
    GL_HOST="${GITLAB_HOST:-gitlab.com}"
    echo "https://${GL_USER}:${GITLAB_TOKEN}@${GL_HOST}" >> "${CRED_FILE}"
    CRED_WRITTEN=1
    echo "[entrypoint] Git credentials configured for ${GL_USER}@${GL_HOST}"
fi

if [ "${CRED_WRITTEN}" = "1" ]; then
    chmod 600 "${CRED_FILE}"
    git config --global credential.helper "store --file=${CRED_FILE}"
fi

# Initialize git repo in workspace if not already present.
# Persistent roles keep state across restarts via PVC.
if [ ! -d "${WORKSPACE}/.git" ]; then
    echo "[entrypoint] Initializing git repo in ${WORKSPACE}"
    cd "${WORKSPACE}"
    git init -q
    git config user.name "${GIT_AUTHOR_NAME:-${ROLE}}"
    git config user.email "${ROLE}@gasboat.local"
else
    echo "[entrypoint] Git repo already exists in ${WORKSPACE}"
    cd "${WORKSPACE}"

    # Auto-fix stale branch in workspace root on restart.
    CURRENT_BRANCH="$(git branch --show-current 2>/dev/null || true)"
    if [ -n "${CURRENT_BRANCH}" ] && [ "${CURRENT_BRANCH}" != "main" ] && [ "${CURRENT_BRANCH}" != "master" ]; then
        echo "[entrypoint] WARNING: Workspace on stale branch '${CURRENT_BRANCH}', resetting to main"
        git checkout -- . 2>/dev/null || true
        git clean -fd 2>/dev/null || true
        if git show-ref --verify --quiet refs/heads/main 2>/dev/null; then
            git checkout main 2>/dev/null || echo "[entrypoint] ERROR: git checkout main failed"
        else
            git checkout -b main 2>/dev/null || echo "[entrypoint] ERROR: git checkout -b main failed"
        fi
        echo "[entrypoint] Workspace now on branch: $(git branch --show-current 2>/dev/null)"
    fi
fi

# ── Daemon connection ────────────────────────────────────────────────────
# Configure .beads/config.yaml so kd CLI can talk to the remote daemon.

if [ -n "${BEADS_DAEMON_HOST:-}" ]; then
    DAEMON_HTTP_PORT="${BEADS_DAEMON_HTTP_PORT:-9080}"
    DAEMON_URL="http://${BEADS_DAEMON_HOST}:${DAEMON_HTTP_PORT}"
    echo "[entrypoint] Configuring daemon connection at ${DAEMON_URL}"
    mkdir -p "${WORKSPACE}/.beads"
    cat > "${WORKSPACE}/.beads/config.yaml" <<BEADSCFG
daemon-host: "${DAEMON_URL}"
daemon-token: "${BEADS_DAEMON_TOKEN:-}"
BEADSCFG
fi

# ── Session persistence ──────────────────────────────────────────────────
#
# Persist Claude state (~/.claude) and coop session artifacts on the
# workspace PVC so they survive pod restarts.  The PVC is already mounted
# at the workspace.  We store session state under .state/ on the PVC
# and symlink the ephemeral home-directory paths into it.
#
#   PVC layout:
#     {workspace}/.state/claude/     →  symlinked from ~/.claude
#     {workspace}/.state/coop/       →  symlinked from $XDG_STATE_HOME/coop

STATE_DIR="${WORKSPACE}/.state"
CLAUDE_STATE="${STATE_DIR}/claude"
COOP_STATE="${STATE_DIR}/coop"

mkdir -p "${CLAUDE_STATE}" "${COOP_STATE}"

# Persist ~/.claude on PVC.
CLAUDE_DIR="${HOME}/.claude"
# If ~/.claude is a mount point (subPath mount from controller),
# it's already PVC-backed — skip the symlink dance.
if mountpoint -q "${CLAUDE_DIR}" 2>/dev/null; then
    echo "[entrypoint] ${CLAUDE_DIR} is a mount point (subPath) — already PVC-backed"
else
    rm -rf "${CLAUDE_DIR}"
    ln -sfn "${CLAUDE_STATE}" "${CLAUDE_DIR}"
    echo "[entrypoint] Linked ${CLAUDE_DIR} → ${CLAUDE_STATE} (PVC-backed)"
fi

# ── Claude credential provisioning ────────────────────────────────────
# Priority: (1) PVC credentials (preserved from refresh), (2) K8s secret mount,
# (3) CLAUDE_CODE_OAUTH_TOKEN env var (coop auto-writes .credentials.json),
# (4) ANTHROPIC_API_KEY env var (API key mode — no credentials file needed),
# (5) coopmux distribute endpoint (fetch from centralized credential manager).
CREDS_STAGING="/tmp/claude-credentials/credentials.json"
CREDS_PVC="${CLAUDE_STATE}/.credentials.json"
if [ -f "${CREDS_PVC}" ]; then
    echo "[entrypoint] Using existing PVC credentials (preserved from refresh)"
elif [ -f "${CREDS_STAGING}" ]; then
    cp "${CREDS_STAGING}" "${CREDS_PVC}"
    echo "[entrypoint] Seeded Claude credentials from K8s secret"
elif [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    echo "[entrypoint] CLAUDE_CODE_OAUTH_TOKEN set — coop will auto-write credentials"
elif [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    echo "[entrypoint] ANTHROPIC_API_KEY set — using API key mode"
elif [ -n "${COOP_MUX_URL:-}" ]; then
    # Attempt to fetch credentials from coopmux's distribute endpoint.
    echo "[entrypoint] No credentials found, requesting from coopmux..."
    mux_auth="${COOP_MUX_AUTH_TOKEN:-${COOP_BROKER_TOKEN:-}}"
    mux_creds=$(curl -sf "${COOP_MUX_URL}/api/v1/credentials/distribute" \
        ${mux_auth:+-H "Authorization: Bearer ${mux_auth}"} \
        -H 'Content-Type: application/json' \
        -d "{\"session_id\":\"${HOSTNAME:-$(hostname)}\"}" 2>/dev/null) || true
    if [ -n "${mux_creds}" ] && echo "${mux_creds}" | jq -e '.claudeAiOauth.accessToken' >/dev/null 2>&1; then
        echo "${mux_creds}" > "${CREDS_PVC}"
        echo "[entrypoint] Seeded credentials from coopmux"
    else
        echo "[entrypoint] WARNING: No Claude credentials available — agent may not authenticate"
    fi
else
    echo "[entrypoint] WARNING: No Claude credentials available — agent may not authenticate"
fi

# Set XDG_STATE_HOME so coop writes session artifacts to the PVC.
export XDG_STATE_HOME="${STATE_DIR}"
echo "[entrypoint] XDG_STATE_HOME=${XDG_STATE_HOME}"

# ── Dev tools PATH ─────────────────────────────────────────────────────

if [ -d "/usr/local/go/bin" ]; then
    export PATH="/usr/local/go/bin:${PATH}"
    echo "[entrypoint] Added /usr/local/go/bin to PATH"
fi
if [ -d "/usr/local/cargo/bin" ]; then
    export PATH="/usr/local/cargo/bin:${PATH}"
    echo "[entrypoint] Added /usr/local/cargo/bin to PATH"
fi

# ── Claude settings ──────────────────────────────────────────────────────
#
# User-level settings (permissions + LSP plugins) written to ~/.claude/settings.json.
# LSP plugins are always enabled — gopls and rust-analyzer are in the omnibus image.

# Start with base settings JSON (permissions + LSP plugins).
SETTINGS_JSON='{"permissions":{"allow":["Bash(*)","Read(*)","Write(*)","Edit(*)","Glob(*)","Grep(*)","WebFetch(*)","WebSearch(*)"],"deny":[]}}'

# Enable LSP plugins (gopls + rust-analyzer are always present in the agent image).
PLUGINS_JSON=""
if command -v gopls &>/dev/null; then
    PLUGINS_JSON="${PLUGINS_JSON}\"gopls-lsp@claude-plugins-official\":true,"
    echo "[entrypoint] Enabling gopls LSP plugin"
fi
if command -v rust-analyzer &>/dev/null; then
    PLUGINS_JSON="${PLUGINS_JSON}\"rust-analyzer-lsp@claude-plugins-official\":true,"
    echo "[entrypoint] Enabling rust-analyzer LSP plugin"
fi

if [ -n "${PLUGINS_JSON}" ]; then
    PLUGINS_JSON="{${PLUGINS_JSON%,}}"
    SETTINGS_JSON=$(echo "${SETTINGS_JSON}" | jq --argjson p "${PLUGINS_JSON}" '. + {enabledPlugins: $p}')
fi

echo "${SETTINGS_JSON}" | jq . > "${CLAUDE_DIR}/settings.json"

# Materialize hooks from config beads (writes workspace .claude/settings.json).
# Falls back to hardcoded hooks if daemon is unreachable or no config beads exist.
mkdir -p "${WORKSPACE}/.claude"
MATERIALIZED=0

if command -v kd &>/dev/null; then
    echo "[entrypoint] Materializing hooks from config beads (role: ${ROLE})"
    if kd setup claude --workspace="${WORKSPACE}" --role="${ROLE}" 2>&1; then
        if grep -q '"hooks"' "${WORKSPACE}/.claude/settings.json" 2>/dev/null; then
            MATERIALIZED=1
            echo "[entrypoint] Hooks materialized from config beads"
        fi
    fi
fi

if [ "${MATERIALIZED}" = "0" ]; then
    echo "[entrypoint] No config beads found, writing static hooks"
    cat > "${WORKSPACE}/.claude/settings.json" <<'HOOKS'
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "/hooks/prime.sh 2>/dev/null || true"},
          {"type": "command", "command": "/hooks/check-mail.sh 2>/dev/null || true"}
        ]
      }
    ],
    "PreCompact": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "/hooks/prime.sh 2>/dev/null || true"}
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "/hooks/check-mail.sh 2>/dev/null || true"}
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "/hooks/drain-queue.sh --quiet 2>/dev/null || true"}
        ]
      }
    ],
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "/hooks/stop-gate.sh"}
        ]
      }
    ]
  }
}
HOOKS
fi

# Write CLAUDE.md with role context if not already present.
if [ ! -f "${WORKSPACE}/CLAUDE.md" ]; then
    cat > "${WORKSPACE}/CLAUDE.md" <<CLAUDEMD
# Gasboat Agent: ${ROLE}

You are the **${ROLE}** agent${PROJECT:+ (project: ${PROJECT})}.
Agent name: ${AGENT}

## Quick Reference

- \`kd ready\` — See your workflow steps
- \`kd mail inbox\` — Check messages
- \`kd show <issue>\` — View specific issue details

## Checkpoint Protocol (Stop Hook)

When you hit a Stop hook block, you MUST create a decision checkpoint:

1. Review what you accomplished
2. Create a decision:
   \`\`\`bash
   kd decision create --no-wait \\
     --prompt="<what you did and why these options>" \\
     --options='[{"id":"opt1","short":"Option 1","label":"Full description"}]'
   \`\`\`
3. Run \`kd yield\` — blocks until human responds
4. When \`kd yield\` returns, act on the response
CLAUDEMD
fi

# Append dev tools section (guard: only append once)
if ! grep -q "## Development Tools" "${WORKSPACE}/CLAUDE.md" 2>/dev/null; then
    cat >> "${WORKSPACE}/CLAUDE.md" <<'DEVTOOLS'

## Development Tools

All tools are installed directly in the agent image — use them from the command line.

| Tool | Command | Notes |
|------|---------|-------|
| Go | `go build`, `go test` | + `gopls` LSP server |
| Rust | `rustc`, `cargo` | Full toolchain + `rust-analyzer` LSP |
| Node.js | `node`, `npm`, `npx` | |
| Bun | `bun`, `bunx` | Fast JS runtime + package manager |
| Python 3 | `python3`, `uv`, `uvx` | `uv` for fast package management |
| Task | `task` | Taskfile runner |
| Helm | `helm` | K8s chart management |
| kubectl | `kubectl` | |
| AWS CLI | `aws` | |
| Docker CLI | `docker` | Client only (no daemon) |
| GitHub CLI | `gh` | |
| GitLab CLI | `glab` | |
| git | `git`, `git-lfs` | HTTPS + SSH protocols |
| Build tools | `make`, `gcc`, `g++` | |
| Utilities | `curl`, `jq`, `unzip`, `ssh` | |
DEVTOOLS
fi

# ── Skip Claude onboarding wizard ─────────────────────────────────────────

printf '{"hasCompletedOnboarding":true,"lastOnboardingVersion":"2.1.37","preferredTheme":"dark","bypassPermissionsModeAccepted":true}\n' > "${HOME}/.claude.json"

# ── Start coop + Claude ──────────────────────────────────────────────────
#
# We keep bash as PID 1 (no exec) so the pod survives if Claude/coop exit.
# On child exit we clean up FIFO pipes and restart with --resume.
# SIGTERM from K8s is forwarded to coop for graceful shutdown.

cd "${WORKSPACE}"

COOP_CMD="coop --agent=claude --port 8080 --port-health 9090 --cols 200 --rows 50"

# Claude CLI args (model selection, permissions bypass).
CLAUDE_ARGS="--dangerously-skip-permissions"
if [ -n "${CLAUDE_MODEL:-}" ]; then
    CLAUDE_ARGS="--model ${CLAUDE_MODEL} ${CLAUDE_ARGS}"
fi

# Coop log level (overridable via pod env).
export COOP_LOG_LEVEL="${COOP_LOG_LEVEL:-info}"

# ── Auto-bypass startup prompts ────────────────────────────────────────
auto_bypass_startup() {
    false_positive_count=0
    for i in $(seq 1 30); do
        sleep 2
        state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null) || continue
        agent_state=$(echo "${state}" | jq -r '.state // empty' 2>/dev/null)
        prompt_type=$(echo "${state}" | jq -r '.prompt.type // empty' 2>/dev/null)
        subtype=$(echo "${state}" | jq -r '.prompt.subtype // empty' 2>/dev/null)

        # Handle interactive prompts while agent is in "starting" state.
        if [ "${agent_state}" = "starting" ]; then
            screen=$(curl -sf http://localhost:8080/api/v1/screen/text 2>/dev/null)

            # Handle "Resume Session" picker — press Escape to start fresh.
            if echo "${screen}" | grep -q "Resume Session"; then
                echo "[entrypoint] Detected resume session picker, pressing Escape to start fresh"
                curl -sf -X POST http://localhost:8080/api/v1/input/keys \
                    -H 'Content-Type: application/json' \
                    -d '{"keys":["Escape"]}' 2>&1 || true
                sleep 3
                continue
            fi

            # Handle "Detected a custom API key" prompt.
            if echo "${screen}" | grep -q "Detected a custom API key"; then
                echo "[entrypoint] Detected API key prompt, selecting 'Yes' to use it"
                curl -sf -X POST http://localhost:8080/api/v1/input/keys \
                    -H 'Content-Type: application/json' \
                    -d '{"keys":["Up","Return"]}' 2>&1 || true
                sleep 3
                continue
            fi
        fi

        if [ "${prompt_type}" = "setup" ]; then
            screen=$(curl -sf http://localhost:8080/api/v1/screen 2>/dev/null)
            if echo "${screen}" | grep -q "No, exit"; then
                echo "[entrypoint] Auto-accepting setup prompt (subtype: ${subtype})"
                curl -sf -X POST http://localhost:8080/api/v1/agent/respond \
                    -H 'Content-Type: application/json' \
                    -d '{"option":2}' 2>&1 || true
                false_positive_count=0
                sleep 5
                continue
            else
                false_positive_count=$((false_positive_count + 1))
                if [ "${false_positive_count}" -ge 5 ]; then
                    echo "[entrypoint] Skipping false-positive setup prompt (no dialog after ${false_positive_count} checks)"
                    return 0
                fi
                continue
            fi
        fi
        # If agent is past setup prompts, we're done
        agent_state=$(echo "${state}" | jq -r '.state // empty' 2>/dev/null)
        if [ "${agent_state}" = "idle" ] || [ "${agent_state}" = "working" ]; then
            return 0
        fi
    done
    echo "[entrypoint] WARNING: auto-bypass timed out after 60s"
}

# ── Inject initial work prompt ────────────────────────────────────────
inject_initial_prompt() {
    # Wait for agent to be past setup and idle
    for i in $(seq 1 60); do
        sleep 2
        state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null) || continue
        agent_state=$(echo "${state}" | jq -r '.state // empty' 2>/dev/null)
        if [ "${agent_state}" = "idle" ]; then
            break
        fi
        # If agent is already working (hook triggered it), no nudge needed
        if [ "${agent_state}" = "working" ]; then
            echo "[entrypoint] Agent already working, skipping initial prompt"
            return 0
        fi
    done

    local nudge_msg="Check \`kd ready\` for your workflow steps and begin working."

    echo "[entrypoint] Injecting initial work prompt (role: ${ROLE})"
    response=$(curl -sf -X POST http://localhost:8080/api/v1/agent/nudge \
        -H 'Content-Type: application/json' \
        -d "{\"message\": \"${nudge_msg}\"}" 2>&1) || {
        echo "[entrypoint] WARNING: nudge failed: ${response}"
        return 0
    }

    delivered=$(echo "${response}" | jq -r '.delivered // false' 2>/dev/null)
    if [ "${delivered}" = "true" ]; then
        echo "[entrypoint] Initial prompt delivered successfully"
    else
        reason=$(echo "${response}" | jq -r '.reason // "unknown"' 2>/dev/null)
        echo "[entrypoint] WARNING: nudge not delivered: ${reason}"
    fi
}

# ── OAuth credential refresh ────────────────────────────────────────────
OAUTH_TOKEN_URL="https://platform.claude.com/v1/oauth/token"
OAUTH_CLIENT_ID="9d1c250a-e61b-44d9-88ed-5944d1962f5e"
CREDS_FILE="${CLAUDE_STATE}/.credentials.json"

refresh_credentials() {
    # Skip refresh entirely when using API key mode — no OAuth credentials to refresh.
    if [ -n "${ANTHROPIC_API_KEY:-}" ] && [ ! -f "${CREDS_FILE}" ]; then
        echo "[entrypoint] API key mode — skipping OAuth refresh loop"
        return 0
    fi
    sleep 30  # Let Claude start first
    local consecutive_failures=0
    local max_failures=5
    while true; do
        sleep 300  # Check every 5 minutes

        if [ ! -f "${CREDS_FILE}" ]; then
            continue
        fi

        expires_at=$(jq -r '.claudeAiOauth.expiresAt // 0' "${CREDS_FILE}" 2>/dev/null)
        refresh_token=$(jq -r '.claudeAiOauth.refreshToken // empty' "${CREDS_FILE}" 2>/dev/null)

        if [ -z "${refresh_token}" ] || [ "${expires_at}" = "0" ]; then
            continue
        fi

        # Coop-provisioned credentials use a sentinel expiresAt (>= 10^12 ms).
        # Skip refresh — these are managed by coop profiles.
        if [ "${expires_at}" -ge 9999999999000 ] 2>/dev/null; then
            consecutive_failures=0
            continue
        fi

        # Check if within 1 hour of expiry (3600000ms)
        now_ms=$(date +%s)000
        remaining_ms=$((expires_at - now_ms))
        if [ "${remaining_ms}" -gt 3600000 ]; then
            consecutive_failures=0
            continue
        fi

        echo "[entrypoint] OAuth token expires in $((remaining_ms / 60000))m, refreshing..."

        response=$(curl -sf "${OAUTH_TOKEN_URL}" \
            -H 'Content-Type: application/json' \
            -d "{\"grant_type\":\"refresh_token\",\"refresh_token\":\"${refresh_token}\",\"client_id\":\"${OAUTH_CLIENT_ID}\"}" 2>/dev/null) || {
            consecutive_failures=$((consecutive_failures + 1))
            echo "[entrypoint] WARNING: OAuth refresh request failed (attempt ${consecutive_failures}/${max_failures})"
            if [ "${consecutive_failures}" -ge "${max_failures}" ]; then
                agent_state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null | jq -r '.state // empty' 2>/dev/null)
                if [ "${agent_state}" = "working" ] || [ "${agent_state}" = "idle" ]; then
                    echo "[entrypoint] WARNING: OAuth refresh failing but agent is ${agent_state}, not terminating"
                    consecutive_failures=0
                    continue
                fi
                echo "[entrypoint] FATAL: OAuth refresh failed ${max_failures} consecutive times, terminating pod"
                kill -TERM $$ 2>/dev/null || kill -TERM 1 2>/dev/null
                exit 1
            fi
            continue
        }

        new_access_token=$(echo "${response}" | jq -r '.access_token // empty' 2>/dev/null)
        new_refresh_token=$(echo "${response}" | jq -r '.refresh_token // empty' 2>/dev/null)
        expires_in=$(echo "${response}" | jq -r '.expires_in // 0' 2>/dev/null)

        if [ -z "${new_access_token}" ] || [ -z "${new_refresh_token}" ]; then
            consecutive_failures=$((consecutive_failures + 1))
            echo "[entrypoint] WARNING: OAuth refresh returned invalid response (attempt ${consecutive_failures}/${max_failures})"
            if [ "${consecutive_failures}" -ge "${max_failures}" ]; then
                agent_state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null | jq -r '.state // empty' 2>/dev/null)
                if [ "${agent_state}" = "working" ] || [ "${agent_state}" = "idle" ]; then
                    echo "[entrypoint] WARNING: OAuth refresh failing but agent is ${agent_state}, not terminating"
                    consecutive_failures=0
                    continue
                fi
                echo "[entrypoint] FATAL: OAuth refresh failed ${max_failures} consecutive times, terminating pod"
                kill -TERM $$ 2>/dev/null || kill -TERM 1 2>/dev/null
                exit 1
            fi
            continue
        fi

        consecutive_failures=0
        new_expires_at=$(( $(date +%s) * 1000 + expires_in * 1000 ))

        jq --arg at "${new_access_token}" \
           --arg rt "${new_refresh_token}" \
           --argjson ea "${new_expires_at}" \
           '.claudeAiOauth.accessToken = $at | .claudeAiOauth.refreshToken = $rt | .claudeAiOauth.expiresAt = $ea' \
           "${CREDS_FILE}" > "${CREDS_FILE}.tmp" && mv "${CREDS_FILE}.tmp" "${CREDS_FILE}"

        echo "[entrypoint] OAuth credentials refreshed (expires in $((expires_in / 3600))h)"
    done
}

# ── Monitor agent exit and shut down coop ──────────────────────────────
monitor_agent_exit() {
    sleep 10
    while true; do
        sleep 5
        state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null) || break
        agent_state=$(echo "${state}" | jq -r '.state // empty' 2>/dev/null)
        if [ "${agent_state}" = "exited" ]; then
            echo "[entrypoint] Agent exited, requesting coop shutdown"
            curl -sf -X POST http://localhost:8080/api/v1/shutdown 2>/dev/null || true
            return 0
        fi
    done
}

# ── Mux registration ──────────────────────────────────────────────────────
MUX_SESSION_ID=""
register_with_mux() {
    local mux_url="${COOP_MUX_URL}"
    if [ -z "${mux_url}" ]; then
        return 0
    fi

    # Wait for local coop to be healthy
    for i in $(seq 1 30); do
        sleep 2
        curl -sf http://localhost:8080/api/v1/health >/dev/null 2>&1 && break
    done

    local session_id="${HOSTNAME:-$(hostname)}"
    local coop_url="http://${POD_IP:-$(hostname -i 2>/dev/null || echo localhost)}:8080"
    local auth_token="${COOP_AUTH_TOKEN:-${COOP_BROKER_TOKEN:-}}"
    local mux_auth="${COOP_MUX_AUTH_TOKEN:-${auth_token}}"

    echo "[entrypoint] Registering with mux: id=${session_id} url=${coop_url}"

    local payload
    payload=$(jq -n \
        --arg url "${coop_url}" \
        --arg id "${session_id}" \
        --arg role "${BOAT_ROLE:-unknown}" \
        --arg agent "${BOAT_AGENT:-unknown}" \
        --arg pod "${HOSTNAME:-}" \
        --arg ip "${POD_IP:-}" \
        '{url: $url, id: $id, metadata: {role: $role, agent: $agent, k8s: {pod: $pod, ip: $ip}}}')

    if [ -n "${auth_token}" ]; then
        payload=$(echo "${payload}" | jq --arg t "${auth_token}" '.auth_token = $t')
    fi

    local result
    result=$(curl -sf -X POST "${mux_url}/api/v1/sessions" \
        -H 'Content-Type: application/json' \
        ${mux_auth:+-H "Authorization: Bearer ${mux_auth}"} \
        -d "${payload}" 2>&1) || {
        echo "[entrypoint] WARNING: mux registration failed: ${result}"
        return 0
    }

    MUX_SESSION_ID="${session_id}"
    echo "[entrypoint] Registered with mux as '${session_id}'"
}

deregister_from_mux() {
    if [ -z "${COOP_MUX_URL}" ] || [ -z "${MUX_SESSION_ID}" ]; then
        return 0
    fi
    local mux_auth="${COOP_MUX_AUTH_TOKEN:-${COOP_AUTH_TOKEN:-}}"
    curl -sf -X DELETE "${COOP_MUX_URL}/api/v1/sessions/${MUX_SESSION_ID}" \
        ${mux_auth:+-H "Authorization: Bearer ${mux_auth}"} >/dev/null 2>&1 || true
    echo "[entrypoint] Deregistered from mux (${MUX_SESSION_ID})"
    MUX_SESSION_ID=""
}

# ── Signal forwarding ─────────────────────────────────────────────────────
COOP_PID=""
forward_signal() {
    deregister_from_mux
    if [ -n "${COOP_PID}" ]; then
        echo "[entrypoint] Forwarding $1 to coop (pid ${COOP_PID})"
        kill -"$1" "${COOP_PID}" 2>/dev/null || true
        wait "${COOP_PID}" 2>/dev/null || true
    fi
    exit 0
}
trap 'forward_signal TERM' TERM
trap 'forward_signal INT' INT

# Start credential refresh in background (survives coop restarts).
refresh_credentials &

# ── Restart loop ──────────────────────────────────────────────────────────
MAX_RESTARTS="${COOP_MAX_RESTARTS:-10}"
restart_count=0
MIN_RUNTIME_SECS=30

while true; do
    if [ "${restart_count}" -ge "${MAX_RESTARTS}" ]; then
        echo "[entrypoint] Max restarts (${MAX_RESTARTS}) reached, exiting"
        exit 1
    fi

    # Clean up stale FIFO pipes before each start.
    if [ -d "${COOP_STATE}/sessions" ]; then
        find "${COOP_STATE}/sessions" -name 'hook.pipe' -delete 2>/dev/null || true
    fi

    # Find latest session log for resume.
    RESUME_FLAG=""
    MAX_STALE_RETRIES=2
    STALE_COUNT=$( (find "${CLAUDE_STATE}/projects" -maxdepth 2 -name '*.jsonl.stale' -type f 2>/dev/null || true) | wc -l | tr -d ' ')
    if [ "${SESSION_RESUME}" = "1" ] && [ -d "${CLAUDE_STATE}/projects" ] && [ "${STALE_COUNT:-0}" -lt "${MAX_STALE_RETRIES}" ]; then
        LATEST_LOG=$( (find "${CLAUDE_STATE}/projects" -maxdepth 2 -name '*.jsonl' -not -path '*/subagents/*' -type f -printf '%T@ %p\n' 2>/dev/null || true) \
            | sort -rn | head -1 | cut -d' ' -f2-)
        if [ -n "${LATEST_LOG}" ]; then
            RESUME_FLAG="--resume ${LATEST_LOG}"
        fi
    elif [ "${STALE_COUNT:-0}" -ge "${MAX_STALE_RETRIES}" ]; then
        echo "[entrypoint] Skipping resume: ${STALE_COUNT} stale session(s) found (max ${MAX_STALE_RETRIES}), starting fresh"
    fi

    start_time=$(date +%s)

    if [ -n "${RESUME_FLAG}" ]; then
        echo "[entrypoint] Starting coop + claude (${ROLE}/${AGENT}) with resume"
        ${COOP_CMD} ${RESUME_FLAG} -- claude ${CLAUDE_ARGS} &
        COOP_PID=$!
        (auto_bypass_startup && inject_initial_prompt) &
        monitor_agent_exit &
        wait "${COOP_PID}" 2>/dev/null && exit_code=0 || exit_code=$?
        COOP_PID=""

        if [ "${exit_code}" -ne 0 ] && [ -n "${LATEST_LOG}" ] && [ -f "${LATEST_LOG}" ]; then
            echo "[entrypoint] Resume failed (exit ${exit_code}), retiring stale session log"
            mv "${LATEST_LOG}" "${LATEST_LOG}.stale"
            echo "[entrypoint]   renamed: ${LATEST_LOG} -> ${LATEST_LOG}.stale"
        fi
    else
        echo "[entrypoint] Starting coop + claude (${ROLE}/${AGENT})"
        ${COOP_CMD} -- claude ${CLAUDE_ARGS} &
        COOP_PID=$!
        (auto_bypass_startup && inject_initial_prompt) &
        monitor_agent_exit &
        wait "${COOP_PID}" 2>/dev/null && exit_code=0 || exit_code=$?
        COOP_PID=""
    fi

    elapsed=$(( $(date +%s) - start_time ))
    echo "[entrypoint] Coop exited with code ${exit_code} after ${elapsed}s"

    if [ "${elapsed}" -ge "${MIN_RUNTIME_SECS}" ]; then
        restart_count=0
    fi

    restart_count=$((restart_count + 1))
    echo "[entrypoint] Restarting (attempt ${restart_count}/${MAX_RESTARTS}) in 2s..."
    sleep 2
done
