package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// writeJSON encodes v as JSON to w, failing the test on error.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("writeJSON: %v", err)
	}
}

// mockDaemon creates a test HTTP server that returns canned bead responses.
func mockDaemon(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/beads/test-advice-1"):
			writeJSON(t, w, map[string]any{
				"id":          "test-advice-1",
				"title":       "Test Advice",
				"type":        "advice",
				"status":      "open",
				"labels":      []string{"global"},
				"description": "Test description",
				"fields":      map[string]string{"hook_command": "echo hi"},
			})
		case r.Method == "GET" && r.URL.Path == "/v1/beads":
			q := r.URL.Query()
			beadType := q.Get("type")
			if beadType == "advice" {
				writeJSON(t, w, map[string]any{
					"beads": []map[string]any{
						{
							"id":          "test-advice-1",
							"title":       "Global Advice",
							"type":        "advice",
							"status":      "open",
							"labels":      []string{"global"},
							"description": "Global advice text",
							"fields":      map[string]string{},
						},
						{
							"id":          "test-advice-2",
							"title":       "Role Advice",
							"type":        "advice",
							"status":      "open",
							"labels":      []string{"role:crew"},
							"description": "Role advice text",
							"fields":      map[string]string{},
						},
					},
					"total": 2,
				})
			} else if beadType == "agent" {
				writeJSON(t, w, map[string]any{
					"beads": []map[string]any{
						{
							"id":     "agent-1",
							"title":  "test-bot",
							"type":   "agent",
							"status": "open",
							"labels": []string{},
							"fields": map[string]string{
								"agent":       "test-bot",
								"project":     "gasboat",
								"role":        "crew",
								"mode":        "crew",
								"agent_state": "working",
							},
						},
					},
					"total": 1,
				})
			} else {
				writeJSON(t, w, map[string]any{"beads": []any{}, "total": 0})
			}
		case r.Method == "POST" && r.URL.Path == "/v1/beads":
			writeJSON(t, w, map[string]string{"id": "new-bead-id"})
		case r.Method == "PATCH":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/labels"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dependencies"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/labels/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			writeJSON(t, w, map[string]any{"beads": []any{}, "total": 0})
		}
	}))
}

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	mock := mockDaemon(t)
	daemon, err := beadsapi.New(beadsapi.Config{HTTPAddr: mock.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		daemon.Close()
		mock.Close()
	})
	logger := slog.Default()
	srv := NewServer(daemon, logger, "")
	return srv, mock
}

func TestHandleIndex(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "test-bot") {
		t.Error("expected agent name in response")
	}
	if !strings.Contains(body, "gasboat") {
		t.Error("expected project name in response")
	}
}

func TestHandleAdviceList(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/advice", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Global Advice") {
		t.Error("expected advice title in response")
	}
}

func TestHandleAdviceShow(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/advice/test-advice-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Test Advice") {
		t.Error("expected advice title in response")
	}
}

func TestHandleAdviceEdit(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/advice/test-advice-1/edit", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Test Advice") {
		t.Error("expected advice title in edit form")
	}
}

func TestHandleAdviceUpdate(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{
		"title":        {"Updated Title"},
		"description":  {"Updated desc"},
		"labels":       {"global, role:crew"},
		"hook_command": {"echo updated"},
		"hook_trigger": {"on_start"},
	}
	req := httptest.NewRequest("POST", "/advice/test-advice-1/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/advice/test-advice-1" {
		t.Errorf("expected redirect to /advice/test-advice-1, got %s", loc)
	}
}

func TestHandleAdviceCreate(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{
		"title":       {"New Advice"},
		"description": {"Some advice"},
		"project":     {"gasboat"},
		"role":        {"crew"},
	}
	req := httptest.NewRequest("POST", "/advice/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", w.Code)
	}
}

func TestHandleGenerateForm(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/generate", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Generate") {
		t.Error("expected generate form in response")
	}
}

func TestHandleGenerateDispatch(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{
		"topic":   {"Best practices for code review"},
		"project": {"gasboat"},
	}
	req := httptest.NewRequest("POST", "/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Created task") {
		t.Error("expected success message in response")
	}
	if !strings.Contains(body, "agent") {
		t.Error("expected agent reference in success message")
	}
}

func TestHandleGenerateDispatch_MissingTopic(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{"project": {"gasboat"}}
	req := httptest.NewRequest("POST", "/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleGenerateDispatch_MissingProject(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{"topic": {"some advice topic"}}
	req := httptest.NewRequest("POST", "/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleAgent_MissingID(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/agent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestParseLabelsString(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"global", 1},
		{"global, role:crew", 2},
		{"global, , role:crew, ", 2},
	}
	for _, tt := range tests {
		got := parseLabelsString(tt.input)
		if len(got) != tt.want {
			t.Errorf("parseLabelsString(%q) = %d labels, want %d", tt.input, len(got), tt.want)
		}
	}
}
