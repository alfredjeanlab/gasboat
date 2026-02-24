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

// --- Config / New tests ---

func TestNew_AutoPrependsHTTPScheme(t *testing.T) {
	c, err := New(Config{HTTPAddr: "localhost:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("expected http://localhost:8080, got %s", c.baseURL)
	}
}

func TestNew_DoesNotDoublePrependHTTP(t *testing.T) {
	c, err := New(Config{HTTPAddr: "http://already-has-scheme:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "http://already-has-scheme:8080" {
		t.Errorf("expected http://already-has-scheme:8080, got %s", c.baseURL)
	}
}

func TestNew_DoesNotDoublePrependHTTPS(t *testing.T) {
	c, err := New(Config{HTTPAddr: "https://secure:443"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "https://secure:443" {
		t.Errorf("expected https://secure:443, got %s", c.baseURL)
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	c, err := New(Config{HTTPAddr: "http://host:8080/"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "http://host:8080" {
		t.Errorf("expected trailing slash removed, got %s", c.baseURL)
	}
}

func TestNew_EmptyAddrReturnsError(t *testing.T) {
	_, err := New(Config{HTTPAddr: ""})
	if err == nil {
		t.Fatal("expected error for empty HTTPAddr")
	}
	if !strings.Contains(err.Error(), "HTTPAddr is required") {
		t.Errorf("expected 'HTTPAddr is required' error, got: %v", err)
	}
}

func TestClose_NoOp(t *testing.T) {
	c, _ := New(Config{HTTPAddr: "localhost:1"})
	if err := c.Close(); err != nil {
		t.Errorf("Close should be no-op, got error: %v", err)
	}
}

// --- GetBead tests ---

func TestGetBead_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/beads/bd-abc123" {
			t.Errorf("expected path /v1/beads/bd-abc123, got %s", r.URL.Path)
		}
		bead := beadJSON{
			ID:     "bd-abc123",
			Title:  "Test bead",
			Type:   "issue",
			Status: "open",
			Labels: []string{"p1", "bug"},
			Notes:  "key: value",
			Fields: json.RawMessage(`{"priority":"high","component":"api"}`),
		}
		json.NewEncoder(w).Encode(bead)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	detail, err := c.GetBead(context.Background(), "bd-abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if detail.ID != "bd-abc123" {
		t.Errorf("expected ID bd-abc123, got %s", detail.ID)
	}
	if detail.Title != "Test bead" {
		t.Errorf("expected title 'Test bead', got %s", detail.Title)
	}
	if detail.Type != "issue" {
		t.Errorf("expected type issue, got %s", detail.Type)
	}
	if detail.Status != "open" {
		t.Errorf("expected status open, got %s", detail.Status)
	}
	if len(detail.Labels) != 2 || detail.Labels[0] != "p1" || detail.Labels[1] != "bug" {
		t.Errorf("expected labels [p1, bug], got %v", detail.Labels)
	}
	if detail.Notes != "key: value" {
		t.Errorf("expected notes 'key: value', got %s", detail.Notes)
	}
	if detail.Fields["priority"] != "high" {
		t.Errorf("expected field priority=high, got %s", detail.Fields["priority"])
	}
	if detail.Fields["component"] != "api" {
		t.Errorf("expected field component=api, got %s", detail.Fields["component"])
	}
}

func TestGetBead_HandlesEmptyFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bead := beadJSON{
			ID:     "bd-empty",
			Title:  "No fields",
			Type:   "issue",
			Status: "open",
		}
		json.NewEncoder(w).Encode(bead)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	detail, err := c.GetBead(context.Background(), "bd-empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(detail.Fields) != 0 {
		t.Errorf("expected empty fields, got %v", detail.Fields)
	}
}

func TestGetBead_EscapesBeadID(t *testing.T) {
	var gotRawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawPath = r.URL.RawPath
		bead := beadJSON{ID: "has/slash"}
		json.NewEncoder(w).Encode(bead)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.GetBead(context.Background(), "has/slash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The slash should be percent-encoded in the raw URL.
	if gotRawPath != "/v1/beads/has%2Fslash" {
		t.Errorf("expected encoded path, got %s", gotRawPath)
	}
}

// --- CloseBead tests ---

func TestCloseBead_SendsCloseRequest(t *testing.T) {
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
	err := c.CloseBead(context.Background(), "bd-close1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/beads/bd-close1/close" {
		t.Errorf("expected path /v1/beads/bd-close1/close, got %s", gotPath)
	}
	if gotBody["closed_by"] != "gasboat" {
		t.Errorf("expected closed_by=gasboat, got %v", gotBody)
	}
}

func TestCloseBead_UpdatesFieldsBeforeClose(t *testing.T) {
	var requests []struct {
		Method string
		Path   string
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, struct {
			Method string
			Path   string
		}{r.Method, r.URL.Path})

		switch {
		case r.Method == http.MethodGet:
			bead := beadJSON{
				ID:     "bd-close2",
				Fields: json.RawMessage(`{"existing":"val"}`),
			}
			json.NewEncoder(w).Encode(bead)

		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.CloseBead(context.Background(), "bd-close2", map[string]string{"exit_code": "0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be 3 requests: GET (read fields), PATCH (update fields), POST (close).
	if len(requests) != 3 {
		t.Fatalf("expected 3 requests (GET+PATCH+POST), got %d: %v", len(requests), requests)
	}
	if requests[0].Method != http.MethodGet {
		t.Errorf("first request should be GET, got %s", requests[0].Method)
	}
	if requests[1].Method != http.MethodPatch {
		t.Errorf("second request should be PATCH, got %s", requests[1].Method)
	}
	if requests[2].Method != http.MethodPost {
		t.Errorf("third request should be POST, got %s", requests[2].Method)
	}
	if requests[2].Path != "/v1/beads/bd-close2/close" {
		t.Errorf("third request should be /close, got %s", requests[2].Path)
	}
}

func TestCloseBead_SkipsFieldUpdateWhenEmpty(t *testing.T) {
	var requests []struct {
		Method string
		Path   string
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, struct {
			Method string
			Path   string
		}{r.Method, r.URL.Path})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.CloseBead(context.Background(), "bd-close3", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With nil fields, only the POST /close should happen.
	if len(requests) != 1 {
		t.Fatalf("expected 1 request (POST only), got %d: %v", len(requests), requests)
	}
	if requests[0].Method != http.MethodPost {
		t.Errorf("expected POST, got %s", requests[0].Method)
	}
}

// --- SetConfig tests ---

func TestSetConfig_SendsCorrectRequest(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.SetConfig(context.Background(), "my-key", []byte(`"my-value"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %s", gotMethod)
	}
	if gotPath != "/v1/configs/my-key" {
		t.Errorf("expected path /v1/configs/my-key, got %s", gotPath)
	}

	valueRaw, ok := gotBody["value"]
	if !ok {
		t.Fatal("expected 'value' key in body")
	}
	if string(valueRaw) != `"my-value"` {
		t.Errorf("expected value '\"my-value\"', got %s", string(valueRaw))
	}
}

func TestSetConfig_EscapesConfigKey(t *testing.T) {
	var gotRawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawPath = r.URL.RawPath
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.SetConfig(context.Background(), "key/with/slashes", []byte(`"val"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotRawPath != "/v1/configs/key%2Fwith%2Fslashes" {
		t.Errorf("expected encoded path, got %s", gotRawPath)
	}
}

// --- Content-Type header test ---

func TestDoJSON_SetsContentTypeForBody(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	c.UpdateBeadNotes(context.Background(), "bd-ct", "notes")

	if gotContentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", gotContentType)
	}
}

func TestDoJSON_NoContentTypeForGET(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		bead := beadJSON{ID: "bd-get"}
		json.NewEncoder(w).Encode(bead)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	c.GetBead(context.Background(), "bd-get")

	if gotContentType != "" {
		t.Errorf("expected no Content-Type for GET, got %s", gotContentType)
	}
}

// --- BeadLister interface compliance ---

func TestClient_ImplementsBeadLister(t *testing.T) {
	var _ BeadLister = (*Client)(nil)
}
