#!/bin/bash
# Stop hook gate: calls gb bus emit and handles block by injecting
# checkpoint protocol instructions so the agent knows what to do.
#
# Exit codes (Claude Code hook protocol):
#   0 = allow (agent may stop)
#   2 = block (agent must continue and create a decision checkpoint)

set -uo pipefail

# ── Cooldown debouncing ──────────────────────────────────────────────────
# If we already blocked within the cooldown window, exit 2 silently
# (no text injection). This prevents the loop where every blocked stop
# injects the full checkpoint text, the model processes it (API cost),
# generates a response, and Claude Code immediately tries to stop again.
COOLDOWN_FILE="/tmp/stop-gate-last-block"
BLOCK_COUNT_FILE="/tmp/stop-gate-block-count"
BASE_COOLDOWN_SECS="${STOP_GATE_COOLDOWN_SECS:-30}"
MAX_BLOCKS="${STOP_GATE_MAX_BLOCKS:-5}"

# Read current block count.
block_count=0
if [ -f "$BLOCK_COUNT_FILE" ]; then
    block_count=$(cat "$BLOCK_COUNT_FILE" 2>/dev/null || echo 0)
fi

# ── Escape hatch ───────────────────────────────────────────────────────
# After too many consecutive blocks, allow the stop to prevent infinite
# cost-sink loops (decision → yield → block → repeat).
if [ "$block_count" -ge "$MAX_BLOCKS" ]; then
    echo "[stop-gate] Escape hatch: $block_count consecutive blocks, allowing stop" >&2
    echo "<system-reminder>Stop gate escape hatch activated after repeated blocks.</system-reminder>"
    rm -f "$COOLDOWN_FILE" "$BLOCK_COUNT_FILE"
    exit 0
fi

# ── Exponential cooldown debouncing ────────────────────────────────────
# Cooldown doubles with each block: 30s, 60s, 120s, 240s, 300s (cap).
if [ -f "$COOLDOWN_FILE" ]; then
    last_block=$(cat "$COOLDOWN_FILE" 2>/dev/null || echo 0)
    now=$(date +%s)
    elapsed=$(( now - last_block ))
    # Calculate exponential cooldown: base * 2^(block_count-1), cap at 300s.
    cooldown=$BASE_COOLDOWN_SECS
    i=1
    while [ "$i" -lt "$block_count" ] && [ "$cooldown" -lt 300 ]; do
        cooldown=$(( cooldown * 2 ))
        i=$(( i + 1 ))
    done
    [ "$cooldown" -gt 300 ] && cooldown=300
    if [ "$elapsed" -lt "$cooldown" ]; then
        # Still within cooldown — block silently without re-injecting text.
        exit 2
    fi
fi

# ── Rate-limit escape hatch ─────────────────────────────────────────────
# If the agent is rate-limited, allow the stop unconditionally.
# This prevents the infinite loop: rate limit -> try to stop -> gate blocks ->
# try to create decision -> rate limit again.
_agent_state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null || echo '{}')
_error_cat=$(echo "$_agent_state" | jq -r '.error_category // empty' 2>/dev/null)
if [ "${_error_cat}" = "rate_limited" ]; then
    echo "[stop-gate] Agent is rate-limited, allowing stop without checkpoint" >&2
    gb gate clear decision 2>/dev/null || true
    rm -f "$COOLDOWN_FILE"
    exit 0
fi

# Read stdin (Claude Code hook JSON) and forward to gb bus emit.
# stderr flows through so Claude Code sees the block reason.
_stdin=$(cat)

echo "$_stdin" | gb bus emit --hook=Stop
_rc=$?

if [ $_rc -eq 2 ]; then
    # Record block time and increment counter for exponential backoff.
    date +%s > "$COOLDOWN_FILE"
    block_count=$(( block_count + 1 ))
    echo "$block_count" > "$BLOCK_COUNT_FILE"

    # Gate blocked — inject checkpoint instructions into the conversation via stdout.
    # Prefer config-bead-materialized file; fall back to hardcoded text.
    STOP_GATE_TEXT="/home/agent/.claude/stop-gate-text.md"
    if [ -f "$STOP_GATE_TEXT" ]; then
        cat "$STOP_GATE_TEXT"
    else
        cat <<'CHECKPOINT'
<system-reminder>
STOP BLOCKED — decision gate unsatisfied.

You CANNOT stop without either creating a decision checkpoint OR calling `gb done`.

## If work is DONE

```bash
kd close <bead-id>        # close completed beads
gb done                   # despawn cleanly (bypasses gate)
```

## If BLOCKED and need human input

```bash
gb decision create --no-wait \
  --prompt="Did X. Blocked on Y. Recommending option A because..." \
  --options='[
    {"id":"continue","short":"Continue work","label":"Finish the remaining implementation","artifact_type":"report"},
    {"id":"rethink","short":"Change approach","label":"Switch to alternative design","artifact_type":"plan"}
  ]'
gb yield
# IMPORTANT: after yield returns, CONTINUE WORKING on the decision outcome.
# Do NOT stop or create another decision. Act on the response.
```
</system-reminder>
CHECKPOINT
    fi

    # After repeated blocks, add escalating guidance.
    if [ "$block_count" -gt 1 ]; then
        echo "<system-reminder>You have been blocked ${block_count} time(s). After gb yield returns, CONTINUE WORKING on the decision outcome. Only call gb done when all work is truly complete.</system-reminder>"
    fi

    exit 2
fi

# Gate allowed — clear cooldown and block count files.
rm -f "$COOLDOWN_FILE" "$BLOCK_COUNT_FILE"

# Clear any remaining gate state so the next session must re-satisfy from scratch.
gb gate clear decision 2>/dev/null || true

exit 0
