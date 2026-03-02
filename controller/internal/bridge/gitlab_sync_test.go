package bridge

import (
	"bytes"
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

// mockGitLabDaemon implements GitLabBeadClient for testing.
type mockGitLabDaemon struct {
	mu     sync.Mutex
	beads  map[string]*beadsapi.BeadDetail
}

func newMockGitLabDaemon() *mockGitLabDaemon {
	return &mockGitLabDaemon{beads: make(map[string]*beadsapi.BeadDetail)}
}

func (m *mockGitLabDaemon) ListTaskBeads(_ context.Context) ([]*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*beadsapi.BeadDetail
	for _, b := range m.beads {
		result = append(result, b)
	}
	return result, nil
}

func (m *mockGitLabDaemon) UpdateBeadFields(_ context.Context, beadID string, fields map[string]string) error {
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

func (m *mockGitLabDaemon) getBead(id string) *beadsapi.BeadDetail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.beads[id]
}

func TestGitLabWebhookHandler_MergeEvent(t *testing.T) {
	daemon := newMockGitLabDaemon()
	daemon.beads["bead-1"] = &beadsapi.BeadDetail{
		ID:     "bead-1",
		Title:  "Fix auth",
		Type:   "task",
		Fields: map[string]string{"mr_url": "https://gitlab.com/org/repo/-/merge_requests/42"},
	}

	handler := GitLabWebhookHandler(nil, daemon, "test-secret", slog.Default())

	event := map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"iid":               42,
			"state":             "merged",
			"action":            "merge",
			"url":               "https://gitlab.com/org/repo/-/merge_requests/42",
			"target_project_id": 99,
		},
	}
	body, _ := json.Marshal(event)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	bead := daemon.getBead("bead-1")
	if bead.Fields["mr_merged"] != "true" {
		t.Errorf("mr_merged=%s, want true", bead.Fields["mr_merged"])
	}
	if bead.Fields["mr_state"] != "merged" {
		t.Errorf("mr_state=%s, want merged", bead.Fields["mr_state"])
	}
	if bead.Fields["gitlab_mr_iid"] != "42" {
		t.Errorf("gitlab_mr_iid=%s, want 42", bead.Fields["gitlab_mr_iid"])
	}
	if bead.Fields["gitlab_project_id"] != "99" {
		t.Errorf("gitlab_project_id=%s, want 99", bead.Fields["gitlab_project_id"])
	}
}

func TestGitLabWebhookHandler_InvalidSecret(t *testing.T) {
	handler := GitLabWebhookHandler(nil, newMockGitLabDaemon(), "real-secret", slog.Default())

	req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	req.Header.Set("X-Gitlab-Token", "wrong-secret")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestGitLabWebhookHandler_IgnoreNonMerge(t *testing.T) {
	handler := GitLabWebhookHandler(nil, newMockGitLabDaemon(), "secret", slog.Default())

	event := map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "open",
			"url":    "https://gitlab.com/org/repo/-/merge_requests/42",
		},
	}
	body, _ := json.Marshal(event)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "secret")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// Should be ignored, no bead updates.
}

func TestGitLabWebhookHandler_NoMatchingBead(t *testing.T) {
	daemon := newMockGitLabDaemon()
	// No beads with matching mr_url.
	daemon.beads["bead-1"] = &beadsapi.BeadDetail{
		ID:     "bead-1",
		Type:   "task",
		Fields: map[string]string{"mr_url": "https://gitlab.com/other/repo/-/merge_requests/99"},
	}

	handler := GitLabWebhookHandler(nil, daemon, "secret", slog.Default())

	event := map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "merge",
			"url":    "https://gitlab.com/org/repo/-/merge_requests/42",
			"iid":    42,
		},
	}
	body, _ := json.Marshal(event)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "secret")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Bead should not be updated.
	bead := daemon.getBead("bead-1")
	if bead.Fields["mr_merged"] != "" {
		t.Errorf("expected mr_merged empty, got %s", bead.Fields["mr_merged"])
	}
}

func TestGitLabWebhookHandler_AlreadyMerged(t *testing.T) {
	daemon := newMockGitLabDaemon()
	daemon.beads["bead-1"] = &beadsapi.BeadDetail{
		ID:     "bead-1",
		Type:   "task",
		Fields: map[string]string{
			"mr_url":    "https://gitlab.com/org/repo/-/merge_requests/42",
			"mr_merged": "true",
		},
	}

	handler := GitLabWebhookHandler(nil, daemon, "secret", slog.Default())

	event := map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"action": "merge",
			"url":    "https://gitlab.com/org/repo/-/merge_requests/42",
			"iid":    42,
		},
	}
	body, _ := json.Marshal(event)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "secret")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// Should be a no-op since already merged.
}
