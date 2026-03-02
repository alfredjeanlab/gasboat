package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// mockBead is a lightweight bead JSON for test responses.
type mockBead struct {
	ID     string            `json:"id"`
	Title  string            `json:"title"`
	Type   string            `json:"type"`
	Kind   string            `json:"kind"`
	Status string            `json:"status"`
	Fields map[string]string `json:"fields,omitempty"`
}

// mockListResponse mirrors the daemon list response.
type mockListResponse struct {
	Beads []mockBead `json:"beads"`
	Total int        `json:"total"`
}

// setupTestDaemon creates an httptest server that responds to GetBead and
// ListBeadsFiltered requests with the provided data, and sets the package-level
// daemon variable. Returns a cleanup function.
func setupTestDaemon(t *testing.T, agentBead *mockBead, projectBeads []mockBead) func() {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// GetBead: GET /v1/beads/<id>
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/beads/") && r.URL.RawQuery == "" {
			if agentBead != nil {
				_ = json.NewEncoder(w).Encode(agentBead)
			} else {
				http.Error(w, "not found", http.StatusNotFound)
			}
			return
		}

		// ListBeadsFiltered: GET /v1/beads?...
		if r.Method == "GET" && r.URL.Path == "/v1/beads" {
			typeFilter := r.URL.Query().Get("type")
			if typeFilter == "project" {
				resp := mockListResponse{Beads: projectBeads, Total: len(projectBeads)}
				_ = json.NewEncoder(w).Encode(resp)
			} else {
				// Return empty for other queries (e.g., in_progress check).
				resp := mockListResponse{Beads: nil, Total: 0}
				_ = json.NewEncoder(w).Encode(resp)
			}
			return
		}

		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))

	oldDaemon := daemon
	c, err := beadsapi.New(beadsapi.Config{HTTPAddr: srv.URL})
	if err != nil {
		t.Fatalf("failed to create test client: %v", err)
	}
	daemon = c

	return func() {
		daemon = oldDaemon
		srv.Close()
	}
}

func TestIsAutoAssignEnabled_DefaultEnabled(t *testing.T) {
	// No agent bead, no project bead — default is enabled.
	cleanup := setupTestDaemon(t, nil, nil)
	defer cleanup()

	t.Setenv("KD_AGENT_ID", "")

	got := isAutoAssignEnabled(context.Background(), "testproj")
	if !got {
		t.Error("expected auto_assign to be enabled by default")
	}
}

func TestIsAutoAssignEnabled_AgentExplicitFalse(t *testing.T) {
	// Agent bead has auto_assign="false" — disabled.
	cleanup := setupTestDaemon(t, &mockBead{
		ID:     "kd-agent1",
		Type:   "agent",
		Status: "open",
		Fields: map[string]string{"auto_assign": "false"},
	}, nil)
	defer cleanup()

	t.Setenv("KD_AGENT_ID", "kd-agent1")

	got := isAutoAssignEnabled(context.Background(), "testproj")
	if got {
		t.Error("expected auto_assign to be disabled when agent bead has auto_assign=false")
	}
}

func TestIsAutoAssignEnabled_AgentExplicitTrue(t *testing.T) {
	// Agent bead has auto_assign="true" — enabled.
	cleanup := setupTestDaemon(t, &mockBead{
		ID:     "kd-agent1",
		Type:   "agent",
		Status: "open",
		Fields: map[string]string{"auto_assign": "true"},
	}, nil)
	defer cleanup()

	t.Setenv("KD_AGENT_ID", "kd-agent1")

	got := isAutoAssignEnabled(context.Background(), "testproj")
	if !got {
		t.Error("expected auto_assign to be enabled when agent bead has auto_assign=true")
	}
}

func TestIsAutoAssignEnabled_AgentFieldAbsent_ProjectDisabled(t *testing.T) {
	// Agent bead exists but has no auto_assign field.
	// Project bead has auto_assign="false" — should inherit project setting.
	cleanup := setupTestDaemon(t, &mockBead{
		ID:     "kd-agent1",
		Type:   "agent",
		Status: "open",
		Fields: map[string]string{"project": "testproj"},
	}, []mockBead{
		{
			ID:     "kd-proj1",
			Title:  "testproj",
			Type:   "project",
			Kind:   "config",
			Status: "open",
			Fields: map[string]string{"auto_assign": "false"},
		},
	})
	defer cleanup()

	t.Setenv("KD_AGENT_ID", "kd-agent1")

	got := isAutoAssignEnabled(context.Background(), "testproj")
	if got {
		t.Error("expected auto_assign to be disabled when agent has no override and project is disabled")
	}
}

func TestIsAutoAssignEnabled_AgentOverridesProject(t *testing.T) {
	// Agent bead has auto_assign="true", project has auto_assign="false".
	// Agent should override project.
	cleanup := setupTestDaemon(t, &mockBead{
		ID:     "kd-agent1",
		Type:   "agent",
		Status: "open",
		Fields: map[string]string{"auto_assign": "true"},
	}, []mockBead{
		{
			ID:     "kd-proj1",
			Title:  "testproj",
			Type:   "project",
			Kind:   "config",
			Status: "open",
			Fields: map[string]string{"auto_assign": "false"},
		},
	})
	defer cleanup()

	t.Setenv("KD_AGENT_ID", "kd-agent1")

	got := isAutoAssignEnabled(context.Background(), "testproj")
	if !got {
		t.Error("expected agent auto_assign=true to override project auto_assign=false")
	}
}

func TestIsAutoAssignEnabled_AgentDisabledOverridesProjectEnabled(t *testing.T) {
	// Agent bead has auto_assign="false", project has auto_assign="true".
	// Agent should override project.
	cleanup := setupTestDaemon(t, &mockBead{
		ID:     "kd-agent1",
		Type:   "agent",
		Status: "open",
		Fields: map[string]string{"auto_assign": "false"},
	}, []mockBead{
		{
			ID:     "kd-proj1",
			Title:  "testproj",
			Type:   "project",
			Kind:   "config",
			Status: "open",
			Fields: map[string]string{"auto_assign": "true"},
		},
	})
	defer cleanup()

	t.Setenv("KD_AGENT_ID", "kd-agent1")

	got := isAutoAssignEnabled(context.Background(), "testproj")
	if got {
		t.Error("expected agent auto_assign=false to override project auto_assign=true")
	}
}

func TestIsAutoAssignEnabled_NoAgentID_ProjectDisabled(t *testing.T) {
	// No KD_AGENT_ID set, project bead has auto_assign="false".
	cleanup := setupTestDaemon(t, nil, []mockBead{
		{
			ID:     "kd-proj1",
			Title:  "testproj",
			Type:   "project",
			Kind:   "config",
			Status: "open",
			Fields: map[string]string{"auto_assign": "false"},
		},
	})
	defer cleanup()

	t.Setenv("KD_AGENT_ID", "")

	got := isAutoAssignEnabled(context.Background(), "testproj")
	if got {
		t.Error("expected auto_assign to be disabled when no agent ID and project is disabled")
	}
}

func TestIsAutoAssignEnabled_ProjectWithLegacyPrefix(t *testing.T) {
	// Project bead has "Project: " prefix in title.
	cleanup := setupTestDaemon(t, nil, []mockBead{
		{
			ID:     "kd-proj1",
			Title:  "Project: testproj",
			Type:   "project",
			Kind:   "config",
			Status: "open",
			Fields: map[string]string{"auto_assign": "false"},
		},
	})
	defer cleanup()

	t.Setenv("KD_AGENT_ID", "")

	got := isAutoAssignEnabled(context.Background(), "testproj")
	if got {
		t.Error("expected project with legacy 'Project: ' prefix to still be matched")
	}
}
