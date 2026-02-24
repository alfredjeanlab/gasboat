#!/bin/bash
# prime.sh — SessionStart hook that outputs role-specific priming context.
#
# Renders the gasboat context template for this agent's role via
# `kd context <role>`, then shows the agent's own bead (which includes
# its title, description/instructions, and dependencies/assigned work).
#
# Env:
#   BOAT_ROLE           — agent role (captain, crew, job)
#   BOAT_AGENT_BEAD_ID  — this agent's bead ID (set by controller)
#
# Always exits 0 so hook failures don't block Claude.

set -euo pipefail

ROLE="${BOAT_ROLE:-crew}"
BEAD_ID="${BOAT_AGENT_BEAD_ID:-}"

# Render the role's context dashboard from the daemon.
# Jobs have no context config — their entire context is the agent bead below.
if [ "${ROLE}" != "job" ]; then
    kd context "${ROLE}" 2>/dev/null || true
fi

# Show this agent's own bead — title is the task, description holds
# instructions, and dependencies are the assigned work beads.
if [ -n "${BEAD_ID}" ]; then
    echo ""
    echo "## Assignment"
    echo ""
    kd show "${BEAD_ID}" 2>/dev/null || true
fi

exit 0
