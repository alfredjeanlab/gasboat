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
#
# Config beads (type=project) drive per-project agent configuration:
#   image            - agent container image override
#   storage_class    - PVC storage class for crew-mode workspaces
#   service_account  - K8s ServiceAccount override
#   rtk_enabled      - enable RTK token optimization
#   cpu_request/cpu_limit/memory_request/memory_limit - pod resource overrides
#   secrets          - per-project K8s secret → env var mappings (JSON)
#   env              - per-project plain env vars (JSON)
#   env_json         - per-project env var overrides (JSON object)
#   repos            - multi-repo clone definitions (JSON)
#   slack_channel    - Slack channel(s) for project resolution
#   auto_assign      - enable/disable auto-assignment for agents
#   prewarmed_pool   - pool config: {enabled, mode, role, min_size, max_size}
#
# All of the above are set on project beads (kd update <project-bead-id> -f key=value)
# and read by the controller at pod creation time. Do NOT hardcode project-specific
# behavior in this entrypoint — use config beads instead.

set -euo pipefail

ROLE="${BOAT_ROLE:-unknown}"
PROJECT="${BOAT_PROJECT:-}"
MODE="${BOAT_MODE:-crew}"
AGENT="${BOAT_AGENT:-unknown}"
WORKSPACE="/home/agent/workspace"
SESSION_RESUME="${BOAT_SESSION_RESUME:-1}"
BOAT_COMMAND="${BOAT_COMMAND:-claude --dangerously-skip-permissions}"

# Detect mock mode: BOAT_COMMAND contains "claudeless"
MOCK_MODE=0
if echo "${BOAT_COMMAND}" | grep -q "claudeless"; then
    MOCK_MODE=1
    echo "[entrypoint] Mock mode enabled (command: ${BOAT_COMMAND})"
fi

# Export platform version for version commands
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
git config --global user.email "${GIT_AUTHOR_EMAIL:-${ROLE}@gasboat.local}"
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

# ── Git committer email resolution ─────────────────────────────────────
# GitLab rejects pushes if the committer email does not match a verified address
# on the account. If GIT_AUTHOR_EMAIL was not explicitly provided, look it up
# from the GitLab account associated with GITLAB_TOKEN.
if [ -z "${GIT_AUTHOR_EMAIL:-}" ] && [ -n "${GITLAB_TOKEN:-}" ]; then
    GL_HOST="${GITLAB_HOST:-gitlab.com}"
    GL_EMAIL=""
    # Try /user/emails first (lists verified addresses).
    GL_EMAIL=$(curl -sf "https://${GL_HOST}/api/v4/user/emails" \
        -H "PRIVATE-TOKEN: ${GITLAB_TOKEN}" 2>/dev/null \
        | jq -r 'first(.[] | select(.confirmed_at != null) | .email) // empty' 2>/dev/null || true)
    # Fallback: derive the noreply email from /user (works when email scope is unavailable).
    if [ -z "${GL_EMAIL}" ]; then
        GL_USER_ID=$(curl -sf "https://${GL_HOST}/api/v4/user" \
            -H "PRIVATE-TOKEN: ${GITLAB_TOKEN}" 2>/dev/null \
            | jq -r '.id // empty' 2>/dev/null || true)
        if [ -n "${GL_USER_ID}" ]; then
            GL_EMAIL="${GL_USER_ID}-noreply@${GL_HOST#https://}"
        fi
    fi
    if [ -n "${GL_EMAIL}" ]; then
        git config --global user.email "${GL_EMAIL}"
        echo "[entrypoint] Git committer email resolved from GitLab account: ${GL_EMAIL}"
    else
        echo "[entrypoint] WARNING: could not resolve GitLab committer email; using ${ROLE}@gasboat.local"
        echo "[entrypoint] TIP: set GIT_AUTHOR_EMAIL env var on the project bead to fix this"
    fi
fi

# ── Repo cloning ──────────────────────────────────────────────────────────
# Clone source repos that agents reference.  Stored under ${WORKSPACE}/repos/
# (PVC-backed) so they persist across restarts.  Uses a shallow clone on first
# start for speed; skips if the repo directory already exists.
#
# Public repos: cloned unconditionally.
# Private repos: require credentials set above (GITLAB_TOKEN for gitlab.com).
#
# To force a fresh clone, delete the relevant directory under workspace/repos/.

_clone_repo() {
    local url="$1"
    local dest="$2"
    if [ -d "${dest}/.git" ]; then
        echo "[entrypoint] Repo already present: ${dest} (skipping clone)"
    else
        echo "[entrypoint] Cloning ${url} → ${dest}"
        git clone --depth 1 --quiet "${url}" "${dest}" 2>&1 \
            || echo "[entrypoint] WARNING: clone failed for ${url} (credentials missing?)"
    fi
}

REPOS_DIR="${WORKSPACE}/repos"
mkdir -p "${REPOS_DIR}"

# Clone reference repos declared on the project bead.
# Init container clones them first; this is a fallback for job mode (EmptyDir).
if [ -n "${BOAT_REFERENCE_REPOS:-}" ]; then
    IFS=',' read -ra REPO_ENTRIES <<< "${BOAT_REFERENCE_REPOS}"
    for entry in "${REPO_ENTRIES[@]}"; do
        repo_name="${entry%%=*}"
        repo_url="${entry#*=}"; repo_url="${repo_url%%:*}"
        _clone_repo "${repo_url}" "${REPOS_DIR}/${repo_name}"
    done
fi

# Initialize git repo in workspace if not already present.
# Persistent roles keep state across restarts via PVC.
if [ ! -d "${WORKSPACE}/.git" ]; then
    echo "[entrypoint] Initializing git repo in ${WORKSPACE}"
    cd "${WORKSPACE}"
    git init -q
    git config user.name "$(git config --global user.name)"
    git config user.email "$(git config --global user.email)"
    # Keep cloned repos out of the workspace's own git index.
    echo "repos/" >> "${WORKSPACE}/.gitignore"
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
# Configure .beads/config.yaml so kd/gb CLIs can talk to the remote daemon.

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
    CLAUDE_STATE="${CLAUDE_DIR}"
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
# Mock mode: skip credential provisioning entirely (claudeless doesn't need credentials).
CREDS_STAGING="/tmp/claude-credentials/credentials.json"
CREDS_PVC="${CLAUDE_STATE}/.credentials.json"
if [ "${MOCK_MODE}" = "1" ]; then
    echo "[entrypoint] Mock mode — skipping credential provisioning"
elif [ -f "${CREDS_PVC}" ]; then
    echo "[entrypoint] Using existing PVC credentials (preserved from refresh)"
elif [ -f "${CREDS_STAGING}" ]; then
    cp "${CREDS_STAGING}" "${CREDS_PVC}"
    echo "[entrypoint] Seeded Claude credentials from K8s secret"
elif [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    echo "[entrypoint] CLAUDE_CODE_OAUTH_TOKEN set — coop will auto-write credentials"
    # Unset API key when OAuth is present to avoid Claude's
    # "Detected a custom API key" confirmation prompt.
    if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
        echo "[entrypoint] Unsetting ANTHROPIC_API_KEY (OAuth takes precedence)"
        unset ANTHROPIC_API_KEY
    fi
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
if [ -d "/ms-playwright" ] && [ -z "${PLAYWRIGHT_BROWSERS_PATH:-}" ]; then
    export PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
    echo "[entrypoint] Set PLAYWRIGHT_BROWSERS_PATH=/ms-playwright"
fi

# ── Claude settings ──────────────────────────────────────────────────────
#
# User-level settings (permissions, plugins, model) and workspace hooks are
# both materialized from config beads via `gb setup claude`. The command
# fetches claude-settings:* and claude-hooks:* config beads, merges them by
# specificity (global → role), and writes:
#   ~/.claude/settings.json           — user-level (permissions, plugins)
#   {workspace}/.claude/settings.json — workspace-level (hooks)
#
# When config beads are unavailable, --defaults installs hardcoded fallbacks.
# Mock mode: skip Claude settings entirely (claudeless doesn't use them).

if [ "${MOCK_MODE}" = "1" ]; then
    echo "[entrypoint] Mock mode — skipping Claude settings materialization"
    # Write minimal workspace settings so hooks still work with claudeless.
    mkdir -p "${WORKSPACE}/.claude"
    echo '{}' > "${CLAUDE_DIR}/settings.json"
else

# Disable interactive features that interrupt autonomous agents.
export CLAUDE_CODE_ENABLE_TASKS="${CLAUDE_CODE_ENABLE_TASKS:-false}"

# Materialize settings + hooks from config beads.
# gb setup claude writes both:
#   ~/.claude/settings.json           (user-level: permissions, plugins, model)
#   {workspace}/.claude/settings.json (workspace-level: hooks)
mkdir -p "${WORKSPACE}/.claude"
MATERIALIZED=0

if command -v gb &>/dev/null; then
    echo "[entrypoint] Materializing settings and hooks from config beads (role: ${ROLE})"
    if gb setup claude --workspace="${WORKSPACE}" --role="${ROLE}" 2>&1; then
        if grep -q '"hooks"' "${WORKSPACE}/.claude/settings.json" 2>/dev/null; then
            MATERIALIZED=1
            echo "[entrypoint] Settings and hooks materialized from config beads"
        fi
    fi
fi

if [ "${MATERIALIZED}" = "0" ]; then
    echo "[entrypoint] No config beads found, installing defaults"
    gb setup claude --defaults --workspace="${WORKSPACE}" 2>&1 || \
        echo "[entrypoint] WARNING: gb setup --defaults failed"
fi

# ── Skip Claude onboarding wizard ─────────────────────────────────────────

printf '{"hasCompletedOnboarding":true,"lastOnboardingVersion":"2.1.37","preferredTheme":"dark","bypassPermissionsModeAccepted":true}\n' > "${HOME}/.claude.json"

fi  # end of MOCK_MODE != 1 (Claude settings block)

# ── Start coop + Claude ──────────────────────────────────────────────────
#
# We keep bash as PID 1 (no exec) so the pod survives if Claude/coop exit.
# On child exit we clean up FIFO pipes and restart with --resume.
# SIGTERM from K8s is forwarded to coop for graceful shutdown.

cd "${WORKSPACE}"

COOP_CMD="coop --agent=claude --port 8080 --port-health 9090 --cols 200 --rows 50"

# Use the pod hostname as the mux session ID so that Slack deep-links
# (which embed the pod name as the URL #fragment) resolve correctly.
export COOP_MUX_SESSION_ID="${HOSTNAME:-$(hostname)}"

# Agent command: use BOAT_COMMAND if set (e.g., "claudeless run /path/to/scenario.toml"
# for E2E testing without consuming API credits). Defaults to real Claude Code.
AGENT_CMD="${BOAT_COMMAND:-claude --dangerously-skip-permissions}"

# Append --model if CLAUDE_MODEL is set and agent command is real Claude (not claudeless).
if [ -n "${CLAUDE_MODEL:-}" ] && [ "${MOCK_MODE}" != "1" ]; then
    AGENT_CMD="${AGENT_CMD} --model ${CLAUDE_MODEL}"
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

            # Handle "Resume Session" picker — press Enter to resume the most recent session.
            # Previously pressed Escape which caused a new .jsonl to accumulate on the PVC.
            if echo "${screen}" | grep -q "Resume Session"; then
                echo "[entrypoint] Detected resume session picker, selecting resume"
                curl -sf -X POST http://localhost:8080/api/v1/input/keys \
                    -H 'Content-Type: application/json' \
                    -d '{"keys":["Return"]}' 2>&1 || true
                sleep 3
                continue
            fi
        fi

        # Handle "Detected a custom API key" prompt — can appear as state
        # "starting" or "prompt" (type=permission, subtype=trust).
        # Normally avoided by unsetting ANTHROPIC_API_KEY when OAuth is
        # present, but kept as a safety net.
        if [ "${agent_state}" = "starting" ] || [ "${prompt_type}" = "permission" ]; then
            screen=$(curl -sf http://localhost:8080/api/v1/screen/text 2>/dev/null)
            if echo "${screen}" | grep -q "Detected a custom API key"; then
                echo "[entrypoint] Detected API key prompt, selecting 'Yes' to use it"
                curl -sf -X POST http://localhost:8080/api/v1/input/keys \
                    -H 'Content-Type: application/json' \
                    -d '{"keys":["Up","Return"]}' 2>&1 || true
                sleep 3
                continue
            fi
            # Handle "trust this folder" permission prompt.
            if echo "${screen}" | grep -q "trust this folder"; then
                echo "[entrypoint] Auto-accepting trust folder prompt"
                curl -sf -X POST http://localhost:8080/api/v1/agent/respond \
                    -H 'Content-Type: application/json' \
                    -d '{"option":0}' 2>&1 || true
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

    # Resolve nudge prompt from config beads via gb nudge-prompt.
    # Task resolution from agent bead dependencies is handled inside gb nudge-prompt.
    local nudge_msg
    if command -v gb &>/dev/null; then
        nudge_msg=$(gb nudge-prompt 2>/dev/null) || nudge_msg=""
    fi

    if [ -z "${nudge_msg}" ]; then
        echo "[entrypoint] WARNING: gb nudge-prompt failed — no nudge-prompts config beads found"
        nudge_msg="Run gb ready to find work."
    fi

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
    # Skip refresh in mock mode — claudeless doesn't use OAuth.
    if [ "${MOCK_MODE}" = "1" ]; then
        echo "[entrypoint] Mock mode — skipping OAuth refresh loop"
        return 0
    fi
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
monitor_agent_idle() {
    [ -z "${KD_AGENT_ID:-}" ] && return 0
    command -v kd &>/dev/null || return 0
    local prev_state=""
    sleep 15
    while true; do
        sleep 10
        state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null) || break
        agent_state=$(echo "${state}" | jq -r '.state // empty' 2>/dev/null)
        if [ "${agent_state}" != "${prev_state}" ] && [ -n "${agent_state}" ]; then
            case "${agent_state}" in
                idle|working)
                    kd update "${KD_AGENT_ID}" -f agent_state="${agent_state}" 2>/dev/null || true
                    ;;
            esac
            prev_state="${agent_state}"
        fi
        # Stop polling once the agent exits.
        [ "${agent_state}" = "exited" ] && break
    done
}

monitor_agent_exit() {
    sleep 10
    while true; do
        sleep 5
        state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null) || break
        agent_state=$(echo "${state}" | jq -r '.state // empty' 2>/dev/null)
        error_category=$(echo "${state}" | jq -r '.error_category // empty' 2>/dev/null)
        if [ "${agent_state}" = "exited" ]; then
            echo "[entrypoint] Agent exited, requesting coop shutdown"
            curl -sf -X POST http://localhost:8080/api/v1/shutdown 2>/dev/null || true
            return 0
        fi
        # Detect rate-limited state and park the pod.
        error_cat=$(echo "${state}" | jq -r '.error_category // empty' 2>/dev/null)
        if [ "${error_cat}" = "rate_limited" ]; then
            echo "[entrypoint] Agent rate-limited, dismissing prompt"
            # Send Enter to select "Stop and wait" from /rate-limit-options.
            curl -sf -X POST http://localhost:8080/api/v1/input/keys \
                -H 'Content-Type: application/json' \
                -d '{"keys":["Return"]}' 2>/dev/null || true
            last_msg=$(echo "${state}" | jq -r '.last_message // empty' 2>/dev/null)
            echo "[entrypoint] Rate limit info: ${last_msg}"
            # Report rate_limited status to the agent bead.
            if [ -n "${KD_AGENT_ID:-}" ] && command -v kd &>/dev/null; then
                kd update "${KD_AGENT_ID}" -f agent_state=rate_limited 2>/dev/null || true
            fi
            # Write sentinel so the restart loop knows to sleep until reset.
            echo "${last_msg}" > /tmp/rate_limit_reset
            sleep 2
            echo "[entrypoint] Requesting coop shutdown (rate-limited)"
            curl -sf -X POST http://localhost:8080/api/v1/shutdown 2>/dev/null || true
            return 0
        fi
    done
}

# ── Signal forwarding ─────────────────────────────────────────────────────
COOP_PID=""
forward_signal() {
    if [ -n "${COOP_PID}" ]; then
        echo "[entrypoint] Graceful shutdown: interrupting Claude before forwarding $1"
        # Send Escape to interrupt Claude mid-generation.
        curl -sf -X POST http://localhost:8080/api/v1/input/keys \
            -H 'Content-Type: application/json' \
            -d '{"keys":["Escape"]}' 2>/dev/null || true
        sleep 2
        # Request graceful coop shutdown via API.
        curl -sf -X POST http://localhost:8080/api/v1/shutdown 2>/dev/null || true
        sleep 3
        # If coop is still running, send SIGTERM.
        if kill -0 "${COOP_PID}" 2>/dev/null; then
            echo "[entrypoint] Forwarding $1 to coop (pid ${COOP_PID})"
            kill -"$1" "${COOP_PID}" 2>/dev/null || true
        fi
        wait "${COOP_PID}" 2>/dev/null || true
    fi
    exit 0
}
trap 'forward_signal TERM' TERM
trap 'forward_signal INT' INT

# Start credential refresh in background (survives coop restarts).
# Skip for mock agent commands — claudeless needs no OAuth credentials.
if [ "${MOCK_MODE}" != "1" ]; then
    refresh_credentials &
fi

# ── Standby mode (prewarmed agents) ──────────────────────────────────────
# When BOAT_STANDBY=true, the agent is prewarmed: workspace is ready but
# Claude should not start until the pool manager assigns work. We poll the
# bead's agent_state via the daemon and wait until it changes from "prewarmed".
if [ "${BOAT_STANDBY:-}" = "true" ] && [ -n "${BOAT_AGENT_BEAD_ID:-}" ]; then
    echo "[entrypoint] Standby mode: waiting for assignment (bead: ${BOAT_AGENT_BEAD_ID})"
    STANDBY_POLL_INTERVAL="${BOAT_STANDBY_POLL:-5}"
    STANDBY_MAX_WAIT="${BOAT_STANDBY_TTL:-1800}"
    standby_elapsed=0

    while true; do
        if [ "${standby_elapsed}" -ge "${STANDBY_MAX_WAIT}" ]; then
            echo "[entrypoint] Standby TTL (${STANDBY_MAX_WAIT}s) exceeded, exiting"
            exit 0
        fi

        # Check agent_state via kd (faster than raw curl + jq).
        if command -v kd &>/dev/null; then
            current_state=$(kd show "${BOAT_AGENT_BEAD_ID}" --json 2>/dev/null \
                | jq -r '.fields.agent_state // "prewarmed"' 2>/dev/null) || current_state="prewarmed"
        else
            # Fallback: query daemon HTTP API directly.
            current_state=$(curl -sf "${BEADS_HTTP_ADDR:-http://localhost:8080}/v1/beads/${BOAT_AGENT_BEAD_ID}" 2>/dev/null \
                | jq -r '.fields.agent_state // "prewarmed"' 2>/dev/null) || current_state="prewarmed"
        fi

        if [ "${current_state}" != "prewarmed" ]; then
            echo "[entrypoint] Assignment received (state: ${current_state}), exiting standby"

            # Hydrate env vars from the assigned bead's fields so the nudge
            # includes thread context (channel, thread_ts, description).
            if command -v kd &>/dev/null; then
                bead_json=$(kd show "${BOAT_AGENT_BEAD_ID}" --json 2>/dev/null) || true
                if [ -n "${bead_json}" ]; then
                    assigned_channel=$(echo "${bead_json}" | jq -r '.fields.slack_thread_channel // empty' 2>/dev/null)
                    assigned_thread_ts=$(echo "${bead_json}" | jq -r '.fields.slack_thread_ts // empty' 2>/dev/null)
                    assigned_project=$(echo "${bead_json}" | jq -r '.fields.project // empty' 2>/dev/null)
                    if [ -n "${assigned_channel}" ]; then
                        export SLACK_THREAD_CHANNEL="${assigned_channel}"
                        echo "[entrypoint] Hydrated SLACK_THREAD_CHANNEL=${assigned_channel}"
                    fi
                    if [ -n "${assigned_thread_ts}" ]; then
                        export SLACK_THREAD_TS="${assigned_thread_ts}"
                        echo "[entrypoint] Hydrated SLACK_THREAD_TS=${assigned_thread_ts}"
                    fi
                    if [ -n "${assigned_project}" ] && [ -z "${PROJECT:-}" ]; then
                        export PROJECT="${assigned_project}"
                        echo "[entrypoint] Hydrated PROJECT=${assigned_project}"
                    fi
                fi
            fi

            break
        fi

        sleep "${STANDBY_POLL_INTERVAL}"
        standby_elapsed=$((standby_elapsed + STANDBY_POLL_INTERVAL))
    done
fi

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
    if [ "${MOCK_MODE}" != "1" ] && [ "${SESSION_RESUME}" = "1" ] && [ -d "${CLAUDE_STATE}/projects" ] && [ "${STALE_COUNT:-0}" -lt "${MAX_STALE_RETRIES}" ]; then
        LATEST_LOG=$( (find "${CLAUDE_STATE}/projects" -maxdepth 2 -name '*.jsonl' -not -path '*/subagents/*' -type f -printf '%T@ %p\n' 2>/dev/null || true) \
            | sort -rn | head -1 | cut -d' ' -f2-)
        if [ -n "${LATEST_LOG}" ] && [ -f "${LATEST_LOG}" ]; then
            # Validate last line is complete JSON. A partial write from an
            # abrupt kill leaves an incomplete line that breaks --resume.
            last_line=$(tail -1 "${LATEST_LOG}" 2>/dev/null)
            if [ -n "${last_line}" ] && ! echo "${last_line}" | jq empty 2>/dev/null; then
                echo "[entrypoint] Truncating corrupted last line from ${LATEST_LOG}"
                # Remove the last (incomplete) line, keeping all complete lines.
                head -n -1 "${LATEST_LOG}" > "${LATEST_LOG}.tmp" && mv "${LATEST_LOG}.tmp" "${LATEST_LOG}"
            fi
            RESUME_FLAG="--resume ${LATEST_LOG}"
        fi
    elif [ "${STALE_COUNT:-0}" -ge "${MAX_STALE_RETRIES}" ]; then
        echo "[entrypoint] Skipping resume: ${STALE_COUNT} stale session(s) found (max ${MAX_STALE_RETRIES}), starting fresh"
    fi

    start_time=$(date +%s)

    if [ -n "${RESUME_FLAG}" ]; then
        echo "[entrypoint] Starting coop + ${AGENT_CMD%% *} (${ROLE}/${AGENT}) with resume"
        ${COOP_CMD} ${RESUME_FLAG} -- ${AGENT_CMD} &
        COOP_PID=$!
        (auto_bypass_startup && inject_initial_prompt) &
        monitor_agent_exit &
        monitor_agent_idle &
        wait "${COOP_PID}" 2>/dev/null && exit_code=0 || exit_code=$?
        COOP_PID=""

        if [ "${exit_code}" -ne 0 ] && [ -n "${LATEST_LOG}" ] && [ -f "${LATEST_LOG}" ]; then
            echo "[entrypoint] Resume failed (exit ${exit_code}), retiring stale session log"
            mv "${LATEST_LOG}" "${LATEST_LOG}.stale"
            echo "[entrypoint]   renamed: ${LATEST_LOG} -> ${LATEST_LOG}.stale"
        fi
    else
        echo "[entrypoint] Starting coop + ${AGENT_CMD%% *} (${ROLE}/${AGENT})"
        ${COOP_CMD} -- ${AGENT_CMD} &
        COOP_PID=$!
        (auto_bypass_startup && inject_initial_prompt) &
        monitor_agent_exit &
        monitor_agent_idle &
        wait "${COOP_PID}" 2>/dev/null && exit_code=0 || exit_code=$?
        COOP_PID=""
    fi

    elapsed=$(( $(date +%s) - start_time ))
    echo "[entrypoint] Coop exited with code ${exit_code} after ${elapsed}s"

    # Check if the agent requested a polite stop (gb stop sets stop_requested=true).
    # Close the bead so the reconciler stops tracking this pod, then exit cleanly.
    if [ -n "${KD_AGENT_ID:-}" ] && command -v kd &>/dev/null && command -v jq &>/dev/null; then
        stop_req=$(kd show "${KD_AGENT_ID}" --json 2>/dev/null | jq -r '.fields.stop_requested // empty')
        if [ "${stop_req}" = "true" ]; then
            echo "[entrypoint] stop_requested — closing agent bead and exiting cleanly"
            kd close "${KD_AGENT_ID}" 2>/dev/null || true
            exit 0
        fi
    fi

    # If rate-limited, sleep until the reset time before restarting.
    if [ -f /tmp/rate_limit_reset ]; then
        rate_msg=$(cat /tmp/rate_limit_reset)
        rm -f /tmp/rate_limit_reset
        # Extract reset hour from message (e.g., "resets 9pm (UTC)" -> "9pm").
        reset_hour=$(echo "${rate_msg}" | grep -oP '\d{1,2}(am|pm)' | head -1)
        sleep_secs=""
        if [ -n "${reset_hour}" ]; then
            target_epoch=$(date -u -d "today ${reset_hour}" +%s 2>/dev/null) || true
            now_epoch=$(date -u +%s)
            if [ -n "${target_epoch}" ] && [ "${target_epoch}" -le "${now_epoch}" ]; then
                target_epoch=$(date -u -d "tomorrow ${reset_hour}" +%s 2>/dev/null) || true
            fi
            if [ -n "${target_epoch}" ] && [ "${target_epoch}" -gt "${now_epoch}" ]; then
                sleep_secs=$((target_epoch - now_epoch + 60))
            fi
        fi
        if [ -n "${sleep_secs}" ]; then
            echo "[entrypoint] Rate limited — sleeping ${sleep_secs}s until ${reset_hour} UTC"
        else
            sleep_secs=1800
            echo "[entrypoint] Rate limited — sleeping ${sleep_secs}s (could not parse reset time)"
        fi
        sleep "${sleep_secs}"
        restart_count=0
        continue
    fi

    if [ "${elapsed}" -ge "${MIN_RUNTIME_SECS}" ]; then
        restart_count=0
    fi

    restart_count=$((restart_count + 1))
    echo "[entrypoint] Restarting (attempt ${restart_count}/${MAX_RESTARTS}) in 2s..."
    sleep 2
done
