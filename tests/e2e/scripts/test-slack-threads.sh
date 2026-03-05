#!/usr/bin/env bash
# test-slack-threads.sh — Manual test scenarios for Slack thread interactions.
#
# Prerequisites:
#   - SLACK_BOT_TOKEN set (xoxb-...)
#   - SLACK_CHANNEL set (C...)
#   - BEADS_HTTP_ADDR set (https://beads...)
#   - jq installed
#
# Usage:
#   ./tests/e2e/scripts/test-slack-threads.sh [scenario]
#
# Scenarios:
#   all           Run all scenarios (default)
#   thread-spawn  Scenario 1: Thread-bound agent spawn
#   decision      Scenario 2: Decision posts in correct thread
#   resolve       Scenario 3: Decision resolution
#   report        Scenario 4: Report delivery
#   lifecycle     Scenario 5: Agent lifecycle (done/failed)
#   restart       Scenario 6: State survives restart
#   multi-agent   Scenario 7: Multiple agents, different threads

set -euo pipefail

: "${SLACK_BOT_TOKEN:?Set SLACK_BOT_TOKEN}"
: "${SLACK_CHANNEL:?Set SLACK_CHANNEL}"
: "${BEADS_HTTP_ADDR:=http://localhost:8080}"

SLACK_API="https://slack.com/api"
AUTH="Authorization: Bearer ${SLACK_BOT_TOKEN}"

# --- Helpers ---

slack_post() {
  local channel="$1" text="$2" thread_ts="${3:-}"
  local payload
  payload=$(jq -n \
    --arg ch "$channel" \
    --arg txt "$text" \
    --arg ts "$thread_ts" \
    '{channel: $ch, text: $txt} + (if $ts != "" then {thread_ts: $ts} else {} end)')
  curl -s -X POST "${SLACK_API}/chat.postMessage" \
    -H "$AUTH" \
    -H "Content-Type: application/json" \
    -d "$payload" | jq -r '.ts'
}

slack_get_replies() {
  local channel="$1" ts="$2"
  curl -s "${SLACK_API}/conversations.replies?channel=${channel}&ts=${ts}&limit=50" \
    -H "$AUTH" | jq -r '.messages[] | "\(.user // .bot_id): \(.text)"'
}

beads_create() {
  local type="$1" title="$2"
  shift 2
  curl -s -X POST "${BEADS_HTTP_ADDR}/api/v1/beads" \
    -H "Content-Type: application/json" \
    -d "{\"type\":\"${type}\",\"title\":\"${title}\"$*}" | jq -r '.id'
}

beads_get() {
  curl -s "${BEADS_HTTP_ADDR}/api/v1/beads/$1" | jq .
}

pass() { echo "  ✓ $1"; }
fail() { echo "  ✗ $1"; FAILURES=$((FAILURES + 1)); }
check() {
  if [ "$1" = "$2" ]; then pass "$3"; else fail "$3 (expected '$2', got '$1')"; fi
}
check_not_empty() {
  if [ -n "$1" ]; then pass "$2"; else fail "$2 (empty)"; fi
}

FAILURES=0

# --- Scenarios ---

scenario_thread_spawn() {
  echo ""
  echo "=== Scenario 1: Thread-bound agent spawn ==="
  echo "Steps:"
  echo "  1. Post a message in ${SLACK_CHANNEL}"
  echo "  2. Reply in the thread mentioning @gasboat"
  echo "  3. Verify an agent bead is created with slack_thread_channel/slack_thread_ts"
  echo "  4. Mention @gasboat again in same thread — should NOT spawn a second agent"
  echo ""
  echo "Manual verification required. Check:"
  echo "  - Bot posts ':zap: Spinning up an agent...' in the thread"
  echo "  - Second mention posts ':information_source: An agent is already working...'"
  echo ""
  pass "Scenario documented (manual)"
}

scenario_decision() {
  echo ""
  echo "=== Scenario 2: Decision posts in correct thread ==="
  echo "Steps:"
  echo "  1. Spawn a thread-bound agent (Scenario 1)"
  echo "  2. The agent creates a decision bead"
  echo "  3. Verify the decision notification appears in the correct Slack thread"
  echo ""
  echo "Manual verification: Decision buttons appear in the agent's bound thread"
  pass "Scenario documented (manual)"
}

scenario_resolve() {
  echo ""
  echo "=== Scenario 3: Decision resolution via button ==="
  echo "Steps:"
  echo "  1. Click a decision button in Slack"
  echo "  2. Confirm in the modal"
  echo "  3. Verify the message updates to show resolved state"
  echo "  4. Verify the agent is nudged via coop"
  echo ""
  echo "Manual verification: Message updates, agent resumes work"
  pass "Scenario documented (manual)"
}

scenario_report() {
  echo ""
  echo "=== Scenario 4: Report delivery ==="
  echo "Steps:"
  echo "  1. Resolve a decision with required_artifact set"
  echo "  2. Agent submits report via 'gb decision report'"
  echo "  3. Verify the report is inlined in the resolved decision message"
  echo "  4. Delete the decision message, then submit another report"
  echo "  5. Verify the fallback: report appears as new message in thread"
  echo ""
  echo "Manual verification: Report content visible in Slack"
  pass "Scenario documented (manual)"
}

scenario_lifecycle() {
  echo ""
  echo "=== Scenario 5: Agent lifecycle ==="
  echo "Steps:"
  echo "  1. Start a thread-bound agent"
  echo "  2. Wait for agent to finish (done state)"
  echo "  3. Verify ':white_check_mark: Agent finished' appears in thread"
  echo "  4. Force-kill an agent pod"
  echo "  5. Verify ':x: Agent failed' appears in thread"
  echo "  6. Wait for periodic prune (5min) or restart bridge"
  echo "  7. Verify stale agent cards are cleaned up"
  echo ""
  pass "Scenario documented (manual)"
}

scenario_restart() {
  echo ""
  echo "=== Scenario 6: State survives restart ==="
  echo "Steps:"
  echo "  1. Spawn a thread-bound agent, create a decision"
  echo "  2. Restart the slack-bridge pod: kubectl rollout restart deploy/gasboat-test-slack-bridge -n gasboat-test"
  echo "  3. Resolve the decision via Slack button"
  echo "  4. Verify the message updates correctly (state was reloaded)"
  echo "  5. Verify the agent card still works (not duplicated)"
  echo ""
  echo "Check: /data/slack-bridge-state.json should contain thread_agents entry"
  pass "Scenario documented (manual)"
}

scenario_multi_agent() {
  echo ""
  echo "=== Scenario 7: Multiple agents in different threads ==="
  echo "Steps:"
  echo "  1. Create thread A, mention @gasboat → spawns agent-A"
  echo "  2. Create thread B, mention @gasboat → spawns agent-B"
  echo "  3. Agent-A creates a decision → appears in thread A only"
  echo "  4. Agent-B creates a decision → appears in thread B only"
  echo "  5. Resolve both — verify no cross-contamination"
  echo ""
  echo "Manual verification: Decisions route to correct threads"
  pass "Scenario documented (manual)"
}

# --- Main ---

scenario="${1:-all}"

echo "Slack Thread Test Scenarios"
echo "=========================="
echo "Channel: ${SLACK_CHANNEL}"
echo "Beads:   ${BEADS_HTTP_ADDR}"

case "$scenario" in
  all)
    scenario_thread_spawn
    scenario_decision
    scenario_resolve
    scenario_report
    scenario_lifecycle
    scenario_restart
    scenario_multi_agent
    ;;
  thread-spawn)  scenario_thread_spawn ;;
  decision)      scenario_decision ;;
  resolve)       scenario_resolve ;;
  report)        scenario_report ;;
  lifecycle)     scenario_lifecycle ;;
  restart)       scenario_restart ;;
  multi-agent)   scenario_multi_agent ;;
  *)
    echo "Unknown scenario: $scenario"
    echo "Available: all, thread-spawn, decision, resolve, report, lifecycle, restart, multi-agent"
    exit 1
    ;;
esac

echo ""
if [ "$FAILURES" -eq 0 ]; then
  echo "All scenarios documented. Run manually in gasboat-test namespace."
else
  echo "Failures: ${FAILURES}"
  exit 1
fi
