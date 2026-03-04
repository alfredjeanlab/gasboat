package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestJiraSync_MRLink(t *testing.T) {
	var (
		mu           sync.Mutex
		commentAdded bool
		linkAdded    bool
		commentBody  string
		linkURL      string
	)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-7001/comment":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if doc, ok := body["body"].(map[string]any); ok {
				if content, ok := doc["content"].([]any); ok && len(content) > 0 {
					if para, ok := content[0].(map[string]any); ok {
						if pc, ok := para["content"].([]any); ok && len(pc) > 0 {
							if tn, ok := pc[0].(map[string]any); ok {
								commentBody, _ = tn["text"].(string)
							}
						}
					}
				}
			}
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-7001/remotelink":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if obj, ok := body["object"].(map[string]any); ok {
				linkURL, _ = obj["url"].(string)
			}
			linkAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient(jiraServer.URL), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-1", Type: "task", Title: "[PE-7001] Fix upload error",
		Labels: []string{"source:jira", "jira:PE-7001"},
		Fields: map[string]string{"jira_key": "PE-7001", "mr_url": "https://gitlab.com/PiHealth/CoreFICS/monorepo/-/merge_requests/123"},
	})
	s.handleUpdated(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if !commentAdded {
		t.Fatal("expected JIRA comment")
	}
	if commentBody != "Automated MR created: https://gitlab.com/PiHealth/CoreFICS/monorepo/-/merge_requests/123" {
		t.Errorf("comment body: %s", commentBody)
	}
	if !linkAdded {
		t.Fatal("expected JIRA remote link")
	}
	if linkURL != "https://gitlab.com/PiHealth/CoreFICS/monorepo/-/merge_requests/123" {
		t.Errorf("link URL: %s", linkURL)
	}
}

func TestJiraSync_SkipNonJira(t *testing.T) {
	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient("https://example.com"), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-2", Type: "task", Title: "Regular task",
		Labels: []string{"source:manual"},
		Fields: map[string]string{"mr_url": "https://gitlab.com/example/mr/1"},
	})
	s.handleUpdated(context.Background(), event)
}

func TestJiraSync_Closed(t *testing.T) {
	var (
		mu              sync.Mutex
		commentAdded    bool
		transitionCalls int
	)
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/DEVOPS-42/comment":
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/DEVOPS-42/transitions":
			resp := map[string]any{"transitions": []map[string]any{
				{"id": "31", "name": "In Progress", "to": map[string]string{"name": "In Progress"}},
				{"id": "41", "name": "Review", "to": map[string]string{"name": "Review"}},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/DEVOPS-42/transitions":
			transitionCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient(jiraServer.URL), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-3", Type: "task", Title: "[DEVOPS-42] Fix CI pipeline",
		Labels: []string{"source:jira", "jira:DEVOPS-42"},
		Fields: map[string]string{"jira_key": "DEVOPS-42"},
	})
	s.handleClosed(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if !commentAdded {
		t.Fatal("expected JIRA closing comment")
	}
	// Transitions are no longer triggered on close — they fire on mr_merged=true.
	if transitionCalls != 0 {
		t.Errorf("expected 0 transition calls on close, got %d", transitionCalls)
	}
}

func TestJiraSync_MRMerged(t *testing.T) {
	var (
		mu              sync.Mutex
		commentAdded    bool
		commentBody     string
		transitionCalls int
	)
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-100/comment":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if doc, ok := body["body"].(map[string]any); ok {
				if content, ok := doc["content"].([]any); ok && len(content) > 0 {
					if para, ok := content[0].(map[string]any); ok {
						if pc, ok := para["content"].([]any); ok && len(pc) > 0 {
							if tn, ok := pc[0].(map[string]any); ok {
								commentBody, _ = tn["text"].(string)
							}
						}
					}
				}
			}
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PE-100/transitions":
			resp := map[string]any{"transitions": []map[string]any{
				{"id": "31", "name": "In Progress", "to": map[string]string{"name": "In Progress"}},
				{"id": "41", "name": "Review", "to": map[string]string{"name": "Review"}},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-100/transitions":
			transitionCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient(jiraServer.URL), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-5", Type: "task", Title: "[PE-100] Fix login bug",
		Labels: []string{"source:jira", "jira:PE-100"},
		Fields: map[string]string{"jira_key": "PE-100", "mr_merged": "true"},
	})
	s.handleUpdated(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if !commentAdded {
		t.Fatal("expected JIRA MR merged comment")
	}
	if commentBody != "MR merged — transitioning to Review." {
		t.Errorf("comment body: %s", commentBody)
	}
	if transitionCalls != 1 {
		t.Errorf("expected 1 transition call, got %d", transitionCalls)
	}
}

func TestJiraSync_MRMerged_TransitionsDisabled(t *testing.T) {
	var (
		mu              sync.Mutex
		commentAdded    bool
		transitionCalls int
	)
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-200/comment":
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PE-200/transitions":
			resp := map[string]any{"transitions": []map[string]any{
				{"id": "41", "name": "Review", "to": map[string]string{"name": "Review"}},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-200/transitions":
			transitionCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{
		Jira:               newTestJiraClient(jiraServer.URL),
		Logger:             slog.Default(),
		DisableTransitions: true,
	})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-6", Type: "task", Title: "[PE-200] Fix upload",
		Labels: []string{"source:jira", "jira:PE-200"},
		Fields: map[string]string{"jira_key": "PE-200", "mr_merged": "true"},
	})
	s.handleUpdated(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if !commentAdded {
		t.Fatal("expected JIRA MR merged comment even with transitions disabled")
	}
	if transitionCalls != 0 {
		t.Errorf("expected 0 transition calls with DisableTransitions, got %d", transitionCalls)
	}
}

func TestJiraSync_Claimed(t *testing.T) {
	var (
		mu              sync.Mutex
		commentAdded    bool
		commentBody     string
		labelAdded      bool
		transitionCalls int
		transitionName  string
	)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-500/comment":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if doc, ok := body["body"].(map[string]any); ok {
				if content, ok := doc["content"].([]any); ok && len(content) > 0 {
					if para, ok := content[0].(map[string]any); ok {
						if pc, ok := para["content"].([]any); ok && len(pc) > 0 {
							if tn, ok := pc[0].(map[string]any); ok {
								commentBody, _ = tn["text"].(string)
							}
						}
					}
				}
			}
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "PUT" && r.URL.Path == "/rest/api/3/issue/PE-500":
			labelAdded = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PE-500/transitions":
			resp := map[string]any{"transitions": []map[string]any{
				{"id": "21", "name": "In Progress", "to": map[string]string{"name": "In Progress"}},
				{"id": "41", "name": "Review", "to": map[string]string{"name": "Review"}},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-500/transitions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if t, ok := body["transition"].(map[string]any); ok {
				transitionName, _ = t["id"].(string)
			}
			transitionCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient(jiraServer.URL), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-10", Type: "task", Title: "[PE-500] Fix rendering bug",
		Status:   "in_progress",
		Assignee: "agent-worker-1",
		Labels:   []string{"source:jira", "jira:PE-500"},
		Fields:   map[string]string{"jira_key": "PE-500"},
	})
	s.handleUpdated(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if !commentAdded {
		t.Fatal("expected JIRA claim comment")
	}
	if commentBody != "Gasboat agent agent-worker-1 is working on this issue." {
		t.Errorf("comment body: %s", commentBody)
	}
	if !labelAdded {
		t.Fatal("expected gasboat label to be added")
	}
	if transitionCalls != 1 {
		t.Errorf("expected 1 transition call (In Progress), got %d", transitionCalls)
	}
	if transitionName != "21" {
		t.Errorf("expected transition ID 21 (In Progress), got %s", transitionName)
	}
}

func TestJiraSync_Claimed_WithBotAssignment(t *testing.T) {
	var (
		mu         sync.Mutex
		assigned   bool
		assigneeID string
	)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-600/comment":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "PUT" && r.URL.Path == "/rest/api/3/issue/PE-600":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PE-600/transitions":
			resp := map[string]any{"transitions": []map[string]any{
				{"id": "21", "name": "In Progress", "to": map[string]string{"name": "In Progress"}},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-600/transitions":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "PUT" && r.URL.Path == "/rest/api/3/issue/PE-600/assignee":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			assigneeID, _ = body["accountId"].(string)
			assigned = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{
		Jira:         newTestJiraClient(jiraServer.URL),
		Logger:       slog.Default(),
		BotAccountID: "5f1234567890abcdef012345",
	})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-11", Type: "task", Title: "[PE-600] Fix API timeout",
		Status:   "in_progress",
		Assignee: "agent-worker-2",
		Labels:   []string{"source:jira", "jira:PE-600"},
		Fields:   map[string]string{"jira_key": "PE-600"},
	})
	s.handleUpdated(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if !assigned {
		t.Fatal("expected JIRA issue to be assigned to bot account")
	}
	if assigneeID != "5f1234567890abcdef012345" {
		t.Errorf("assignee account ID: %s", assigneeID)
	}
}

func TestJiraSync_Claimed_ClaimTransitionOverride(t *testing.T) {
	var (
		mu              sync.Mutex
		transitionCalls int
		transitionName  string
	)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-800/comment":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "PUT" && r.URL.Path == "/rest/api/3/issue/PE-800":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PE-800/transitions":
			resp := map[string]any{"transitions": []map[string]any{
				{"id": "21", "name": "In Progress", "to": map[string]string{"name": "In Progress"}},
				{"id": "41", "name": "Review", "to": map[string]string{"name": "Review"}},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-800/transitions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if t, ok := body["transition"].(map[string]any); ok {
				transitionName, _ = t["id"].(string)
			}
			transitionCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	// DisableTransitions=true but EnableClaimTransition=true → claim transition should fire.
	s := NewJiraSync(JiraSyncConfig{
		Jira:                  newTestJiraClient(jiraServer.URL),
		Logger:                slog.Default(),
		DisableTransitions:    true,
		EnableClaimTransition: true,
	})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-13", Type: "task", Title: "[PE-800] Fix dashboard",
		Status:   "in_progress",
		Assignee: "agent-worker-4",
		Labels:   []string{"source:jira", "jira:PE-800"},
		Fields:   map[string]string{"jira_key": "PE-800"},
	})
	s.handleUpdated(context.Background(), event)

	mu.Lock()
	if transitionCalls != 1 {
		t.Errorf("expected 1 transition call (In Progress) with EnableClaimTransition override, got %d", transitionCalls)
	}
	if transitionName != "21" {
		t.Errorf("expected transition ID 21 (In Progress), got %s", transitionName)
	}
	mu.Unlock()

	// Verify Review transition is still blocked — send mr_merged event.
	// Must release lock before calling handleUpdated since the mock handler needs it.
	s.handleUpdated(context.Background(), marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-14", Type: "task", Title: "[PE-800] Fix dashboard",
		Labels: []string{"source:jira", "jira:PE-800"},
		Fields: map[string]string{"jira_key": "PE-800", "mr_merged": "true"},
	}))

	mu.Lock()
	defer mu.Unlock()
	// transitionCalls should still be 1 (Review blocked by DisableTransitions).
	if transitionCalls != 1 {
		t.Errorf("expected Review transition to be blocked (DisableTransitions=true), got %d total transition calls", transitionCalls)
	}
}

func TestJiraSync_Claimed_Dedup(t *testing.T) {
	var (
		mu           sync.Mutex
		commentCount int
	)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-700/comment":
			commentCount++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1"}`)
		case r.Method == "PUT" && r.URL.Path == "/rest/api/3/issue/PE-700":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && r.URL.Path == "/rest/api/3/issue/PE-700/transitions":
			resp := map[string]any{"transitions": []map[string]any{
				{"id": "21", "name": "In Progress", "to": map[string]string{"name": "In Progress"}},
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue/PE-700/transitions":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jiraServer.Close()

	s := NewJiraSync(JiraSyncConfig{Jira: newTestJiraClient(jiraServer.URL), Logger: slog.Default()})
	event := marshalSSEBeadPayload(BeadEvent{
		ID: "bd-task-12", Type: "task", Title: "[PE-700] Fix search",
		Status:   "in_progress",
		Assignee: "agent-worker-3",
		Labels:   []string{"source:jira", "jira:PE-700"},
		Fields:   map[string]string{"jira_key": "PE-700"},
	})

	// First call should post comment.
	s.handleUpdated(context.Background(), event)
	// Second call (same assignee) should be deduped.
	s.handleUpdated(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()
	if commentCount != 1 {
		t.Errorf("expected 1 comment (dedup), got %d", commentCount)
	}
}

func TestAdfToMarkdown(t *testing.T) {
	tests := []struct {
		name, input, expected string
	}{
		{"empty", "", ""},
		{"paragraph", `{"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]}]}`, "Hello world"},
		{"heading", `{"version":1,"type":"doc","content":[{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]}]}`, "## Title"},
		{"bullet list", `{"version":1,"type":"doc","content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Item A"}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Item B"}]}]}]}]}`, "- Item A\n- Item B"},
		{"code block", `{"version":1,"type":"doc","content":[{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println()"}]}]}`, "```go\nfmt.Println()\n```"},
		{"bold+italic", `{"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"bold","marks":[{"type":"strong"}]},{"type":"text","text":" and "},{"type":"text","text":"italic","marks":[{"type":"em"}]}]}]}`, "**bold** and _italic_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := adfToMarkdown(json.RawMessage(tt.input)); got != tt.expected {
				t.Errorf("adfToMarkdown():\n  got:  %q\n  want: %q", got, tt.expected)
			}
		})
	}
}
