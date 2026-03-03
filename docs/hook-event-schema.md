# Hook Event Schema for NATS

## Overview

Claude Code hooks in agent pods publish tool and lifecycle events to the
`HOOK_EVENTS` NATS JetStream stream so beads3d can visualize live agent
activity ("doots").

## NATS Stream

- **Stream:** `HOOK_EVENTS` (already exists)
- **Subject pattern:** `hooks.>`
- **Subject format:** `hooks.<agent>.<event>`
  - Example: `hooks.worker-1.PreToolUse`

## Events to Capture

### High-priority (real-time activity visualization)

| Event | Purpose | Fires when |
|-------|---------|------------|
| `PreToolUse` | Show tool about to execute | Before tool runs |
| `PostToolUse` | Show tool result | After tool succeeds |
| `Stop` | Agent finished responding | Turn complete |
| `SubagentStart` | Parallel work spawned | Subagent created |
| `SubagentStop` | Parallel work finished | Subagent done |

### Medium-priority (session lifecycle)

| Event | Purpose | Fires when |
|-------|---------|------------|
| `SessionStart` | Agent session began | New/resumed session |
| `SessionEnd` | Agent session ended | Session terminates |
| `PreCompact` | Context compaction | Before compaction |

### Agent teams (requires `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`)

| Event | Purpose | Fires when |
|-------|---------|------------|
| `TeammateIdle` | Teammate finished work, awaiting direction | Teammate becomes idle |
| `TaskCompleted` | Task marked done in shared task list | Task status → completed |

### Not captured

| Event | Reason |
|-------|--------|
| `UserPromptSubmit` | In agent pods these are system prompts, low signal |
| `PermissionRequest` | Agents use dontAsk/bypassPermissions mode |
| `Notification` | Internal to Claude Code UI |
| `PostToolUseFailure` | Captured via `PostToolUse` with error field |
| `ConfigChange` | Infrastructure noise |
| `WorktreeCreate/Remove` | Low frequency, not useful for doots |

## Payload Schema

All events share a common envelope:

```json
{
  "agent": "worker-1",
  "session_id": "abc-123-def",
  "event": "PreToolUse",
  "ts": "2026-03-02T07:30:00.000Z",
  "cwd": "/home/agent/workspace"
}
```

### Common Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent` | string | yes | Agent name from `$HOSTNAME` (e.g., `worker-1`) |
| `session_id` | string | yes | Claude Code session ID |
| `event` | string | yes | Hook event name |
| `ts` | string | yes | ISO 8601 timestamp with milliseconds |
| `cwd` | string | no | Working directory |

### PreToolUse

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "PreToolUse",
  "ts": "2026-03-02T07:30:00.000Z",
  "tool_name": "Bash",
  "tool_input": {
    "command": "go test ./...",
    "description": "Run tests"
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `tool_name` | string | Tool name: `Bash`, `Edit`, `Write`, `Read`, `Glob`, `Grep`, `Agent`, `WebFetch`, `WebSearch`, MCP tools |
| `tool_input` | object | Tool parameters (truncated to 1KB if large) |

### PostToolUse

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "PostToolUse",
  "ts": "2026-03-02T07:30:01.500Z",
  "tool_name": "Bash",
  "tool_input": {
    "command": "go test ./..."
  },
  "tool_response": {
    "success": true
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `tool_name` | string | Same as PreToolUse |
| `tool_input` | object | Tool parameters (truncated) |
| `tool_response` | object | Tool result summary (truncated to 1KB) |

### Stop

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "Stop",
  "ts": "2026-03-02T07:35:00.000Z"
}
```

No additional fields. The `stop_hook_active` field from Claude Code
is not forwarded since it's internal to hook loop prevention.

### SubagentStart

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "SubagentStart",
  "ts": "2026-03-02T07:30:02.000Z",
  "subagent_id": "agent-abc123",
  "subagent_type": "Explore"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `subagent_id` | string | Subagent instance ID |
| `subagent_type` | string | Agent type: `Bash`, `Explore`, `Plan`, etc. |

### SubagentStop

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "SubagentStop",
  "ts": "2026-03-02T07:30:05.000Z",
  "subagent_id": "agent-abc123",
  "subagent_type": "Explore"
}
```

Same fields as SubagentStart.

### SessionStart

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "SessionStart",
  "ts": "2026-03-02T07:25:00.000Z",
  "source": "startup"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `source` | string | `startup`, `resume`, `clear`, or `compact` |

### SessionEnd

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "SessionEnd",
  "ts": "2026-03-02T08:00:00.000Z",
  "reason": "other"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `reason` | string | `clear`, `logout`, `prompt_input_exit`, `bypass_permissions_disabled`, `other` |

### PreCompact

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "PreCompact",
  "ts": "2026-03-02T07:45:00.000Z",
  "trigger": "auto"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `trigger` | string | `manual` or `auto` |

### TeammateIdle

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "TeammateIdle",
  "ts": "2026-03-02T07:40:00.000Z",
  "teammate_id": "teammate-xyz",
  "teammate_type": "general-purpose"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `teammate_id` | string | ID of the teammate that became idle |
| `teammate_type` | string | Teammate agent type (e.g., `general-purpose`, `Explore`) |

Can be used as a quality gate: exit code 2 sends feedback and keeps
the teammate working instead of idling.

### TaskCompleted

```json
{
  "agent": "worker-1",
  "session_id": "abc-123",
  "event": "TaskCompleted",
  "ts": "2026-03-02T07:42:00.000Z",
  "task_id": "task-001",
  "task_subject": "Implement auth middleware"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `task_id` | string | ID of the completed task |
| `task_subject` | string | Brief description of the task |

Can be used to enforce review before marking tasks done.

## Publishing Mechanism

The hook relay script reads JSON from stdin (Claude Code hook input),
extracts the relevant fields, and publishes to NATS using one of:

1. **HTTP to kbeads daemon** (preferred): `POST $BEADS_HTTP_ADDR/v1/hooks/publish`
   - New endpoint needed on kbeads daemon
   - Daemon handles NATS publishing
   - No NATS client needed in hook script

2. **Direct NATS publish** (fallback): Using `nats pub` CLI or HTTP gateway
   - Requires NATS tools in agent image
   - Uses `$BEADS_NATS_URL` env var

### Hook Configuration (settings.json)

```json
{
  "hooks": {
    "PreToolUse": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "PostToolUse": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "Stop": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "SubagentStart": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "SubagentStop": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "SessionStart": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "SessionEnd": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "PreCompact": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "TeammateIdle": [{"type": "command", "command": "/usr/local/bin/hook-relay"}],
    "TaskCompleted": [{"type": "command", "command": "/usr/local/bin/hook-relay"}]
  }
}
```

A single `hook-relay` script handles all event types. It reads
`hook_event_name` from stdin JSON to determine the event type and
constructs the appropriate NATS subject.

## Size Limits

- `tool_input`: Truncated to 1024 bytes (avoids large file contents in Write/Edit)
- `tool_response`: Truncated to 1024 bytes
- Total message: Soft cap at 4KB

## Compatibility

The schema is forward-compatible with the existing `HOOK_EVENTS` stream:
- Uses the same `hooks.>` subject pattern
- Subject hierarchy `hooks.<agent>.<event>` allows filtering by agent
  or event type (e.g., `hooks.worker-1.>` or `hooks.*.PreToolUse`)
- Consumers can subscribe to `hooks.>` for all events
