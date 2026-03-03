#!/bin/bash
# Stop hook gate: calls gb bus emit and handles block by injecting
# checkpoint protocol instructions so the agent knows what to do.
#
# Exit codes (Claude Code hook protocol):
#   0 = allow (agent may stop)
#   2 = block (agent must continue and create a decision checkpoint)

set -uo pipefail

# If the agent is rate-limited, allow the stop unconditionally.
# This prevents the infinite loop: rate limit -> try to stop -> gate blocks ->
# try to create decision -> rate limit again.
_agent_state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null || echo '{}')
_error_cat=$(echo "$_agent_state" | jq -r '.error_category // empty' 2>/dev/null)
if [ "${_error_cat}" = "rate_limited" ]; then
    echo "[stop-gate] Agent is rate-limited, allowing stop without checkpoint" >&2
    gb gate clear decision 2>/dev/null || true
    exit 0
fi

# Read stdin (Claude Code hook JSON) and forward to gb bus emit.
# stderr flows through so Claude Code sees the block reason.
_stdin=$(cat)

# If the agent is rate-limited, it cannot create a decision checkpoint.
# Allow the stop immediately to avoid an infinite block loop.
_agent_state=$(curl -sf http://localhost:8080/api/v1/agent 2>/dev/null)
_error_cat=$(echo "$_agent_state" | jq -r '.error_category // empty')
if [ "$_error_cat" = "rate_limited" ]; then
    echo '[stop-gate] Rate limited — allowing stop without checkpoint'
    exit 0
fi

echo "$_stdin" | gb bus emit --hook=Stop
_rc=$?

if [ $_rc -eq 2 ]; then
    # Gate blocked — inject checkpoint instructions into the conversation via stdout.
    # Prefer config-bead-materialized file; fall back to hardcoded text.
    STOP_GATE_TEXT="/home/agent/.claude/stop-gate-text.md"
    if [ -f "$STOP_GATE_TEXT" ]; then
        cat "$STOP_GATE_TEXT"
    else
        cat <<'CHECKPOINT'
<system-reminder>
STOP BLOCKED — decision gate unsatisfied.

You are an ephemeral agent. If you have finished your work, close your bead(s) and
call `gb done` to despawn. If you are blocked mid-task and need human input, create
a decision checkpoint first.

## If work is DONE (preferred path)

```bash
kd close <bead-id>        # close completed work
git add <files> && git commit -m "..." && git push   # push any code changes
gb done                   # despawn cleanly
```

## If BLOCKED and need human input

1. Create a decision with options (each needs an `artifact_type`):
```bash
gb decision create --no-wait \
  --prompt="Did X. Blocked on Y. Recommending option A because..." \
  --options='[
    {"id":"continue","short":"Continue work","label":"Finish the remaining implementation","artifact_type":"report"},
    {"id":"rethink","short":"Change approach","label":"Switch to alternative design","artifact_type":"plan"}
  ]'
```

2. Yield and wait:
```bash
gb yield
```

3. If the chosen option requires an artifact, submit it:
```bash
gb decision report <decision-id> --content '<artifact content>'
```
</system-reminder>
CHECKPOINT
    fi
    exit 2
fi

# Gate verified by the server (gb bus emit already confirmed gate_satisfied_by
# was "yield", "operator", or "manual-force" before allowing the stop).
# Clear any remaining gate state so the next session must re-satisfy from scratch.
gb gate clear decision 2>/dev/null || true

exit 0
