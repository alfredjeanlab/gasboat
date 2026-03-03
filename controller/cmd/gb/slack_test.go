package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestResolveSlackEnv_AllSet(t *testing.T) {
	t.Setenv("SLACK_BRIDGE_URL", "http://bridge:8080")
	t.Setenv("SLACK_THREAD_CHANNEL", "C123")
	t.Setenv("SLACK_THREAD_TS", "1234567890.123456")

	bridgeURL, channel, ts, err := resolveSlackEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bridgeURL != "http://bridge:8080" {
		t.Errorf("bridgeURL = %q, want %q", bridgeURL, "http://bridge:8080")
	}
	if channel != "C123" {
		t.Errorf("channel = %q, want %q", channel, "C123")
	}
	if ts != "1234567890.123456" {
		t.Errorf("ts = %q, want %q", ts, "1234567890.123456")
	}
}

func TestResolveSlackEnv_MissingBridgeURL(t *testing.T) {
	t.Setenv("SLACK_BRIDGE_URL", "")
	t.Setenv("SLACK_THREAD_CHANNEL", "C123")
	t.Setenv("SLACK_THREAD_TS", "1234567890.123456")

	_, _, _, err := resolveSlackEnv()
	if err == nil {
		t.Fatal("expected error when SLACK_BRIDGE_URL is empty")
	}
	if !strings.Contains(err.Error(), "SLACK_BRIDGE_URL") {
		t.Errorf("error should mention SLACK_BRIDGE_URL, got: %v", err)
	}
}

func TestResolveSlackEnv_MissingThreadEnvVars(t *testing.T) {
	t.Setenv("SLACK_BRIDGE_URL", "http://bridge:8080")
	t.Setenv("SLACK_THREAD_CHANNEL", "")
	t.Setenv("SLACK_THREAD_TS", "")

	_, _, _, err := resolveSlackEnv()
	if err == nil {
		t.Fatal("expected error when thread env vars are empty")
	}
	if !strings.Contains(err.Error(), "not a thread-spawned agent") {
		t.Errorf("error should say 'not a thread-spawned agent', got: %v", err)
	}
}

func TestResolveSlackEnv_MissingChannel(t *testing.T) {
	t.Setenv("SLACK_BRIDGE_URL", "http://bridge:8080")
	t.Setenv("SLACK_THREAD_CHANNEL", "")
	t.Setenv("SLACK_THREAD_TS", "12345.6789")

	_, _, _, err := resolveSlackEnv()
	if err == nil {
		t.Fatal("expected error when SLACK_THREAD_CHANNEL is empty")
	}
}

func TestResolveSlackEnv_MissingTS(t *testing.T) {
	t.Setenv("SLACK_BRIDGE_URL", "http://bridge:8080")
	t.Setenv("SLACK_THREAD_CHANNEL", "C123")
	t.Setenv("SLACK_THREAD_TS", "")

	_, _, _, err := resolveSlackEnv()
	if err == nil {
		t.Fatal("expected error when SLACK_THREAD_TS is empty")
	}
}

func TestPrintThreadMarkdown_Empty(t *testing.T) {
	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printThreadMarkdown(nil, 4000)

	w.Close()
	os.Stdout = old

	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	output := string(buf[:n])
	if !strings.Contains(output, "No thread messages") {
		t.Errorf("expected 'No thread messages' in output, got: %q", output)
	}
}

func TestPrintThreadMarkdown_FormatAndTruncate(t *testing.T) {
	msgs := []slackThreadMessage{
		{Author: "alice", Text: "Hello there"},
		{Author: "bob", Text: "Hi alice!"},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printThreadMarkdown(msgs, 4000)

	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "**alice**: Hello there") {
		t.Errorf("expected formatted alice message, got: %q", output)
	}
	if !strings.Contains(output, "**bob**: Hi alice!") {
		t.Errorf("expected formatted bob message, got: %q", output)
	}
}

func TestPrintThreadMarkdown_Truncation(t *testing.T) {
	msgs := []slackThreadMessage{
		{Author: "alice", Text: "Short message"},
		{Author: "bob", Text: "Another message that will cause truncation"},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Very small max-chars to force truncation.
	printThreadMarkdown(msgs, 30)

	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "truncated") {
		t.Errorf("expected truncation marker, got: %q", output)
	}
}

func TestSlackThread_Integration(t *testing.T) {
	// Mock bridge server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/slack/threads" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		channel := r.URL.Query().Get("channel")
		ts := r.URL.Query().Get("ts")
		if channel != "C999" || ts != "111.222" {
			t.Errorf("unexpected params: channel=%s ts=%s", channel, ts)
		}

		msgs := []slackThreadMessage{
			{Author: "user1", Text: "Need help with deployment", Timestamp: "111.223"},
			{Author: "user2", Text: "I can look into it", Timestamp: "111.224"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(msgs)
	}))
	defer server.Close()

	t.Setenv("SLACK_BRIDGE_URL", server.URL)
	t.Setenv("SLACK_THREAD_CHANNEL", "C999")
	t.Setenv("SLACK_THREAD_TS", "111.222")

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runSlackThread(slackThreadCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "**user1**: Need help with deployment") {
		t.Errorf("expected user1 message, got: %q", output)
	}
	if !strings.Contains(output, "**user2**: I can look into it") {
		t.Errorf("expected user2 message, got: %q", output)
	}
}

func TestSlackReply_Integration(t *testing.T) {
	var receivedBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/slack/threads/reply" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(slackReplyResult{OK: true, Timestamp: "111.225"})
	}))
	defer server.Close()

	t.Setenv("SLACK_BRIDGE_URL", server.URL)
	t.Setenv("SLACK_THREAD_CHANNEL", "C999")
	t.Setenv("SLACK_THREAD_TS", "111.222")

	// Capture stdout.
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	err := runSlackReply(slackReplyCmd, []string{"Thanks for the help!"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old

	if receivedBody["channel"] != "C999" {
		t.Errorf("channel = %q, want %q", receivedBody["channel"], "C999")
	}
	if receivedBody["thread_ts"] != "111.222" {
		t.Errorf("thread_ts = %q, want %q", receivedBody["thread_ts"], "111.222")
	}
	if receivedBody["text"] != "Thanks for the help!" {
		t.Errorf("text = %q, want %q", receivedBody["text"], "Thanks for the help!")
	}
}

func TestSlackThread_BridgeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("SLACK_BRIDGE_URL", server.URL)
	t.Setenv("SLACK_THREAD_CHANNEL", "C999")
	t.Setenv("SLACK_THREAD_TS", "111.222")

	err := runSlackThread(slackThreadCmd, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention HTTP 500, got: %v", err)
	}
}

func TestSlackReply_BridgeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("SLACK_BRIDGE_URL", server.URL)
	t.Setenv("SLACK_THREAD_CHANNEL", "C999")
	t.Setenv("SLACK_THREAD_TS", "111.222")

	err := runSlackReply(slackReplyCmd, []string{"test"})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention HTTP 500, got: %v", err)
	}
}

func TestIsThreadSpawnedAgent(t *testing.T) {
	t.Run("both set", func(t *testing.T) {
		t.Setenv("SLACK_THREAD_CHANNEL", "C123")
		t.Setenv("SLACK_THREAD_TS", "111.222")
		if !isThreadSpawnedAgent() {
			t.Error("expected true when both env vars set")
		}
	})
	t.Run("missing channel", func(t *testing.T) {
		t.Setenv("SLACK_THREAD_CHANNEL", "")
		t.Setenv("SLACK_THREAD_TS", "111.222")
		if isThreadSpawnedAgent() {
			t.Error("expected false when channel missing")
		}
	})
	t.Run("missing ts", func(t *testing.T) {
		t.Setenv("SLACK_THREAD_CHANNEL", "C123")
		t.Setenv("SLACK_THREAD_TS", "")
		if isThreadSpawnedAgent() {
			t.Error("expected false when ts missing")
		}
	})
	t.Run("both empty", func(t *testing.T) {
		t.Setenv("SLACK_THREAD_CHANNEL", "")
		t.Setenv("SLACK_THREAD_TS", "")
		if isThreadSpawnedAgent() {
			t.Error("expected false when both empty")
		}
	})
}

func TestFetchThreadMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msgs := []slackThreadMessage{
			{Author: "alice", Text: "Hello", Timestamp: "111.222"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(msgs)
	}))
	defer server.Close()

	msgs := fetchThreadMessages(server.URL, "C123", "111.222", 50)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Author != "alice" {
		t.Errorf("expected author alice, got %s", msgs[0].Author)
	}
}

func TestFetchThreadMessages_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer server.Close()

	msgs := fetchThreadMessages(server.URL, "C123", "111.222", 50)
	if msgs != nil {
		t.Errorf("expected nil on server error, got %v", msgs)
	}
}

func TestFetchThreadMessages_Unreachable(t *testing.T) {
	msgs := fetchThreadMessages("http://127.0.0.1:1", "C123", "111.222", 50)
	if msgs != nil {
		t.Errorf("expected nil for unreachable server, got %v", msgs)
	}
}

func TestFormatThreadMarkdown(t *testing.T) {
	msgs := []slackThreadMessage{
		{Author: "alice", Text: "Hello"},
		{Author: "bob", Text: "Hi"},
	}
	out := formatThreadMarkdown(msgs, 4000)
	if !strings.Contains(out, "**alice**: Hello") {
		t.Errorf("expected alice message, got: %q", out)
	}
	if !strings.Contains(out, "**bob**: Hi") {
		t.Errorf("expected bob message, got: %q", out)
	}
}

func TestFormatThreadMarkdown_Empty(t *testing.T) {
	out := formatThreadMarkdown(nil, 4000)
	if !strings.Contains(out, "No thread messages") {
		t.Errorf("expected 'No thread messages', got: %q", out)
	}
}

func TestFormatThreadMarkdown_Truncation(t *testing.T) {
	msgs := []slackThreadMessage{
		{Author: "alice", Text: "First message"},
		{Author: "bob", Text: "Second message that will be truncated"},
	}
	out := formatThreadMarkdown(msgs, 30)
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation marker, got: %q", out)
	}
}

func TestOutputSlackThreadContext_WithThread(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msgs := []slackThreadMessage{
			{Author: "user1", Text: "Help with deployment", Timestamp: "111.222"},
			{Author: "user2", Text: "I agree we need help", Timestamp: "111.223"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(msgs)
	}))
	defer server.Close()

	t.Setenv("SLACK_BRIDGE_URL", server.URL)
	t.Setenv("SLACK_THREAD_CHANNEL", "C999")
	t.Setenv("SLACK_THREAD_TS", "111.222")

	var buf strings.Builder
	outputSlackThreadContext(&buf)
	out := buf.String()

	if !strings.Contains(out, "## Slack Thread Context") {
		t.Errorf("expected header, got: %q", out)
	}
	if !strings.Contains(out, "You were spawned to help with this Slack thread") {
		t.Errorf("expected intro text, got: %q", out)
	}
	if !strings.Contains(out, "**user1**: Help with deployment") {
		t.Errorf("expected user1 message, got: %q", out)
	}
	if !strings.Contains(out, "**user2**: I agree we need help") {
		t.Errorf("expected user2 message, got: %q", out)
	}
	if !strings.Contains(out, "gb slack reply") {
		t.Errorf("expected reply hint, got: %q", out)
	}
	if !strings.Contains(out, "---") {
		t.Errorf("expected separator, got: %q", out)
	}
}

func TestOutputSlackThreadContext_NotThreadSpawned(t *testing.T) {
	t.Setenv("SLACK_THREAD_CHANNEL", "")
	t.Setenv("SLACK_THREAD_TS", "")

	var buf strings.Builder
	outputSlackThreadContext(&buf)
	if buf.Len() != 0 {
		t.Errorf("expected no output for non-thread agent, got: %q", buf.String())
	}
}

func TestOutputSlackThreadContext_NoBridgeURL(t *testing.T) {
	t.Setenv("SLACK_BRIDGE_URL", "")
	t.Setenv("SLACK_THREAD_CHANNEL", "C123")
	t.Setenv("SLACK_THREAD_TS", "111.222")

	var buf strings.Builder
	outputSlackThreadContext(&buf)
	if buf.Len() != 0 {
		t.Errorf("expected no output when bridge URL missing, got: %q", buf.String())
	}
}

func TestOutputSlackThreadContext_BridgeUnreachable(t *testing.T) {
	t.Setenv("SLACK_BRIDGE_URL", "http://127.0.0.1:1")
	t.Setenv("SLACK_THREAD_CHANNEL", "C123")
	t.Setenv("SLACK_THREAD_TS", "111.222")

	var buf strings.Builder
	outputSlackThreadContext(&buf)
	// Should gracefully produce no output when bridge is unreachable.
	if buf.Len() != 0 {
		t.Errorf("expected no output when bridge unreachable, got: %q", buf.String())
	}
}

func TestSlackThread_JSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msgs := []slackThreadMessage{
			{Author: "alice", Text: "Hello", Timestamp: "111.222"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(msgs)
	}))
	defer server.Close()

	t.Setenv("SLACK_BRIDGE_URL", server.URL)
	t.Setenv("SLACK_THREAD_CHANNEL", "C999")
	t.Setenv("SLACK_THREAD_TS", "111.222")

	// Set jsonOutput flag.
	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runSlackThread(slackThreadCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	// Verify it's valid JSON.
	var parsed []slackThreadMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got: %q (error: %v)", output, err)
	}
	if len(parsed) != 1 || parsed[0].Author != "alice" {
		t.Errorf("unexpected parsed result: %+v", parsed)
	}
}
