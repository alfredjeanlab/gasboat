package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// commentRecord tracks a comment added to a bead.
type commentRecord struct {
	BeadID string
	Author string
	Text   string
}

// mockGitLabDaemon implements GitLabBeadClient for testing.
type mockGitLabDaemon struct {
	mu       sync.Mutex
	beads    map[string]*beadsapi.BeadDetail
	comments []commentRecord
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

func (m *mockGitLabDaemon) AddComment(_ context.Context, beadID, author, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.beads[beadID]; !ok {
		return fmt.Errorf("bead %s not found", beadID)
	}
	m.comments = append(m.comments, commentRecord{BeadID: beadID, Author: author, Text: text})
	return nil
}

func (m *mockGitLabDaemon) getBead(id string) *beadsapi.BeadDetail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.beads[id]
}

func (m *mockGitLabDaemon) getComments() []commentRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]commentRecord, len(m.comments))
	copy(result, m.comments)
	return result
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

func TestGitLabWebhookHandler_PipelineEvent(t *testing.T) {
	daemon := newMockGitLabDaemon()
	daemon.beads["bead-1"] = &beadsapi.BeadDetail{
		ID:     "bead-1",
		Title:  "Fix auth",
		Type:   "task",
		Fields: map[string]string{"mr_url": "https://gitlab.com/org/repo/-/merge_requests/42"},
	}

	handler := GitLabWebhookHandler(nil, daemon, "secret", slog.Default())

	event := map[string]any{
		"object_kind": "pipeline",
		"object_attributes": map[string]any{
			"id":     123,
			"status": "failed",
			"url":    "https://gitlab.com/org/repo/-/pipelines/123",
		},
		"merge_request": map[string]any{
			"iid": 42,
			"url": "https://gitlab.com/org/repo/-/merge_requests/42",
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

	bead := daemon.getBead("bead-1")
	if bead.Fields["mr_pipeline_status"] != "failed" {
		t.Errorf("mr_pipeline_status=%s, want failed", bead.Fields["mr_pipeline_status"])
	}
	if bead.Fields["mr_pipeline_url"] != "https://gitlab.com/org/repo/-/pipelines/123" {
		t.Errorf("mr_pipeline_url=%s, want pipeline URL", bead.Fields["mr_pipeline_url"])
	}
}

func TestGitLabWebhookHandler_ApprovalEvent(t *testing.T) {
	daemon := newMockGitLabDaemon()
	daemon.beads["bead-1"] = &beadsapi.BeadDetail{
		ID:     "bead-1",
		Title:  "Fix auth",
		Type:   "task",
		Fields: map[string]string{"mr_url": "https://gitlab.com/org/repo/-/merge_requests/42"},
	}

	handler := GitLabWebhookHandler(nil, daemon, "secret", slog.Default())

	event := map[string]any{
		"object_kind": "merge_request",
		"user":        map[string]any{"username": "alice"},
		"object_attributes": map[string]any{
			"iid":    42,
			"action": "approved",
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

	bead := daemon.getBead("bead-1")
	if bead.Fields["mr_approved"] != "true" {
		t.Errorf("mr_approved=%s, want true", bead.Fields["mr_approved"])
	}
	if bead.Fields["mr_approvers"] != "alice" {
		t.Errorf("mr_approvers=%s, want alice", bead.Fields["mr_approvers"])
	}

	// Second approval by bob.
	event2 := map[string]any{
		"object_kind": "merge_request",
		"user":        map[string]any{"username": "bob"},
		"object_attributes": map[string]any{
			"iid":    42,
			"action": "approved",
			"url":    "https://gitlab.com/org/repo/-/merge_requests/42",
		},
	}
	body2, _ := json.Marshal(event2)
	req2 := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body2))
	req2.Header.Set("X-Gitlab-Token", "secret")
	w2 := httptest.NewRecorder()

	handler.ServeHTTP(w2, req2)

	bead = daemon.getBead("bead-1")
	if bead.Fields["mr_approvers"] != "alice,bob" {
		t.Errorf("mr_approvers=%s, want alice,bob", bead.Fields["mr_approvers"])
	}

	// Unapproval by alice.
	event3 := map[string]any{
		"object_kind": "merge_request",
		"user":        map[string]any{"username": "alice"},
		"object_attributes": map[string]any{
			"iid":    42,
			"action": "unapproved",
			"url":    "https://gitlab.com/org/repo/-/merge_requests/42",
		},
	}
	body3, _ := json.Marshal(event3)
	req3 := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body3))
	req3.Header.Set("X-Gitlab-Token", "secret")
	w3 := httptest.NewRecorder()

	handler.ServeHTTP(w3, req3)

	bead = daemon.getBead("bead-1")
	if bead.Fields["mr_approved"] != "false" {
		t.Errorf("mr_approved=%s, want false", bead.Fields["mr_approved"])
	}
	if bead.Fields["mr_approvers"] != "bob" {
		t.Errorf("mr_approvers=%s, want bob", bead.Fields["mr_approvers"])
	}
}

func TestGitLabWebhookHandler_NoteEvent(t *testing.T) {
	daemon := newMockGitLabDaemon()
	daemon.beads["bead-1"] = &beadsapi.BeadDetail{
		ID:     "bead-1",
		Title:  "Fix auth",
		Type:   "task",
		Fields: map[string]string{"mr_url": "https://gitlab.com/org/repo/-/merge_requests/42"},
	}

	handler := GitLabWebhookHandler(nil, daemon, "secret", slog.Default())

	event := map[string]any{
		"object_kind": "note",
		"user":        map[string]any{"username": "reviewer-alice"},
		"object_attributes": map[string]any{
			"note":          "This function needs error handling",
			"noteable_type": "MergeRequest",
			"system":        false,
			"position": map[string]any{
				"new_path": "pkg/auth/handler.go",
				"new_line": 42,
			},
		},
		"merge_request": map[string]any{
			"iid": 42,
			"url": "https://gitlab.com/org/repo/-/merge_requests/42",
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

	bead := daemon.getBead("bead-1")
	if bead.Fields["mr_has_review_comments"] != "true" {
		t.Errorf("mr_has_review_comments=%s, want true", bead.Fields["mr_has_review_comments"])
	}

	comments := daemon.getComments()
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "gitlab-bridge" {
		t.Errorf("comment author=%s, want gitlab-bridge", comments[0].Author)
	}
	if !strings.Contains(comments[0].Text, "reviewer-alice") {
		t.Errorf("comment should mention reviewer, got: %s", comments[0].Text)
	}
	if !strings.Contains(comments[0].Text, "pkg/auth/handler.go:42") {
		t.Errorf("comment should contain file:line, got: %s", comments[0].Text)
	}
	if !strings.Contains(comments[0].Text, "This function needs error handling") {
		t.Errorf("comment should contain note text, got: %s", comments[0].Text)
	}
}

func TestGitLabWebhookHandler_NoteEvent_SystemNote(t *testing.T) {
	daemon := newMockGitLabDaemon()
	daemon.beads["bead-1"] = &beadsapi.BeadDetail{
		ID:     "bead-1",
		Type:   "task",
		Fields: map[string]string{"mr_url": "https://gitlab.com/org/repo/-/merge_requests/42"},
	}

	handler := GitLabWebhookHandler(nil, daemon, "secret", slog.Default())

	event := map[string]any{
		"object_kind": "note",
		"user":        map[string]any{"username": "system"},
		"object_attributes": map[string]any{
			"note":          "approved this merge request",
			"noteable_type": "MergeRequest",
			"system":        true,
		},
		"merge_request": map[string]any{
			"iid": 42,
			"url": "https://gitlab.com/org/repo/-/merge_requests/42",
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

	// System notes should be ignored — no comments or field updates.
	comments := daemon.getComments()
	if len(comments) != 0 {
		t.Errorf("expected 0 comments for system note, got %d", len(comments))
	}
	bead := daemon.getBead("bead-1")
	if bead.Fields["mr_has_review_comments"] != "" {
		t.Errorf("mr_has_review_comments should be empty for system note, got %s", bead.Fields["mr_has_review_comments"])
	}
}

func TestGitLabWebhookHandler_NoteEvent_NoMatchingBead(t *testing.T) {
	daemon := newMockGitLabDaemon()
	daemon.beads["bead-1"] = &beadsapi.BeadDetail{
		ID:     "bead-1",
		Type:   "task",
		Fields: map[string]string{"mr_url": "https://gitlab.com/other/repo/-/merge_requests/99"},
	}

	handler := GitLabWebhookHandler(nil, daemon, "secret", slog.Default())

	event := map[string]any{
		"object_kind": "note",
		"user":        map[string]any{"username": "alice"},
		"object_attributes": map[string]any{
			"note":          "looks good",
			"noteable_type": "MergeRequest",
			"system":        false,
		},
		"merge_request": map[string]any{
			"iid": 42,
			"url": "https://gitlab.com/org/repo/-/merge_requests/42",
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

	// No matching bead — no comments added.
	comments := daemon.getComments()
	if len(comments) != 0 {
		t.Errorf("expected 0 comments for non-matching MR, got %d", len(comments))
	}
}

func TestGitLabWebhookHandler_PipelineEvent_NoMR(t *testing.T) {
	daemon := newMockGitLabDaemon()
	handler := GitLabWebhookHandler(nil, daemon, "secret", slog.Default())

	event := map[string]any{
		"object_kind": "pipeline",
		"object_attributes": map[string]any{
			"id":     456,
			"status": "success",
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
	// No MR in event — should be a no-op.
}
