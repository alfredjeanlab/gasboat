package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// mockJiraDaemon implements JiraBeadClient for testing.
type mockJiraDaemon struct {
	mu     sync.Mutex
	beads  map[string]*beadsapi.BeadDetail
	deps   []mockDep // recorded dependency additions
	nextID int
}

type mockDep struct {
	BeadID      string
	DependsOnID string
	Type        string
	CreatedBy   string
}

func newMockJiraDaemon() *mockJiraDaemon {
	return &mockJiraDaemon{
		beads: make(map[string]*beadsapi.BeadDetail),
	}
}

func (m *mockJiraDaemon) CreateBead(_ context.Context, req beadsapi.CreateBeadRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprintf("bd-task-%d", m.nextID)
	fields := beadsapi.ParseFieldsJSON(req.Fields)
	fields["_priority"] = fmt.Sprintf("%d", req.Priority)
	m.beads[id] = &beadsapi.BeadDetail{
		ID:          id,
		Title:       req.Title,
		Type:        req.Type,
		Labels:      req.Labels,
		Description: req.Description,
		CreatedBy:   req.CreatedBy,
		Fields:      fields,
	}
	return id, nil
}

func (m *mockJiraDaemon) AddDependency(_ context.Context, beadID, dependsOnID, depType, createdBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deps = append(m.deps, mockDep{BeadID: beadID, DependsOnID: dependsOnID, Type: depType, CreatedBy: createdBy})
	return nil
}

func (m *mockJiraDaemon) ListTaskBeads(_ context.Context) ([]*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*beadsapi.BeadDetail
	for _, b := range m.beads {
		if b.Type == "task" {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockJiraDaemon) getBeads() map[string]*beadsapi.BeadDetail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*beadsapi.BeadDetail, len(m.beads))
	for k, v := range m.beads {
		out[k] = v
	}
	return out
}

// newTestJiraClient creates a JiraClient pointing at a test server.
func newTestJiraClient(url string) *JiraClient {
	return NewJiraClient(JiraClientConfig{
		BaseURL: url, Email: "test@example.com", APIToken: "tok", Logger: slog.Default(),
	})
}

func TestJiraPoller_CreateBead(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-7001", "id": "10001",
				"fields": map[string]any{
					"summary": "Error alert after uploading file",
					"description": map[string]any{"version": 1, "type": "doc", "content": []any{
						map[string]any{"type": "paragraph", "content": []any{
							map[string]any{"type": "text", "text": "Steps to reproduce the error."},
						}},
					}},
					"status": map[string]string{"name": "To Do"}, "issuetype": map[string]string{"name": "Bug"},
					"priority": map[string]string{"name": "High"},
					"reporter": map[string]string{"displayName": "Jane Doe", "accountId": "abc123"},
					"labels":   []string{"frontend", "urgent"},
					"parent":   map[string]any{"key": "PE-5000", "fields": map[string]string{"summary": "Upload Epic"}},
				},
			}},
			"total": 1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects:   []string{"PE"},
		Statuses:   []string{"To Do"},
		IssueTypes: []string{"Bug"},
		ProjectMap: map[string]string{"PE": "monorepo"},
		Logger:     slog.Default(),
	})
	poller.poll(context.Background())

	beads := daemon.getBeads()
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(beads))
	}
	var bead *beadsapi.BeadDetail
	for _, b := range beads {
		bead = b
	}
	if bead.Title != "[PE-7001] Error alert after uploading file" {
		t.Errorf("unexpected title: %s", bead.Title)
	}
	if bead.Type != "task" {
		t.Errorf("expected type=task, got %s", bead.Type)
	}
	// project:monorepo because ProjectMap maps PE → monorepo
	want := map[string]bool{"source:jira": true, "jira:PE-7001": true, "project:monorepo": true, "jira-label:frontend": true, "jira-label:urgent": true}
	for _, l := range bead.Labels {
		delete(want, l)
	}
	if len(want) > 0 {
		t.Errorf("missing labels: %v", want)
	}
	if bead.Fields["jira_key"] != "PE-7001" {
		t.Errorf("jira_key=%s", bead.Fields["jira_key"])
	}
	if bead.Fields["jira_type"] != "Bug" {
		t.Errorf("jira_type=%s", bead.Fields["jira_type"])
	}
	if bead.Fields["jira_epic"] != "PE-5000" {
		t.Errorf("jira_epic=%s", bead.Fields["jira_epic"])
	}
	if bead.Fields["_priority"] != "1" {
		t.Errorf("priority=%s, want 1", bead.Fields["_priority"])
	}
	if bead.CreatedBy != "jira-bridge" {
		t.Errorf("created_by=%s", bead.CreatedBy)
	}
	if bead.Description != "Steps to reproduce the error." {
		t.Errorf("description=%q", bead.Description)
	}
}

func TestJiraPoller_CreateBead_FallbackProject(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "DEVOPS-42", "id": "42",
				"fields": map[string]any{
					"summary": "CI pipeline fix", "status": map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Task"}, "priority": map[string]string{"name": "Medium"},
				},
			}},
			"total": 1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	// No ProjectMap entry for DEVOPS — falls back to "devops" (lowercased prefix).
	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects: []string{"DEVOPS"}, Logger: slog.Default(),
	})
	poller.poll(context.Background())

	beads := daemon.getBeads()
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(beads))
	}
	var bead *beadsapi.BeadDetail
	for _, b := range beads {
		bead = b
	}
	hasProjectLabel := false
	for _, l := range bead.Labels {
		if l == "project:devops" {
			hasProjectLabel = true
		}
	}
	if !hasProjectLabel {
		t.Errorf("expected fallback label project:devops, got %v", bead.Labels)
	}
}

func TestJiraPoller_Dedup(t *testing.T) {
	callCount := 0
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		callCount++
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-100", "id": "100",
				"fields": map[string]any{
					"summary": "Dup test", "status": map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Task"}, "priority": map[string]string{"name": "Medium"},
				},
			}},
			"total": 1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects: []string{"PE"}, Statuses: []string{"To Do"}, IssueTypes: []string{"Task"}, Logger: slog.Default(),
	})
	poller.poll(context.Background())
	poller.poll(context.Background())

	if len(daemon.getBeads()) != 1 {
		t.Fatalf("expected 1 bead after 2 polls (dedup), got %d", len(daemon.getBeads()))
	}
	if callCount != 2 {
		t.Errorf("expected 2 JIRA API calls, got %d", callCount)
	}
}

func TestJiraPoller_CatchUp(t *testing.T) {
	daemon := newMockJiraDaemon()
	daemon.mu.Lock()
	daemon.beads["existing-1"] = &beadsapi.BeadDetail{
		ID: "existing-1", Type: "task",
		// Labels is nil — the list API does not populate labels from the
		// separate labels table.  CatchUp must rely on jira_key field only.
		Labels: nil,
		Fields: map[string]string{"jira_key": "PE-500"},
	}
	daemon.beads["non-jira"] = &beadsapi.BeadDetail{
		ID: "non-jira", Type: "task", Labels: nil, Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	poller := NewJiraPoller(nil, daemon, JiraPollerConfig{Logger: slog.Default()})
	poller.CatchUp(context.Background())

	if !poller.IsTracked("PE-500") {
		t.Error("expected PE-500 to be tracked")
	}
	if poller.IsTracked("non-jira") {
		t.Error("non-JIRA bead should not be tracked")
	}
	if poller.TrackedCount() != 1 {
		t.Errorf("expected 1 tracked, got %d", poller.TrackedCount())
	}
}

func TestMapJiraPriority(t *testing.T) {
	tests := []struct {
		name     string
		expected int
	}{
		{"Highest", 0}, {"Critical", 0}, {"Blocker", 0}, {"High", 1},
		{"Medium", 2}, {"Low", 3}, {"Lowest", 3}, {"Trivial", 3}, {"unknown", 2}, {"", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapJiraPriority(tt.name); got != tt.expected {
				t.Errorf("MapJiraPriority(%q) = %d, want %d", tt.name, got, tt.expected)
			}
		})
	}
}

func TestJiraPoller_EpicImport(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-5000", "id": "5000",
				"fields": map[string]any{
					"summary":   "Upload Epic",
					"status":    map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Epic"},
					"priority":  map[string]string{"name": "Medium"},
				},
			}},
			"total": 1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects:   []string{"PE"},
		IssueTypes: []string{"Epic"},
		ProjectMap: map[string]string{"PE": "monorepo"},
		Logger:     slog.Default(),
	})
	poller.poll(context.Background())

	beads := daemon.getBeads()
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(beads))
	}
	var bead *beadsapi.BeadDetail
	for _, b := range beads {
		bead = b
	}
	// Epics get jira-epic label.
	hasEpicLabel := false
	for _, l := range bead.Labels {
		if l == "jira-epic" {
			hasEpicLabel = true
		}
	}
	if !hasEpicLabel {
		t.Errorf("expected jira-epic label, got %v", bead.Labels)
	}
	// Epics get priority=1 regardless of JIRA priority.
	if bead.Fields["_priority"] != "1" {
		t.Errorf("priority=%s, want 1 for epic", bead.Fields["_priority"])
	}
	if bead.Fields["jira_type"] != "Epic" {
		t.Errorf("jira_type=%s, want Epic", bead.Fields["jira_type"])
	}
}

func TestJiraPoller_ChildOfDependency(t *testing.T) {
	callCount := 0
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var issues []map[string]any
		if callCount == 1 {
			// First poll: return the epic.
			issues = []map[string]any{{
				"key": "PE-5000", "id": "5000",
				"fields": map[string]any{
					"summary": "Upload Epic", "status": map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Epic"}, "priority": map[string]string{"name": "Medium"},
				},
			}}
		} else {
			// Second poll: return a child issue referencing the epic.
			issues = []map[string]any{{
				"key": "PE-7001", "id": "7001",
				"fields": map[string]any{
					"summary": "Child task", "status": map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Task"}, "priority": map[string]string{"name": "Medium"},
					"parent":    map[string]any{"key": "PE-5000"},
				},
			}}
		}
		resp := map[string]any{"issues": issues, "total": len(issues)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects:   []string{"PE"},
		IssueTypes: []string{"Epic", "Task"},
		Logger:     slog.Default(),
	})

	// First poll creates the epic bead.
	poller.poll(context.Background())
	// Second poll creates the child bead and links it.
	poller.poll(context.Background())

	beads := daemon.getBeads()
	if len(beads) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(beads))
	}

	daemon.mu.Lock()
	deps := daemon.deps
	daemon.mu.Unlock()

	if len(deps) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(deps))
	}
	dep := deps[0]
	if dep.Type != "child-of" {
		t.Errorf("dep type=%s, want child-of", dep.Type)
	}
	if dep.CreatedBy != "jira-bridge" {
		t.Errorf("dep created_by=%s, want jira-bridge", dep.CreatedBy)
	}

	// The child bead should depend on the epic bead.
	var epicBeadID string
	for _, b := range beads {
		if b.Fields["jira_key"] == "PE-5000" {
			epicBeadID = b.ID
		}
	}
	if dep.DependsOnID != epicBeadID {
		t.Errorf("dep depends_on=%s, want epic bead %s", dep.DependsOnID, epicBeadID)
	}
}

func TestJiraKeyFromBead(t *testing.T) {
	tests := []struct {
		name     string
		bead     BeadEvent
		expected string
	}{
		{"from fields", BeadEvent{Fields: map[string]string{"jira_key": "PE-123"}}, "PE-123"},
		{"from labels", BeadEvent{Labels: []string{"source:jira", "jira:DEVOPS-42"}, Fields: map[string]string{}}, "DEVOPS-42"},
		{"not jira", BeadEvent{Labels: []string{"source:manual"}, Fields: map[string]string{}}, ""},
		{"jira-label no match", BeadEvent{Labels: []string{"jira-label:frontend"}, Fields: map[string]string{}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jiraKeyFromBead(tt.bead); got != tt.expected {
				t.Errorf("jiraKeyFromBead() = %q, want %q", got, tt.expected)
			}
		})
	}
}
