package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// TestE2E_AdviceViewer runs integration tests against a real beads daemon.
// Uses BEADS_E2E_HTTP_ADDR so spawn events hit the isolated gasboat-e2e
// beads instance, not the production agents controller.
func TestE2E_AdviceViewer(t *testing.T) {
	addr := os.Getenv("BEADS_E2E_HTTP_ADDR")
	if addr == "" {
		t.Skip("BEADS_E2E_HTTP_ADDR not set, skipping e2e tests")
	}

	daemon, err := beadsapi.New(beadsapi.Config{HTTPAddr: addr})
	if err != nil {
		t.Fatalf("creating daemon client: %v", err)
	}
	defer daemon.Close()

	logger := slog.Default()
	srv := NewServer(daemon, logger, "")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	t.Run("agent_list_page_loads", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "Agents") {
			t.Error("expected Agents heading in response")
		}
	})

	t.Run("advice_list_page_loads", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/advice", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "All Advice") {
			t.Error("expected All Advice heading in response")
		}
	})

	t.Run("advice_create_edit_roundtrip", func(t *testing.T) {
		// Create an advice bead.
		form := url.Values{
			"title":       {"E2E Test Advice"},
			"description": {"Created by e2e test"},
			"project":     {"e2e-test"},
		}
		req := httptest.NewRequest("POST", "/advice/new", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("create: expected 303, got %d: %s", w.Code, w.Body.String())
		}

		// Extract the bead ID from the redirect location.
		loc := w.Header().Get("Location")
		if !strings.HasPrefix(loc, "/advice/") {
			t.Fatalf("create: unexpected redirect location: %s", loc)
		}
		beadID := strings.TrimPrefix(loc, "/advice/")

		// View the bead.
		req = httptest.NewRequest("GET", "/advice/"+beadID, nil)
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("show: expected 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "E2E Test Advice") {
			t.Error("show: expected advice title in response")
		}

		// Edit the bead.
		editForm := url.Values{
			"title":       {"E2E Test Advice Updated"},
			"description": {"Updated by e2e test"},
			"labels":      {"project:e2e-test"},
		}
		req = httptest.NewRequest("POST", "/advice/"+beadID+"/edit", strings.NewReader(editForm.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther {
			t.Errorf("edit: expected 303, got %d: %s", w.Code, w.Body.String())
		}

		// Verify the edit.
		req = httptest.NewRequest("GET", "/advice/"+beadID, nil)
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), "E2E Test Advice Updated") {
			t.Error("show after edit: expected updated title")
		}

		// Clean up: close the bead.
		_ = daemon.CloseBead(req.Context(), beadID, nil)
	})

	t.Run("generate_dispatch_creates_task", func(t *testing.T) {
		form := url.Values{
			"topic":   {"E2E test topic for advice generation"},
			"project": {"e2e-test"},
		}
		req := httptest.NewRequest("POST", "/generate", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "Created task") {
			t.Error("expected success message")
		}
	})
}
