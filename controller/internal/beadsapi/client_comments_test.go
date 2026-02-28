package beadsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddComment_SendsCorrectRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.AddComment(context.Background(), "kd-task1", "matt-1", "Working on this now")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/beads/kd-task1/comments" {
		t.Errorf("expected path /v1/beads/kd-task1/comments, got %s", gotPath)
	}
	if gotBody["author"] != "matt-1" {
		t.Errorf("expected author=matt-1, got %s", gotBody["author"])
	}
	if gotBody["text"] != "Working on this now" {
		t.Errorf("expected text, got %s", gotBody["text"])
	}
}

func TestAddComment_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"bead not found"}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.AddComment(context.Background(), "kd-missing", "bot", "test")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestGetComments_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/beads/kd-task1/comments" {
			t.Errorf("expected path /v1/beads/kd-task1/comments, got %s", r.URL.Path)
		}
		resp := map[string]any{
			"comments": []map[string]any{
				{"id": "c1", "author": "alice", "text": "First comment", "created_at": "2026-02-28T10:00:00Z"},
				{"id": "c2", "author": "bob", "text": "Second comment", "created_at": "2026-02-28T11:00:00Z"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	comments, err := c.GetComments(context.Background(), "kd-task1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	if comments[0].Author != "alice" || comments[0].Text != "First comment" {
		t.Errorf("unexpected first comment: %+v", comments[0])
	}
	if comments[1].Author != "bob" {
		t.Errorf("unexpected second comment author: %s", comments[1].Author)
	}
}

func TestGetComments_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"comments": []any{}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	comments, err := c.GetComments(context.Background(), "kd-empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(comments))
	}
}
