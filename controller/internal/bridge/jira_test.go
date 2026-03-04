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

// depRecord records a dependency wiring call.
type depRecord struct {
	BeadID      string
	DependsOnID string
	DepType     string
	CreatedBy   string
}

// mockJiraDaemon implements JiraBeadClient for testing.
type mockJiraDaemon struct {
	mu     sync.Mutex
	beads  map[string]*beadsapi.BeadDetail
	deps   []depRecord
	nextID int
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

func (m *mockJiraDaemon) AddDependency(_ context.Context, beadID, dependsOnID, depType, createdBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deps = append(m.deps, depRecord{
		BeadID:      beadID,
		DependsOnID: dependsOnID,
		DepType:     depType,
		CreatedBy:   createdBy,
	})
	return nil
}

func (m *mockJiraDaemon) UpdateBeadFields(_ context.Context, beadID string, fields map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bead, ok := m.beads[beadID]
	if !ok {
		return fmt.Errorf("bead %s not found", beadID)
	}
	for k, v := range fields {
		bead.Fields[k] = v
	}
	return nil
}

func (m *mockJiraDaemon) getDeps() []depRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]depRecord, len(m.deps))
	copy(out, m.deps)
	return out
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

func TestJiraClient_AddLabels(t *testing.T) {
	var (
		mu        sync.Mutex
		gotMethod string
		gotBody   map[string]any
	)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer jiraServer.Close()

	client := newTestJiraClient(jiraServer.URL)
	err := client.AddLabels(context.Background(), "PE-123", []string{"gasboat"})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != "PUT" {
		t.Errorf("expected PUT, got %s", gotMethod)
	}
	update, ok := gotBody["update"].(map[string]any)
	if !ok {
		t.Fatal("expected update field in body")
	}
	labels, ok := update["labels"].([]any)
	if !ok || len(labels) != 1 {
		t.Fatalf("expected 1 label update, got %v", update["labels"])
	}
	entry, ok := labels[0].(map[string]any)
	if !ok {
		t.Fatal("expected label entry to be map")
	}
	if entry["add"] != "gasboat" {
		t.Errorf("expected label add:gasboat, got %v", entry["add"])
	}
}

func TestJiraClient_AssignIssue(t *testing.T) {
	var (
		mu        sync.Mutex
		gotMethod string
		gotPath   string
		gotBody   map[string]any
	)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer jiraServer.Close()

	client := newTestJiraClient(jiraServer.URL)
	err := client.AssignIssue(context.Background(), "PE-456", "abc123")
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != "PUT" {
		t.Errorf("expected PUT, got %s", gotMethod)
	}
	if gotPath != "/rest/api/3/issue/PE-456/assignee" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotBody["accountId"] != "abc123" {
		t.Errorf("expected accountId abc123, got %v", gotBody["accountId"])
	}
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

func TestJiraPoller_AttachmentMetadata(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-8001", "id": "8001",
				"fields": map[string]any{
					"summary": "Bug with screenshots", "status": map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Bug"}, "priority": map[string]string{"name": "High"},
					"attachment": []map[string]any{
						{"id": "1", "filename": "screenshot.png", "mimeType": "image/png", "size": 12345, "content": "https://jira.example.com/att/1", "created": "2026-01-15T10:00:00.000+0000"},
						{"id": "2", "filename": "debug.log", "mimeType": "text/plain", "size": 5678, "content": "https://jira.example.com/att/2", "created": "2026-01-15T10:01:00.000+0000"},
						{"id": "3", "filename": "repro.mp4", "mimeType": "video/mp4", "size": 999999, "content": "https://jira.example.com/att/3", "created": "2026-01-15T10:02:00.000+0000"},
					},
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
		IssueTypes: []string{"Bug"},
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

	// Check attachment metadata fields.
	if bead.Fields["jira_attachment_count"] != "3" {
		t.Errorf("jira_attachment_count=%s, want 3", bead.Fields["jira_attachment_count"])
	}
	if bead.Fields["jira_has_images"] != "true" {
		t.Errorf("jira_has_images=%s, want true", bead.Fields["jira_has_images"])
	}
	if bead.Fields["jira_has_video"] != "true" {
		t.Errorf("jira_has_video=%s, want true", bead.Fields["jira_has_video"])
	}

	// Check jira-has-media label.
	hasMediaLabel := false
	for _, l := range bead.Labels {
		if l == "jira-has-media" {
			hasMediaLabel = true
		}
	}
	if !hasMediaLabel {
		t.Errorf("expected jira-has-media label, got %v", bead.Labels)
	}
}

func TestJiraPoller_AttachmentMetadata_NoMedia(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-8002", "id": "8002",
				"fields": map[string]any{
					"summary": "Bug with log only", "status": map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Bug"}, "priority": map[string]string{"name": "Medium"},
					"attachment": []map[string]any{
						{"id": "1", "filename": "debug.log", "mimeType": "text/plain", "size": 1024},
					},
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
		Projects: []string{"PE"}, Logger: slog.Default(),
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

	// Has attachment count but no media flags.
	if bead.Fields["jira_attachment_count"] != "1" {
		t.Errorf("jira_attachment_count=%s, want 1", bead.Fields["jira_attachment_count"])
	}
	if bead.Fields["jira_has_images"] != "" {
		t.Errorf("jira_has_images should be empty, got %s", bead.Fields["jira_has_images"])
	}
	// jira-has-media label is always added when attachments exist (JIRA
	// search/jql often omits mimeType from stubs, so we tag conservatively).
	hasMediaLabel := false
	for _, l := range bead.Labels {
		if l == "jira-has-media" {
			hasMediaLabel = true
		}
	}
	if !hasMediaLabel {
		t.Errorf("expected jira-has-media label (always set when attachments exist), got %v", bead.Labels)
	}
}

func TestJiraPoller_EpicImport(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-9000", "id": "9000",
				"fields": map[string]any{
					"summary":   "Upload Epic",
					"status":    map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Epic"},
					"priority":  map[string]string{"name": "High"},
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
		Projects: []string{"PE"}, IssueTypes: []string{"Epic"}, Logger: slog.Default(),
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
	hasEpicLabel := false
	for _, l := range bead.Labels {
		if l == "jira-epic" {
			hasEpicLabel = true
		}
	}
	if !hasEpicLabel {
		t.Errorf("expected jira-epic label, got %v", bead.Labels)
	}
}

func TestJiraPoller_ChildOfDep(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{
				{
					"key": "PE-9000", "id": "9000",
					"fields": map[string]any{
						"summary":   "Upload Epic",
						"status":    map[string]string{"name": "To Do"},
						"issuetype": map[string]string{"name": "Epic"},
						"priority":  map[string]string{"name": "High"},
					},
				},
				{
					"key": "PE-9001", "id": "9001",
					"fields": map[string]any{
						"summary":   "Upload Task",
						"status":    map[string]string{"name": "To Do"},
						"issuetype": map[string]string{"name": "Task"},
						"priority":  map[string]string{"name": "Medium"},
						"parent":    map[string]any{"key": "PE-9000", "fields": map[string]string{"summary": "Upload Epic"}},
					},
				},
			},
			"total": 2,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects: []string{"PE"}, Logger: slog.Default(),
	})
	poller.poll(context.Background())

	deps := daemon.getDeps()
	if len(deps) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(deps))
	}
	if deps[0].DepType != "child-of" {
		t.Errorf("expected child-of, got %s", deps[0].DepType)
	}
	if deps[0].CreatedBy != "jira-bridge" {
		t.Errorf("expected created_by=jira-bridge, got %s", deps[0].CreatedBy)
	}
}

func TestJiraPoller_IssueLinks(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{
				{
					"key": "PE-100", "id": "100",
					"fields": map[string]any{
						"summary":   "Blocker",
						"status":    map[string]string{"name": "To Do"},
						"issuetype": map[string]string{"name": "Bug"},
						"priority":  map[string]string{"name": "High"},
						"issuelinks": []map[string]any{{
							"type":         map[string]string{"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
							"outwardIssue": map[string]string{"key": "PE-101"},
						}},
					},
				},
				{
					"key": "PE-101", "id": "101",
					"fields": map[string]any{
						"summary":   "Blocked",
						"status":    map[string]string{"name": "To Do"},
						"issuetype": map[string]string{"name": "Bug"},
						"priority":  map[string]string{"name": "Medium"},
					},
				},
			},
			"total": 2,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects: []string{"PE"}, Logger: slog.Default(),
	})
	poller.poll(context.Background())

	deps := daemon.getDeps()
	if len(deps) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(deps))
	}
	if deps[0].DepType != "blocks" {
		t.Errorf("expected blocks, got %s", deps[0].DepType)
	}

	// Verify jira_link_count was stored on the bead with links.
	beads := daemon.getBeads()
	for _, b := range beads {
		if b.Fields["jira_key"] == "PE-100" {
			if b.Fields["jira_link_count"] != "1" {
				t.Errorf("jira_link_count=%s, want 1", b.Fields["jira_link_count"])
			}
		}
	}
}

func TestJiraPoller_IssueLinks_TargetMissing(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-200", "id": "200",
				"fields": map[string]any{
					"summary":   "Has link to missing",
					"status":    map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Task"},
					"priority":  map[string]string{"name": "Medium"},
					"issuelinks": []map[string]any{{
						"type":         map[string]string{"name": "Relates", "inward": "relates to", "outward": "relates to"},
						"outwardIssue": map[string]string{"key": "PE-999"},
					}},
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
		Projects: []string{"PE"}, Logger: slog.Default(),
	})
	poller.poll(context.Background())

	// No error, no dependency wired (target PE-999 not imported).
	deps := daemon.getDeps()
	if len(deps) != 0 {
		t.Errorf("expected 0 dependencies for missing target, got %d", len(deps))
	}

	// Cross-project link stored as field.
	beads := daemon.getBeads()
	var bead *beadsapi.BeadDetail
	for _, b := range beads {
		bead = b
	}
	if bead.Fields["jira_xlinks"] != "relates:PE-999" {
		t.Errorf("jira_xlinks=%q, want %q", bead.Fields["jira_xlinks"], "relates:PE-999")
	}
}

func TestJiraPoller_NoAttachments(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-400", "id": "400",
				"fields": map[string]any{
					"summary":   "No attachments",
					"status":    map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Task"},
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
		Projects: []string{"PE"}, Logger: slog.Default(),
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
	if _, ok := bead.Fields["jira_attachment_count"]; ok {
		t.Errorf("expected no jira_attachment_count field, got %s", bead.Fields["jira_attachment_count"])
	}
	if _, ok := bead.Fields["jira_has_images"]; ok {
		t.Errorf("expected no jira_has_images field")
	}
	for _, l := range bead.Labels {
		if l == "jira-has-media" {
			t.Errorf("unexpected jira-has-media label")
		}
	}
}

func TestMapJiraLinkType(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"Blocks", "blocks"},
		{"blocks", "blocks"},
		{"Relates", "relates"},
		{"Duplicate", "duplicate"},
		{"Action Item", "action-item"},
		{"Escalate", "escalate"},
		{"Cloners", "clones"},
		{"Unknown Type", "jira-link"},
		{"", "jira-link"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapJiraLinkType(tt.name); got != tt.expected {
				t.Errorf("MapJiraLinkType(%q) = %q, want %q", tt.name, got, tt.expected)
			}
		})
	}
}

