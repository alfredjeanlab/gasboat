package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"log/slog"

	"gasboat/controller/internal/beadsapi"
)

// mockDecisionClient implements DecisionAPIClient for testing.
type mockDecisionClient struct {
	decisions []beadsapi.DecisionDetail
	resolveID string
	cancelID  string
	err       error
}

func (m *mockDecisionClient) ListDecisions(_ context.Context, status string, limit int) ([]beadsapi.DecisionDetail, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.decisions, nil
}

func (m *mockDecisionClient) GetDecision(_ context.Context, id string) (*beadsapi.DecisionDetail, error) {
	if m.err != nil {
		return nil, m.err
	}
	for _, d := range m.decisions {
		if d.Decision != nil && d.Decision.ID == id {
			return &d, nil
		}
	}
	return nil, &beadsapi.APIError{StatusCode: 404, Message: "not found"}
}

func (m *mockDecisionClient) ResolveDecision(_ context.Context, id string, _ beadsapi.ResolveDecisionRequest) error {
	m.resolveID = id
	return m.err
}

func (m *mockDecisionClient) CancelDecision(_ context.Context, id, reason, canceledBy string) error {
	m.cancelID = id
	return m.err
}

func TestDecisionAPI_List(t *testing.T) {
	client := &mockDecisionClient{
		decisions: []beadsapi.DecisionDetail{
			{Decision: &beadsapi.BeadDetail{
				ID:       "kd-test1",
				Title:    "Test decision",
				Type:     "decision",
				Status:   "open",
				Priority: 2,
				Fields:   map[string]string{"prompt": "Which approach?"},
			}},
		},
	}
	api := NewDecisionAPI(client, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/decisions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}

	var resp struct {
		Decisions []beadsapi.DecisionDetail `json:"decisions"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Decisions) != 1 {
		t.Fatalf("got %d decisions, want 1", len(resp.Decisions))
	}
	if resp.Decisions[0].Decision.ID != "kd-test1" {
		t.Errorf("got id=%q, want kd-test1", resp.Decisions[0].Decision.ID)
	}
}

func TestDecisionAPI_Show(t *testing.T) {
	client := &mockDecisionClient{
		decisions: []beadsapi.DecisionDetail{
			{Decision: &beadsapi.BeadDetail{
				ID:     "kd-show1",
				Title:  "Show me",
				Type:   "decision",
				Status: "open",
			}},
		},
	}
	api := NewDecisionAPI(client, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/decisions/kd-show1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}

	var resp beadsapi.DecisionDetail
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Decision == nil || resp.Decision.ID != "kd-show1" {
		t.Errorf("got unexpected decision detail")
	}
}

func TestDecisionAPI_Resolve(t *testing.T) {
	client := &mockDecisionClient{}
	api := NewDecisionAPI(client, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	body := `{"chosen":"option-a","rationale":"because"}`
	req := httptest.NewRequest(http.MethodPost, "/api/decisions/kd-res1/resolve", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}
	if client.resolveID != "kd-res1" {
		t.Errorf("resolveID=%q, want kd-res1", client.resolveID)
	}
}

func TestDecisionAPI_Resolve_MissingChosen(t *testing.T) {
	client := &mockDecisionClient{}
	api := NewDecisionAPI(client, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	body := `{"rationale":"no choice"}`
	req := httptest.NewRequest(http.MethodPost, "/api/decisions/kd-x/resolve", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
}

func TestDecisionAPI_Dismiss(t *testing.T) {
	client := &mockDecisionClient{}
	api := NewDecisionAPI(client, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	body := `{"reason":"not needed"}`
	req := httptest.NewRequest(http.MethodPost, "/api/decisions/kd-dis1/dismiss", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}
	if client.cancelID != "kd-dis1" {
		t.Errorf("cancelID=%q, want kd-dis1", client.cancelID)
	}
}

func TestDecisionAPI_MethodNotAllowed(t *testing.T) {
	client := &mockDecisionClient{}
	api := NewDecisionAPI(client, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/decisions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405", w.Code)
	}
}

func TestWebHandler_ServesIndexHTML(t *testing.T) {
	handler := http.StripPrefix("/ui/", WebHandler())

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type=%q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), "Decisions") {
		t.Error("response does not contain expected HTML content")
	}
}

func TestDecisionSSEProxy_FilterDecisions(t *testing.T) {
	// Create a mock SSE server that sends one decision and one non-decision event.
	sseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events/stream" {
			http.NotFound(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher.Flush()

		// Decision event.
		decBead := `{"bead":{"id":"kd-d1","type":"decision","title":"test","status":"open","fields":{}}}`
		_, _ = w.Write([]byte("event: beads.bead.created\ndata: " + decBead + "\n\n"))
		flusher.Flush()

		// Non-decision event (task).
		taskBead := `{"bead":{"id":"kd-t1","type":"task","title":"task","status":"open","fields":{}}}`
		_, _ = w.Write([]byte("event: beads.bead.created\ndata: " + taskBead + "\n\n"))
		flusher.Flush()

		// Wait for client disconnect.
		<-r.Context().Done()
	}))
	defer sseSrv.Close()

	proxy := NewDecisionSSEProxy(sseSrv.URL, "", slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/api/decisions/events", nil)
	ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		proxy.ServeHTTP(w, req)
		close(done)
	}()

	<-done

	body := w.Body.String()
	// Should contain the decision event.
	if !strings.Contains(body, "kd-d1") {
		t.Error("expected decision event kd-d1 in SSE output")
	}
	// Should NOT contain the task event.
	if strings.Contains(body, "kd-t1") {
		t.Error("task event kd-t1 should have been filtered out")
	}
}
