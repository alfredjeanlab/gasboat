package beadsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- CreateBead tests ---

func TestCreateBead_SendsCorrectRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody CreateBeadRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		resp := map[string]string{"id": "kd-new123"}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	id, err := c.CreateBead(context.Background(), CreateBeadRequest{
		Title:    "New task",
		Type:     "task",
		Priority: 2,
		Labels:   []string{"project:gasboat"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "kd-new123" {
		t.Errorf("expected id kd-new123, got %s", id)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/beads" {
		t.Errorf("expected path /v1/beads, got %s", gotPath)
	}
	if gotBody.Title != "New task" {
		t.Errorf("expected title 'New task', got %s", gotBody.Title)
	}
	if gotBody.Type != "task" {
		t.Errorf("expected type task, got %s", gotBody.Type)
	}
}

func TestCreateBead_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"missing title"}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.CreateBead(context.Background(), CreateBeadRequest{Type: "task"})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

// --- DeleteBead tests ---

func TestDeleteBead_SendsCorrectRequest(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.DeleteBead(context.Background(), "kd-del1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("expected DELETE, got %s", gotMethod)
	}
	if gotPath != "/v1/beads/kd-del1" {
		t.Errorf("expected path /v1/beads/kd-del1, got %s", gotPath)
	}
}

// --- UpdateBead tests ---

func TestUpdateBead_SendsOnlySetFields(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	title := "Updated title"
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBead(context.Background(), "kd-upd1", UpdateBeadRequest{
		Title: &title,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["title"] != "Updated title" {
		t.Errorf("expected title in body, got %v", gotBody)
	}
	if _, ok := gotBody["description"]; ok {
		t.Error("description should not be in body when not set")
	}
}

func TestUpdateBead_NoOpWhenEmpty(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBead(context.Background(), "kd-noop", UpdateBeadRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestCount != 0 {
		t.Errorf("expected no requests for empty update, got %d", requestCount)
	}
}

func TestUpdateBead_AllFields(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	title := "T"
	desc := "D"
	assignee := "matt"
	status := "closed"
	notes := "N"
	priority := 1

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBead(context.Background(), "kd-full", UpdateBeadRequest{
		Title:       &title,
		Description: &desc,
		Assignee:    &assignee,
		Status:      &status,
		Notes:       &notes,
		Priority:    &priority,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["title"] != "T" {
		t.Errorf("expected title=T, got %v", gotBody["title"])
	}
	if gotBody["assignee"] != "matt" {
		t.Errorf("expected assignee=matt, got %v", gotBody["assignee"])
	}
}

// --- AddDependency tests ---

func TestAddDependency_SendsCorrectRequest(t *testing.T) {
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
	err := c.AddDependency(context.Background(), "kd-child", "kd-parent", "blocks", "matt-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/beads/kd-child/dependencies" {
		t.Errorf("expected path /v1/beads/kd-child/dependencies, got %s", gotPath)
	}
	if gotBody["depends_on_id"] != "kd-parent" {
		t.Errorf("expected depends_on_id=kd-parent, got %s", gotBody["depends_on_id"])
	}
	if gotBody["type"] != "blocks" {
		t.Errorf("expected type=blocks, got %s", gotBody["type"])
	}
	if gotBody["created_by"] != "matt-1" {
		t.Errorf("expected created_by=matt-1, got %s", gotBody["created_by"])
	}
}

// --- GetDependencies tests ---

func TestGetDependencies_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/beads/kd-task1/dependencies" {
			t.Errorf("expected path /v1/beads/kd-task1/dependencies, got %s", r.URL.Path)
		}
		resp := map[string]any{
			"dependencies": []map[string]string{
				{"bead_id": "kd-task1", "depends_on_id": "kd-epic1", "type": "parent-child"},
				{"bead_id": "kd-task1", "depends_on_id": "kd-task0", "type": "blocks"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	deps, err := c.GetDependencies(context.Background(), "kd-task1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependencies, got %d", len(deps))
	}
	if deps[0].DependsOnID != "kd-epic1" || deps[0].Type != "parent-child" {
		t.Errorf("unexpected first dep: %+v", deps[0])
	}
}

func TestGetDependencies_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"dependencies": []any{}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	deps, err := c.GetDependencies(context.Background(), "kd-nodeps")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected 0 dependencies, got %d", len(deps))
	}
}

// --- Health tests ---

func TestHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/health" {
			t.Errorf("expected path /v1/health, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHealth_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"database unreachable"}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error for unhealthy server")
	}
	if !strings.Contains(err.Error(), "health check failed") {
		t.Errorf("expected health check error, got: %v", err)
	}
}

// --- ListBeadsFiltered tests ---

func TestListBeadsFiltered_AllQueryParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{ID: "kd-1", Title: "Task 1", Type: "task", Status: "open"},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	result, err := c.ListBeadsFiltered(context.Background(), ListBeadsQuery{
		Types:      []string{"task", "bug"},
		Statuses:   []string{"open"},
		Labels:     []string{"project:gasboat"},
		Assignee:   "matt-1",
		Search:     "auth",
		Sort:       "priority",
		NoOpenDeps: true,
		Limit:      20,
		Offset:     10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Errorf("expected total=1, got %d", result.Total)
	}
	if len(result.Beads) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(result.Beads))
	}

	for _, expected := range []string{
		"type=task%2Cbug",
		"status=open",
		"labels=project%3Agasboat",
		"assignee=matt-1",
		"search=auth",
		"sort=priority",
		"no_open_deps=true",
		"limit=20",
		"offset=10",
	} {
		if !strings.Contains(gotQuery, expected) {
			t.Errorf("expected query to contain %s, got %s", expected, gotQuery)
		}
	}
}

func TestListBeadsFiltered_EmptyQuery(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		resp := listBeadsResponse{Beads: nil, Total: 0}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	result, err := c.ListBeadsFiltered(context.Background(), ListBeadsQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("expected total=0, got %d", result.Total)
	}
	if gotPath != "/v1/beads" {
		t.Errorf("expected path /v1/beads, got %s", gotPath)
	}
}
