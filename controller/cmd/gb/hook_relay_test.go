package main

import (
	"encoding/json"
	"testing"
)

func TestTruncateAny_NilInput(t *testing.T) {
	result := truncateAny(nil, 1024)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestTruncateAny_SmallInput(t *testing.T) {
	input := map[string]string{"command": "go test ./..."}
	result := truncateAny(input, 1024)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Should return the original value unchanged.
	m, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", result)
	}
	if m["command"] != "go test ./..." {
		t.Errorf("unexpected command: %s", m["command"])
	}
}

func TestTruncateAny_LargeInput(t *testing.T) {
	// Create a value that exceeds 100 bytes when marshaled.
	large := map[string]string{"data": string(make([]byte, 200))}
	result := truncateAny(large, 100)
	if result == nil {
		t.Fatal("expected non-nil result for large input")
	}
	s, ok := result.(string)
	if !ok {
		t.Fatalf("expected string (truncated), got %T", result)
	}
	if len(s) > 104 { // 100 bytes + "..."
		t.Errorf("truncated result too long: %d bytes", len(s))
	}
	if s[len(s)-3:] != "..." {
		t.Errorf("expected truncated string to end with '...', got %q", s[len(s)-3:])
	}
}

func TestSanitizeSubject(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"worker-1", "worker-1"},
		{"worker.1", "worker_1"},
		{"my agent", "my_agent"},
		{"clean", "clean"},
		{"a.b c", "a_b_c"},
	}
	for _, tc := range tests {
		got := sanitizeSubject(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeSubject(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestBuildRelayEvent_PreToolUse(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "sess-123",
		"cwd":             "/home/agent/workspace",
		"tool_name":       "Bash",
		"tool_input": map[string]any{
			"command":     "go test ./...",
			"description": "Run tests",
		},
	}

	evt, subject, err := buildRelayEvent(input, "worker-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Event != "PreToolUse" {
		t.Errorf("expected event PreToolUse, got %s", evt.Event)
	}
	if evt.Agent != "worker-1" {
		t.Errorf("expected agent worker-1, got %s", evt.Agent)
	}
	if evt.SessionID != "sess-123" {
		t.Errorf("expected session_id sess-123, got %s", evt.SessionID)
	}
	if evt.ToolName != "Bash" {
		t.Errorf("expected tool_name Bash, got %s", evt.ToolName)
	}
	if evt.ToolInput == nil {
		t.Error("expected tool_input to be set")
	}
	if subject != "hooks.worker-1.PreToolUse" {
		t.Errorf("expected subject hooks.worker-1.PreToolUse, got %s", subject)
	}
}

func TestBuildRelayEvent_PostToolUse(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-456",
		"tool_name":       "Read",
		"tool_input":      map[string]any{"file_path": "/tmp/test.go"},
		"tool_response":   map[string]any{"success": true},
	}

	evt, _, err := buildRelayEvent(input, "worker-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.ToolResponse == nil {
		t.Error("expected tool_response to be set")
	}
}

func TestBuildRelayEvent_SessionStart(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "sess-789",
		"source":          "startup",
	}

	evt, _, err := buildRelayEvent(input, "worker-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Source != "startup" {
		t.Errorf("expected source startup, got %s", evt.Source)
	}
}

func TestBuildRelayEvent_Stop(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "sess-abc",
	}

	evt, subject, err := buildRelayEvent(input, "worker-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Event != "Stop" {
		t.Errorf("expected event Stop, got %s", evt.Event)
	}
	if subject != "hooks.worker-1.Stop" {
		t.Errorf("expected subject hooks.worker-1.Stop, got %s", subject)
	}
}

func TestBuildRelayEvent_SubagentStart(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "SubagentStart",
		"session_id":      "sess-sub",
		"agent_id":        "agent-xyz",
		"agent_type":      "Explore",
	}

	evt, _, err := buildRelayEvent(input, "worker-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.SubagentID != "agent-xyz" {
		t.Errorf("expected subagent_id agent-xyz, got %s", evt.SubagentID)
	}
	if evt.SubagentType != "Explore" {
		t.Errorf("expected subagent_type Explore, got %s", evt.SubagentType)
	}
}

func TestBuildRelayEvent_UnknownEvent(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "UnknownEvent",
		"session_id":      "sess-unk",
	}

	_, _, err := buildRelayEvent(input, "worker-1")
	if err == nil {
		t.Error("expected error for unknown event, got nil")
	}
}

func TestBuildRelayEvent_EmptyEventName(t *testing.T) {
	input := map[string]any{
		"session_id": "sess-empty",
	}

	_, _, err := buildRelayEvent(input, "worker-1")
	if err == nil {
		t.Error("expected error for empty event name, got nil")
	}
}

func TestBuildRelayEvent_PreCompact(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "PreCompact",
		"session_id":      "sess-compact",
		"trigger":         "auto",
	}

	evt, _, err := buildRelayEvent(input, "worker-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Trigger != "auto" {
		t.Errorf("expected trigger auto, got %s", evt.Trigger)
	}
}

func TestBuildRelayEvent_SessionEnd(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "SessionEnd",
		"session_id":      "sess-end",
		"reason":          "clear",
	}

	evt, _, err := buildRelayEvent(input, "worker-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Reason != "clear" {
		t.Errorf("expected reason clear, got %s", evt.Reason)
	}
}

func TestBuildRelayEvent_TeammateIdle(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "TeammateIdle",
		"session_id":      "sess-team",
		"teammate_id":     "teammate-xyz",
		"teammate_type":   "general-purpose",
	}

	evt, subject, err := buildRelayEvent(input, "worker-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Event != "TeammateIdle" {
		t.Errorf("expected event TeammateIdle, got %s", evt.Event)
	}
	if evt.TeammateID != "teammate-xyz" {
		t.Errorf("expected teammate_id teammate-xyz, got %s", evt.TeammateID)
	}
	if evt.TeammateType != "general-purpose" {
		t.Errorf("expected teammate_type general-purpose, got %s", evt.TeammateType)
	}
	if subject != "hooks.worker-1.TeammateIdle" {
		t.Errorf("expected subject hooks.worker-1.TeammateIdle, got %s", subject)
	}
}

func TestBuildRelayEvent_TaskCompleted(t *testing.T) {
	input := map[string]any{
		"hook_event_name": "TaskCompleted",
		"session_id":      "sess-task",
		"task_id":         "task-001",
		"task_subject":    "Implement auth middleware",
	}

	evt, subject, err := buildRelayEvent(input, "worker-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Event != "TaskCompleted" {
		t.Errorf("expected event TaskCompleted, got %s", evt.Event)
	}
	if evt.TaskID != "task-001" {
		t.Errorf("expected task_id task-001, got %s", evt.TaskID)
	}
	if evt.TaskSubject != "Implement auth middleware" {
		t.Errorf("expected task_subject 'Implement auth middleware', got %s", evt.TaskSubject)
	}
	if subject != "hooks.worker-1.TaskCompleted" {
		t.Errorf("expected subject hooks.worker-1.TaskCompleted, got %s", subject)
	}
}

func TestRelayEventJSON_OmitsEmptyFields(t *testing.T) {
	evt := hookRelayEvent{
		Agent:     "worker-1",
		SessionID: "sess-123",
		Event:     "Stop",
		TS:        "2026-03-02T07:35:00.000Z",
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// These fields should be omitted for a Stop event.
	for _, key := range []string{"tool_name", "tool_input", "tool_response", "source", "reason", "subagent_id", "subagent_type", "trigger", "cwd", "teammate_id", "teammate_type", "task_id", "task_subject"} {
		if _, ok := m[key]; ok {
			t.Errorf("expected field %q to be omitted, but it was present", key)
		}
	}

	// These fields should always be present.
	for _, key := range []string{"agent", "session_id", "event", "ts"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected field %q to be present", key)
		}
	}
}
