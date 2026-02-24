package beadsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- ListAgentBeads tests ---

func TestListAgentBeads_QueryParams(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		json.NewEncoder(w).Encode(listBeadsResponse{Beads: nil, Total: 0})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.ListAgentBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The query should include type=agent and all active statuses.
	if !strings.Contains(gotPath, "type=agent") {
		t.Errorf("expected type=agent in query, got %s", gotPath)
	}
	// activeStatuses are joined with comma via url.Values.Set.
	for _, s := range activeStatuses {
		if !strings.Contains(gotPath, s) {
			t.Errorf("expected status %q in query, got %s", s, gotPath)
		}
	}
}

func TestListAgentBeads_ParsesBeads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "crew-town-crew-hq",
					Title:  "Agent: hq",
					Type:   "agent",
					Status: "open",
					Notes:  "coop_url: http://coop:9090\npod_name: agent-hq-0",
					Fields: json.RawMessage(`{"project":"town","mode":"crew","role":"crew","agent":"hq"}`),
				},
				{
					ID:     "crew-gasboat-crew-k8s",
					Title:  "Agent: k8s",
					Type:   "agent",
					Status: "in_progress",
					Notes:  "",
					Fields: json.RawMessage(`{"project":"gasboat","role":"ops","agent":"k8s"}`),
				},
			},
			Total: 2,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	beads, err := c.ListAgentBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(beads) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(beads))
	}

	// First bead.
	b0 := beads[0]
	if b0.ID != "crew-town-crew-hq" {
		t.Errorf("expected ID crew-town-crew-hq, got %s", b0.ID)
	}
	if b0.Project != "town" {
		t.Errorf("expected project town, got %s", b0.Project)
	}
	if b0.Mode != "crew" {
		t.Errorf("expected mode crew, got %s", b0.Mode)
	}
	if b0.Role != "crew" {
		t.Errorf("expected role crew, got %s", b0.Role)
	}
	if b0.AgentName != "hq" {
		t.Errorf("expected agent name hq, got %s", b0.AgentName)
	}
	if b0.Metadata["coop_url"] != "http://coop:9090" {
		t.Errorf("expected coop_url metadata, got %v", b0.Metadata)
	}
	if b0.Metadata["pod_name"] != "agent-hq-0" {
		t.Errorf("expected pod_name metadata, got %v", b0.Metadata)
	}

	// Second bead -- mode defaults to "crew" when empty.
	b1 := beads[1]
	if b1.Mode != "crew" {
		t.Errorf("expected default mode crew, got %s", b1.Mode)
	}
	if b1.Role != "ops" {
		t.Errorf("expected role ops, got %s", b1.Role)
	}
}

func TestListAgentBeads_SkipsBeadsMissingRoleOrAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "missing-role",
					Fields: json.RawMessage(`{"project":"x","agent":"y"}`),
				},
				{
					ID:     "missing-agent",
					Fields: json.RawMessage(`{"project":"x","role":"y"}`),
				},
				{
					ID:     "has-both",
					Fields: json.RawMessage(`{"role":"crew","agent":"hq"}`),
				},
			},
			Total: 3,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	beads, err := c.ListAgentBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead (missing role/agent should be skipped), got %d", len(beads))
	}
	if beads[0].ID != "has-both" {
		t.Errorf("expected has-both, got %s", beads[0].ID)
	}
}

// --- FindAgentBead tests ---

func TestFindAgentBead_FindsByAgentField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "kd-abc",
					Title:  "Agent: hq",
					Type:   "agent",
					Status: "open",
					Notes:  "coop_url: http://coop:9090",
					Fields: json.RawMessage(`{"role":"crew","agent":"hq"}`),
				},
				{
					ID:     "kd-def",
					Title:  "Agent: test-bot-2",
					Type:   "agent",
					Status: "open",
					Notes:  "coop_url: http://coop2:9090",
					Fields: json.RawMessage(`{"role":"crew","agent":"test-bot-2"}`),
				},
			},
			Total: 2,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	detail, err := c.FindAgentBead(context.Background(), "test-bot-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.ID != "kd-def" {
		t.Errorf("expected ID kd-def, got %s", detail.ID)
	}
	if detail.Notes != "coop_url: http://coop2:9090" {
		t.Errorf("expected notes with coop_url, got %s", detail.Notes)
	}
}

func TestFindAgentBead_ReturnsErrorWhenNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(listBeadsResponse{Beads: nil, Total: 0})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.FindAgentBead(context.Background(), "nobody")
	if err == nil {
		t.Fatal("expected error when agent not found")
	}
	if !strings.Contains(err.Error(), "agent bead not found") {
		t.Errorf("expected 'agent bead not found' error, got: %v", err)
	}
}

// --- ListProjectBeads tests ---

func TestListProjectBeads_QueryParams(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		json.NewEncoder(w).Encode(listBeadsResponse{Beads: nil, Total: 0})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(gotPath, "type=project") {
		t.Errorf("expected type=project in query, got %s", gotPath)
	}
}

func TestListProjectBeads_ParsesProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "proj-beads",
					Title:  "Project: beads",
					Type:   "project",
					Status: "open",
					Fields: json.RawMessage(`{"prefix":"bd","git_url":"https://github.com/org/beads","default_branch":"main","image":"ghcr.io/org/beads:latest","storage_class":"gp3"}`),
				},
				{
					ID:     "proj-gasboat",
					Title:  "gasboat",
					Type:   "project",
					Status: "open",
					Fields: json.RawMessage(`{"prefix":"kd","git_url":"https://github.com/org/gasboat"}`),
				},
			},
			Total: 2,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}

	// First project -- "Project: " prefix should be stripped.
	p1, ok := projects["beads"]
	if !ok {
		t.Fatal("expected project 'beads' in map")
	}
	if p1.Name != "beads" {
		t.Errorf("expected name beads, got %s", p1.Name)
	}
	if p1.Prefix != "bd" {
		t.Errorf("expected prefix bd, got %s", p1.Prefix)
	}
	if p1.GitURL != "https://github.com/org/beads" {
		t.Errorf("expected git_url, got %s", p1.GitURL)
	}
	if p1.DefaultBranch != "main" {
		t.Errorf("expected default_branch main, got %s", p1.DefaultBranch)
	}
	if p1.Image != "ghcr.io/org/beads:latest" {
		t.Errorf("expected image, got %s", p1.Image)
	}
	if p1.StorageClass != "gp3" {
		t.Errorf("expected storage_class gp3, got %s", p1.StorageClass)
	}

	// Second project -- title without "Project: " prefix.
	p2, ok := projects["gasboat"]
	if !ok {
		t.Fatal("expected project 'gasboat' in map")
	}
	if p2.Name != "gasboat" {
		t.Errorf("expected name gasboat, got %s", p2.Name)
	}
	if p2.Prefix != "kd" {
		t.Errorf("expected prefix kd, got %s", p2.Prefix)
	}
	// Optional fields should be empty string when missing.
	if p2.DefaultBranch != "" {
		t.Errorf("expected empty default_branch, got %s", p2.DefaultBranch)
	}
}

func TestListProjectBeads_SkipsEmptyTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "empty-title",
					Title:  "",
					Fields: json.RawMessage(`{"prefix":"x"}`),
				},
			},
			Total: 1,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects for empty title, got %d", len(projects))
	}
}
