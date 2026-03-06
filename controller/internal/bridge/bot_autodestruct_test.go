package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsAutoDestructAllowed(t *testing.T) {
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(newMockDaemon(), slackSrv)

	t.Run("denied when env empty", func(t *testing.T) {
		t.Setenv("AUTODESTRUCT_ALLOWED_USERS", "")
		if bot.isAutoDestructAllowed("U123") {
			t.Error("expected denied when AUTODESTRUCT_ALLOWED_USERS is empty")
		}
	})

	t.Run("allowed when user in list", func(t *testing.T) {
		t.Setenv("AUTODESTRUCT_ALLOWED_USERS", "U111,U123,U999")
		if !bot.isAutoDestructAllowed("U123") {
			t.Error("expected allowed for U123")
		}
	})

	t.Run("denied when user not in list", func(t *testing.T) {
		t.Setenv("AUTODESTRUCT_ALLOWED_USERS", "U111,U999")
		if bot.isAutoDestructAllowed("U123") {
			t.Error("expected denied for U123")
		}
	})

	t.Run("handles whitespace in list", func(t *testing.T) {
		t.Setenv("AUTODESTRUCT_ALLOWED_USERS", "U111, U123 ,U999")
		if !bot.isAutoDestructAllowed("U123") {
			t.Error("expected allowed for U123 with surrounding spaces")
		}
	})
}

func TestCallAutoDestruct(t *testing.T) {
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	t.Run("success", func(t *testing.T) {
		controllerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("expected bearer token, got %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("X-Actor") != "slack:U123" {
				t.Errorf("expected actor slack:U123, got %q", r.Header.Get("X-Actor"))
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "destructed", "killed": 3})
		}))
		defer controllerSrv.Close()

		bot := newTestBot(newMockDaemon(), slackSrv)
		bot.controllerURL = controllerSrv.URL

		t.Setenv("AUTODESTRUCT_TOKEN", "test-token")

		result, err := bot.callAutoDestruct(context.Background(), "POST", "/autodestruct", "U123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var resp struct {
			Status string `json:"status"`
			Killed int    `json:"killed"`
		}
		if err := json.Unmarshal([]byte(result), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if resp.Killed != 3 {
			t.Errorf("expected 3 killed, got %d", resp.Killed)
		}
	})

	t.Run("no controller URL", func(t *testing.T) {
		bot := newTestBot(newMockDaemon(), slackSrv)
		bot.controllerURL = ""

		t.Setenv("AUTODESTRUCT_TOKEN", "test-token")

		_, err := bot.callAutoDestruct(context.Background(), "POST", "/autodestruct", "U123")
		if err == nil {
			t.Fatal("expected error when controller URL is empty")
		}
	})

	t.Run("no token", func(t *testing.T) {
		bot := newTestBot(newMockDaemon(), slackSrv)
		bot.controllerURL = "http://localhost:9999"

		t.Setenv("AUTODESTRUCT_TOKEN", "")

		_, err := bot.callAutoDestruct(context.Background(), "POST", "/autodestruct", "U123")
		if err == nil {
			t.Fatal("expected error when token is empty")
		}
	})

	t.Run("controller returns error", func(t *testing.T) {
		controllerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"something broke"}`))
		}))
		defer controllerSrv.Close()

		bot := newTestBot(newMockDaemon(), slackSrv)
		bot.controllerURL = controllerSrv.URL

		t.Setenv("AUTODESTRUCT_TOKEN", "test-token")

		_, err := bot.callAutoDestruct(context.Background(), "POST", "/autodestruct", "U123")
		if err == nil {
			t.Fatal("expected error on 500 response")
		}
	})
}
