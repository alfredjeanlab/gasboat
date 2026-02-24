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

// --- UpdateBeadFields tests ---

func TestUpdateBeadFields_MergesFields(t *testing.T) {
	var putBody map[string]json.RawMessage
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.Method == http.MethodGet:
			// Return bead with existing fields.
			bead := beadJSON{
				ID:     "bd-merge",
				Fields: json.RawMessage(`{"existing":"keep","overwrite":"old"}`),
			}
			json.NewEncoder(w).Encode(bead)

		case r.Method == http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &putBody)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadFields(context.Background(), "bd-merge", map[string]string{
		"overwrite": "new",
		"added":     "fresh",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if callCount != 2 {
		t.Fatalf("expected 2 HTTP calls (GET + PATCH), got %d", callCount)
	}

	// Verify the PATCH body contains merged fields.
	fieldsRaw, ok := putBody["fields"]
	if !ok {
		t.Fatal("expected 'fields' key in PATCH body")
	}
	var merged map[string]string
	if err := json.Unmarshal(fieldsRaw, &merged); err != nil {
		t.Fatalf("failed to unmarshal merged fields: %v", err)
	}

	if merged["existing"] != "keep" {
		t.Errorf("expected existing field preserved, got %s", merged["existing"])
	}
	if merged["overwrite"] != "new" {
		t.Errorf("expected overwritten field updated, got %s", merged["overwrite"])
	}
	if merged["added"] != "fresh" {
		t.Errorf("expected new field added, got %s", merged["added"])
	}
}

func TestUpdateBeadFields_HandlesNilExistingFields(t *testing.T) {
	var putBody map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			// Return bead with no fields (nil Fields).
			bead := beadJSON{ID: "bd-nil"}
			json.NewEncoder(w).Encode(bead)

		case r.Method == http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &putBody)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadFields(context.Background(), "bd-nil", map[string]string{
		"new_field": "value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fieldsRaw := putBody["fields"]
	var merged map[string]string
	if err := json.Unmarshal(fieldsRaw, &merged); err != nil {
		t.Fatalf("failed to unmarshal fields: %v", err)
	}
	if merged["new_field"] != "value" {
		t.Errorf("expected new_field=value, got %s", merged["new_field"])
	}
}

// --- UpdateBeadNotes tests ---

func TestUpdateBeadNotes_SendsCorrectBody(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadNotes(context.Background(), "bd-notes1", "coop_url: http://coop:9090\npod_name: agent-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("expected PATCH, got %s", gotMethod)
	}
	if gotPath != "/v1/beads/bd-notes1" {
		t.Errorf("expected path /v1/beads/bd-notes1, got %s", gotPath)
	}
	if gotBody["notes"] != "coop_url: http://coop:9090\npod_name: agent-0" {
		t.Errorf("expected notes body, got %v", gotBody)
	}
}

// --- UpdateAgentState tests ---

func TestUpdateAgentState_SetsFieldViaUpdateBeadFields(t *testing.T) {
	var putBody map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			bead := beadJSON{
				ID:     "bd-state1",
				Fields: json.RawMessage(`{"project":"town"}`),
			}
			json.NewEncoder(w).Encode(bead)

		case r.Method == http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &putBody)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateAgentState(context.Background(), "bd-state1", "running")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fieldsRaw := putBody["fields"]
	var merged map[string]string
	if err := json.Unmarshal(fieldsRaw, &merged); err != nil {
		t.Fatalf("failed to unmarshal fields: %v", err)
	}
	if merged["agent_state"] != "running" {
		t.Errorf("expected agent_state=running, got %s", merged["agent_state"])
	}
	if merged["project"] != "town" {
		t.Errorf("expected existing project field preserved, got %s", merged["project"])
	}
}

// --- Error handling tests ---

func TestAPIError_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "bead not found"})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.GetBead(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for 404")
	}

	apiErr, ok := err.(*APIError)
	// The error is wrapped by GetBead, so unwrap it.
	if !ok {
		// Check if it's wrapped.
		var inner *APIError
		if unwrapped, ok2 := err.(interface{ Unwrap() error }); ok2 {
			inner, _ = unwrapped.Unwrap().(*APIError)
		}
		if inner == nil {
			t.Fatalf("expected *APIError, got %T: %v", err, err)
		}
		apiErr = inner
	}

	if apiErr.StatusCode != 404 {
		t.Errorf("expected status 404, got %d", apiErr.StatusCode)
	}
	if apiErr.Message != "bead not found" {
		t.Errorf("expected message 'bead not found', got %s", apiErr.Message)
	}
}

func TestAPIError_500WithJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal server error"})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.GetBead(context.Background(), "bd-500")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "internal server error") {
		t.Errorf("expected error message to contain 'internal server error', got: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}

func TestAPIError_500WithPlainTextBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("something went wrong"))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.GetBead(context.Background(), "bd-plain")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("expected plain text body in error, got: %v", err)
	}
}

func TestAPIError_ErrorStringFormat(t *testing.T) {
	e := &APIError{StatusCode: 422, Message: "invalid fields"}
	got := e.Error()
	want := "HTTP 422: invalid fields"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestDoJSON_204NoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadNotes(context.Background(), "bd-204", "notes")
	if err != nil {
		t.Fatalf("204 No Content should not be an error, got: %v", err)
	}
}

func TestListAgentBeads_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.ListAgentBeads(context.Background())
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !strings.Contains(err.Error(), "listing agent beads") {
		t.Errorf("expected wrapped error from ListAgentBeads, got: %v", err)
	}
}

func TestUpdateBeadFields_GetFailsPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadFields(context.Background(), "bd-missing", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("expected error when GET fails during field update")
	}
	if !strings.Contains(err.Error(), "reading bead") {
		t.Errorf("expected 'reading bead' in error, got: %v", err)
	}
}

func TestCloseBead_FieldUpdateFailsPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// All requests return 500 -- the GET in UpdateBeadFields will fail.
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.CloseBead(context.Background(), "bd-fail", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("expected error when field update fails before close")
	}
	if !strings.Contains(err.Error(), "updating fields before close") {
		t.Errorf("expected 'updating fields before close' in error, got: %v", err)
	}
}

// --- parseNotes tests ---

func TestParseNotes_ParsesKeyValueLines(t *testing.T) {
	notes := "coop_url: http://coop:9090\npod_name: agent-hq-0\n"
	m := ParseNotes(notes)
	if m["coop_url"] != "http://coop:9090" {
		t.Errorf("expected coop_url, got %v", m)
	}
	if m["pod_name"] != "agent-hq-0" {
		t.Errorf("expected pod_name, got %v", m)
	}
}

func TestParseNotes_HandlesEmptyString(t *testing.T) {
	m := ParseNotes("")
	if m != nil {
		t.Errorf("expected nil for empty notes, got %v", m)
	}
}

func TestParseNotes_SkipsBlankLines(t *testing.T) {
	notes := "key1: val1\n\n\nkey2: val2\n"
	m := ParseNotes(notes)
	if len(m) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(m), m)
	}
}

func TestParseNotes_HandlesColonInValue(t *testing.T) {
	notes := "url: http://host:8080/path"
	m := ParseNotes(notes)
	if m["url"] != "http://host:8080/path" {
		t.Errorf("expected URL value preserved, got %s", m["url"])
	}
}

func TestParseNotes_TrimsWhitespace(t *testing.T) {
	notes := "  key  :  value  "
	m := ParseNotes(notes)
	if m["key"] != "value" {
		t.Errorf("expected trimmed key/value, got key=%q value=%q", "key", m["key"])
	}
}

func TestParseNotes_NoColonLinesIgnored(t *testing.T) {
	notes := "no-colon-here\nkey: val"
	m := ParseNotes(notes)
	if len(m) != 1 {
		t.Errorf("expected 1 entry, got %d: %v", len(m), m)
	}
	if m["key"] != "val" {
		t.Errorf("expected key=val, got %v", m)
	}
}
