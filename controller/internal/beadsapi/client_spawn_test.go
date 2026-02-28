package beadsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- SpawnAgent tests ---

func TestSpawnAgent_SendsCorrectRequest(t *testing.T) {
	type request struct {
		method string
		path   string
		body   map[string]json.RawMessage
	}
	var requests []request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]json.RawMessage
		_ = json.Unmarshal(body, &parsed)
		requests = append(requests, request{r.Method, r.URL.Path, parsed})
		if r.URL.Path == "/v1/beads" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "bd-agent-42"})
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	id, err := c.SpawnAgent(context.Background(), "my-bot", "gasboat", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if id != "bd-agent-42" {
		t.Errorf("expected id bd-agent-42, got %s", id)
	}

	// Expect three requests: POST /v1/beads, POST /v1/beads/{id}/labels (project),
	// POST /v1/beads/{id}/labels (role).
	if len(requests) != 3 {
		t.Fatalf("expected 3 HTTP requests, got %d", len(requests))
	}

	createReq := requests[0]
	if createReq.method != http.MethodPost {
		t.Errorf("expected POST, got %s", createReq.method)
	}
	if createReq.path != "/v1/beads" {
		t.Errorf("expected /v1/beads, got %s", createReq.path)
	}

	var beadType, beadTitle string
	_ = json.Unmarshal(createReq.body["type"], &beadType)
	_ = json.Unmarshal(createReq.body["title"], &beadTitle)
	if beadType != "agent" {
		t.Errorf("expected type=agent, got %s", beadType)
	}
	if beadTitle != "my-bot" {
		t.Errorf("expected title=my-bot, got %s", beadTitle)
	}

	var fields map[string]string
	_ = json.Unmarshal(createReq.body["fields"], &fields)
	if fields["agent"] != "my-bot" {
		t.Errorf("expected fields.agent=my-bot, got %s", fields["agent"])
	}
	if fields["project"] != "gasboat" {
		t.Errorf("expected fields.project=gasboat, got %s", fields["project"])
	}
	if fields["mode"] != "crew" {
		t.Errorf("expected fields.mode=crew, got %s", fields["mode"])
	}
	if fields["role"] != "crew" {
		t.Errorf("expected fields.role=crew (default), got %s", fields["role"])
	}

	// Second request: add project label.
	projectLabelReq := requests[1]
	if projectLabelReq.path != "/v1/beads/bd-agent-42/labels" {
		t.Errorf("expected label path /v1/beads/bd-agent-42/labels, got %s", projectLabelReq.path)
	}
	var projectLabel string
	_ = json.Unmarshal(projectLabelReq.body["label"], &projectLabel)
	if projectLabel != "project:gasboat" {
		t.Errorf("expected label=project:gasboat, got %s", projectLabel)
	}

	// Third request: add role label.
	roleLabelReq := requests[2]
	if roleLabelReq.path != "/v1/beads/bd-agent-42/labels" {
		t.Errorf("expected label path /v1/beads/bd-agent-42/labels, got %s", roleLabelReq.path)
	}
	var roleLabel string
	_ = json.Unmarshal(roleLabelReq.body["label"], &roleLabel)
	if roleLabel != "role:crew" {
		t.Errorf("expected label=role:crew, got %s", roleLabel)
	}
}

func TestSpawnAgent_CustomRole(t *testing.T) {
	type request struct {
		method string
		path   string
		body   map[string]json.RawMessage
	}
	var requests []request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]json.RawMessage
		_ = json.Unmarshal(body, &parsed)
		requests = append(requests, request{r.Method, r.URL.Path, parsed})
		if r.URL.Path == "/v1/beads" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "bd-agent-77"})
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.SpawnAgent(context.Background(), "my-bot", "gasboat", "", "captain", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(requests) < 1 {
		t.Fatal("expected at least one request")
	}
	var fields map[string]string
	_ = json.Unmarshal(requests[0].body["fields"], &fields)
	if fields["role"] != "captain" {
		t.Errorf("expected fields.role=captain, got %s", fields["role"])
	}

	// Verify role label is sent.
	foundRoleLabel := false
	for _, req := range requests[1:] {
		var label string
		_ = json.Unmarshal(req.body["label"], &label)
		if label == "role:captain" {
			foundRoleLabel = true
		}
	}
	if !foundRoleLabel {
		t.Errorf("expected role:captain label to be added")
	}
}

func TestSpawnAgent_PropagatesCreateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.SpawnAgent(context.Background(), "bad-bot", "gasboat", "", "", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSpawnAgent_WithTaskID_SetsDescriptionAndLinksDependency(t *testing.T) {
	type request struct {
		method string
		path   string
		body   map[string]json.RawMessage
	}
	var requests []request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]json.RawMessage
		_ = json.Unmarshal(body, &parsed)
		requests = append(requests, request{r.Method, r.URL.Path, parsed})

		if r.URL.Path == "/v1/beads" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "bd-agent-99"})
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	id, err := c.SpawnAgent(context.Background(), "my-bot", "gasboat", "kd-task-123", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "bd-agent-99" {
		t.Errorf("expected id bd-agent-99, got %s", id)
	}

	// Expect four requests: POST /v1/beads, POST /v1/beads/{id}/labels (project),
	// POST /v1/beads/{id}/labels (role), POST /v1/beads/{id}/dependencies.
	if len(requests) != 4 {
		t.Fatalf("expected 4 HTTP requests, got %d", len(requests))
	}

	// First request: create agent bead with description.
	createReq := requests[0]
	if createReq.path != "/v1/beads" {
		t.Errorf("expected path /v1/beads, got %s", createReq.path)
	}
	var desc string
	_ = json.Unmarshal(createReq.body["description"], &desc)
	if desc != "Assigned to task: kd-task-123" {
		t.Errorf("expected description %q, got %q", "Assigned to task: kd-task-123", desc)
	}

	// Second request: add project label.
	labelReq := requests[1]
	if labelReq.path != "/v1/beads/bd-agent-99/labels" {
		t.Errorf("expected label path /v1/beads/bd-agent-99/labels, got %s", labelReq.path)
	}
	var label string
	_ = json.Unmarshal(labelReq.body["label"], &label)
	if label != "project:gasboat" {
		t.Errorf("expected label=project:gasboat, got %s", label)
	}

	// Third request: add role label.
	roleLabelReq := requests[2]
	if roleLabelReq.path != "/v1/beads/bd-agent-99/labels" {
		t.Errorf("expected label path /v1/beads/bd-agent-99/labels, got %s", roleLabelReq.path)
	}
	var roleLabel string
	_ = json.Unmarshal(roleLabelReq.body["label"], &roleLabel)
	if roleLabel != "role:crew" {
		t.Errorf("expected label=role:crew, got %s", roleLabel)
	}

	// Fourth request: add dependency to the task bead.
	depReq := requests[3]
	if depReq.path != "/v1/beads/bd-agent-99/dependencies" {
		t.Errorf("expected dep path /v1/beads/bd-agent-99/dependencies, got %s", depReq.path)
	}
	var dependsOn, depType string
	_ = json.Unmarshal(depReq.body["depends_on_id"], &dependsOn)
	_ = json.Unmarshal(depReq.body["type"], &depType)
	if dependsOn != "kd-task-123" {
		t.Errorf("expected depends_on_id=kd-task-123, got %s", dependsOn)
	}
	if depType != "assigned" {
		t.Errorf("expected dep type=assigned, got %s", depType)
	}
}
