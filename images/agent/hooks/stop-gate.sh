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
    cat <<'CHECKPOINT'
<system-reminder>
STOP BLOCKED — decision gate unsatisfied. You MUST create a decision checkpoint before stopping.

## What Is a Decision Checkpoint?

A checkpoint is a structured handoff: you summarize your work, propose next steps as
options, and wait for human direction. Each option declares what artifact you will
produce when chosen, so the human knows what to expect.

## Steps

### 1. Summarize your session
Review what you accomplished, what's blocked, and what remains.

### 2. Create a decision with concrete options
Every option MUST have an `artifact_type` — this tells the human what deliverable
you will produce if they pick that option.

```bash
gb decision create --no-wait \
  --prompt="Completed X and Y. Blocked on Z. Recommending option A because..." \
  --options='[
    {"id":"continue","short":"Continue work","label":"Finish the remaining implementation and write tests","artifact_type":"report"},
    {"id":"rethink","short":"Change approach","label":"Switch to alternative design per discussion","artifact_type":"plan"},
    {"id":"file-bug","short":"File a bug","label":"The blocker is a bug in dependency X — file it","artifact_type":"bug"}
  ]'
```

**Artifact types**: `report` (summary of work), `plan` (implementation plan), `checklist` (verification steps), `diff-summary` (code change summary), `epic` (feature breakdown), `bug` (bug report)

**Writing good prompts**: Lead with what you did, then why these options make sense.
The prompt appears in Slack — make it useful for someone catching up.

**Writing good options**: Each option should be a distinct, actionable next step.
Use `short` for the button label (2-3 words) and `label` for the full description.

### 3. Yield and wait
```bash
gb yield
```
This blocks until the human responds. When it returns, act on their choice.

### 4. Fulfill the artifact requirement
If the chosen option has an artifact_type, `gb yield` will exit with an error
telling you what artifact to produce. Submit it:
```bash
gb decision report <decision-id> --content '<artifact content in markdown>'
```
Then continue with your work.
</system-reminder>
CHECKPOINT
    exit 2
fi

# Gate verified by the server (gb bus emit already confirmed gate_satisfied_by
# was "yield", "operator", or "manual-force" before allowing the stop).
# Clear any remaining gate state so the next session must re-satisfy from scratch.
gb gate clear decision 2>/dev/null || true

exit 0
