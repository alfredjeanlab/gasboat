#!/bin/bash
# stop-decision.sh — Claude Code stop hook for the decision gate.
#
# Check beads for unresolved decisions by this agent.
# If one exists, allow stop (agent properly asked its question).
# If none, block stop (agent must create a decision first).
#
# Exit codes:
#   0 = allow stop (open decision exists, agent goes idle)
#   1 = block stop (no open decision, agent must create one)

set -euo pipefail

AGENT_ID="${BEADS_AGENT_NAME:-unknown}"
DAEMON_URL="${BEADS_DAEMON_HTTP_URL:-http://localhost:9080}"

# Query the daemon for open decision beads assigned to this agent.
# Uses the beads List endpoint with type and assignee filters.
pending=$(curl -sf \
  -H "Content-Type: application/json" \
  -d "{\"issue_type\":\"decision\",\"exclude_status\":[\"closed\"],\"assignee\":\"${AGENT_ID}\"}" \
  "${DAEMON_URL}/bd.v1.BeadsService/List" | jq 'length')

if [ "$pending" -gt 0 ]; then
  # Decision exists — allow stop, agent goes idle waiting for resolution
  exit 0
else
  # No decision — block stop, agent must create one first
  echo "You must create a decision bead before stopping. Use:"
  echo "  kd create --type decision --assign $AGENT_ID --field question='Your question here' --field options='[\"option1\",\"option2\"]'"
  exit 1
fi
