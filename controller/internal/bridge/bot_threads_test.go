package bridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSlackThreadAPI_GetMessages_MissingParams(t *testing.T) {
	api := NewSlackThreadAPI(nil, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	tests := []struct {
		name string
		url  string
	}{
		{"missing both", "/api/slack/threads"},
		{"missing ts", "/api/slack/threads?channel=C-test"},
		{"missing channel", "/api/slack/threads?ts=1111.2222"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestSlackThreadAPI_GetMessages_MethodNotAllowed(t *testing.T) {
	api := NewSlackThreadAPI(nil, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/slack/threads?channel=C-test&ts=1.1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("got status %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestSlackThreadAPI_PostReply_MissingFields(t *testing.T) {
	api := NewSlackThreadAPI(nil, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	tests := []struct {
		name string
		body replyRequest
	}{
		{"missing channel", replyRequest{ThreadTS: "1.1", Text: "hello"}},
		{"missing thread_ts", replyRequest{Channel: "C-test", Text: "hello"}},
		{"missing text", replyRequest{Channel: "C-test", ThreadTS: "1.1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/api/slack/threads/reply", bytes.NewReader(body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestSlackThreadAPI_PostReply_MethodNotAllowed(t *testing.T) {
	api := NewSlackThreadAPI(nil, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/slack/threads/reply", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("got status %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestSlackThreadAPI_PostReply_InvalidBody(t *testing.T) {
	api := NewSlackThreadAPI(nil, slog.Default())
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/slack/threads/reply",
		bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want %d", w.Code, http.StatusBadRequest)
	}
}
