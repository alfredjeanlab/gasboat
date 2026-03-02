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
		_ = json.NewEncoder(w).Encode(listBeadsResponse{Beads: nil, Total: 0})
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
		_ = json.NewEncoder(w).Encode(resp)
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
		_ = json.NewEncoder(w).Encode(resp)
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
		_ = json.NewEncoder(w).Encode(resp)
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
		_ = json.NewEncoder(w).Encode(listBeadsResponse{Beads: nil, Total: 0})
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
		_ = json.NewEncoder(w).Encode(listBeadsResponse{Beads: nil, Total: 0})
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
		_ = json.NewEncoder(w).Encode(resp)
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

func TestListProjectBeads_ParsesSecretsAndRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:    "proj-pihealth",
					Title: "pihealth",
					Type:  "project",
					Fields: json.RawMessage(`{
						"prefix":"ph",
						"git_url":"https://github.com/org/pihealth",
						"secrets":"[{\"env\":\"GITHUB_TOKEN\",\"secret\":\"ph-gh-token\",\"key\":\"token\"},{\"env\":\"JIRA_API_TOKEN\",\"secret\":\"ph-jira\",\"key\":\"api-token\"}]",
						"repos":"[{\"url\":\"https://github.com/org/pihealth.git\",\"branch\":\"main\",\"role\":\"primary\"},{\"url\":\"https://github.com/org/shared-lib.git\",\"role\":\"reference\",\"name\":\"shared-lib\"}]"
					}`),
				},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := projects["pihealth"]
	if !ok {
		t.Fatal("expected project 'pihealth' in map")
	}

	// Secrets
	if len(p.Secrets) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(p.Secrets))
	}
	if p.Secrets[0].Env != "GITHUB_TOKEN" || p.Secrets[0].Secret != "ph-gh-token" || p.Secrets[0].Key != "token" {
		t.Errorf("unexpected first secret: %+v", p.Secrets[0])
	}
	if p.Secrets[1].Env != "JIRA_API_TOKEN" {
		t.Errorf("expected JIRA_API_TOKEN, got %s", p.Secrets[1].Env)
	}

	// Repos
	if len(p.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(p.Repos))
	}
	if p.Repos[0].Role != "primary" || p.Repos[0].URL != "https://github.com/org/pihealth.git" {
		t.Errorf("unexpected primary repo: %+v", p.Repos[0])
	}
	if p.Repos[1].Role != "reference" || p.Repos[1].Name != "shared-lib" {
		t.Errorf("unexpected reference repo: %+v", p.Repos[1])
	}
}

func TestListProjectBeads_EmptySecretsAndRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "proj-simple",
					Title:  "simple",
					Type:   "project",
					Fields: json.RawMessage(`{"prefix":"sm","git_url":"https://github.com/org/simple"}`),
				},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := projects["simple"]
	if len(p.Secrets) != 0 {
		t.Errorf("expected no secrets, got %d", len(p.Secrets))
	}
	if len(p.Repos) != 0 {
		t.Errorf("expected no repos, got %d", len(p.Repos))
	}
}

func TestListProjectBeads_ParsesResourceAndEnvOverrides(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:    "proj-heavy",
					Title: "heavy",
					Type:  "project",
					Fields: json.RawMessage(`{
						"prefix":"hv",
						"git_url":"https://github.com/org/heavy",
						"cpu_request":"4",
						"cpu_limit":"8",
						"memory_request":"4Gi",
						"memory_limit":"16Gi",
						"env_json":"{\"FEATURE_FLAG\":\"true\",\"API_URL\":\"https://api.example.com\"}"
					}`),
				},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := projects["heavy"]
	if !ok {
		t.Fatal("expected project 'heavy' in map")
	}

	if p.CPURequest != "4" {
		t.Errorf("expected cpu_request 4, got %s", p.CPURequest)
	}
	if p.CPULimit != "8" {
		t.Errorf("expected cpu_limit 8, got %s", p.CPULimit)
	}
	if p.MemoryRequest != "4Gi" {
		t.Errorf("expected memory_request 4Gi, got %s", p.MemoryRequest)
	}
	if p.MemoryLimit != "16Gi" {
		t.Errorf("expected memory_limit 16Gi, got %s", p.MemoryLimit)
	}

	if len(p.EnvOverrides) != 2 {
		t.Fatalf("expected 2 env overrides, got %d", len(p.EnvOverrides))
	}
	if p.EnvOverrides["FEATURE_FLAG"] != "true" {
		t.Errorf("expected FEATURE_FLAG=true, got %s", p.EnvOverrides["FEATURE_FLAG"])
	}
	if p.EnvOverrides["API_URL"] != "https://api.example.com" {
		t.Errorf("expected API_URL, got %s", p.EnvOverrides["API_URL"])
	}
}

func TestListProjectBeads_EmptyResourceAndEnvFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "proj-minimal",
					Title:  "minimal",
					Type:   "project",
					Fields: json.RawMessage(`{"prefix":"mn","git_url":"https://github.com/org/minimal"}`),
				},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := projects["minimal"]
	if p.CPURequest != "" || p.CPULimit != "" || p.MemoryRequest != "" || p.MemoryLimit != "" {
		t.Errorf("expected empty resource fields, got cpu_request=%q cpu_limit=%q memory_request=%q memory_limit=%q",
			p.CPURequest, p.CPULimit, p.MemoryRequest, p.MemoryLimit)
	}
	if len(p.EnvOverrides) != 0 {
		t.Errorf("expected no env overrides, got %d", len(p.EnvOverrides))
	}
	if len(p.EnvVars) != 0 {
		t.Errorf("expected no env vars, got %d", len(p.EnvVars))
	}
}

func TestListProjectBeads_ParsesEnvVars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:    "proj-monorepo",
					Title: "monorepo",
					Type:  "project",
					Fields: json.RawMessage(`{
						"prefix":"kd",
						"git_url":"https://github.com/org/monorepo",
						"env":"[{\"name\":\"JIRA_BASE_URL\",\"value\":\"https://pihealth.atlassian.net\"},{\"name\":\"JIRA_PROJECT\",\"value\":\"PIH\"}]"
					}`),
				},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := projects["monorepo"]
	if !ok {
		t.Fatal("expected project 'monorepo' in map")
	}

	if len(p.EnvVars) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(p.EnvVars))
	}
	if p.EnvVars[0].Name != "JIRA_BASE_URL" || p.EnvVars[0].Value != "https://pihealth.atlassian.net" {
		t.Errorf("unexpected first env var: %+v", p.EnvVars[0])
	}
	if p.EnvVars[1].Name != "JIRA_PROJECT" || p.EnvVars[1].Value != "PIH" {
		t.Errorf("unexpected second env var: %+v", p.EnvVars[1])
	}
}

func TestListProjectBeads_SecretsAndEnvVarsTogether(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:    "proj-both",
					Title: "both",
					Type:  "project",
					Fields: json.RawMessage(`{
						"prefix":"bt",
						"secrets":"[{\"env\":\"API_TOKEN\",\"secret\":\"both-api-token\",\"key\":\"token\"}]",
						"env":"[{\"name\":\"BASE_URL\",\"value\":\"https://example.com\"}]"
					}`),
				},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := projects["both"]
	if len(p.Secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(p.Secrets))
	}
	if p.Secrets[0].Env != "API_TOKEN" {
		t.Errorf("expected API_TOKEN secret, got %s", p.Secrets[0].Env)
	}
	if len(p.EnvVars) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(p.EnvVars))
	}
	if p.EnvVars[0].Name != "BASE_URL" || p.EnvVars[0].Value != "https://example.com" {
		t.Errorf("unexpected env var: %+v", p.EnvVars[0])
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
		_ = json.NewEncoder(w).Encode(resp)
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


func TestListProjectBeads_Tier1Fields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:    "proj-full",
					Title: "full",
					Type:  "project",
					Fields: json.RawMessage(`{
						"prefix":"kd",
						"cpu_request":"500m",
						"cpu_limit":"2000m",
						"memory_request":"512Mi",
						"memory_limit":"2Gi",
						"service_account":"my-sa",
						"env_json":"{\"FOO\":\"bar\",\"BAZ\":\"qux\"}"
					}`),
				},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := projects["full"]
	if !ok {
		t.Fatal("expected project 'full' in map")
	}
	if p.CPURequest != "500m" {
		t.Errorf("cpu_request: got %q, want %q", p.CPURequest, "500m")
	}
	if p.CPULimit != "2000m" {
		t.Errorf("cpu_limit: got %q, want %q", p.CPULimit, "2000m")
	}
	if p.MemoryRequest != "512Mi" {
		t.Errorf("memory_request: got %q, want %q", p.MemoryRequest, "512Mi")
	}
	if p.MemoryLimit != "2Gi" {
		t.Errorf("memory_limit: got %q, want %q", p.MemoryLimit, "2Gi")
	}
	if p.ServiceAccount != "my-sa" {
		t.Errorf("service_account: got %q, want %q", p.ServiceAccount, "my-sa")
	}
	if p.EnvOverrides["FOO"] != "bar" {
		t.Errorf("env_json FOO: got %q, want %q", p.EnvOverrides["FOO"], "bar")
	}
	if p.EnvOverrides["BAZ"] != "qux" {
		t.Errorf("env_json BAZ: got %q, want %q", p.EnvOverrides["BAZ"], "qux")
	}
}

func TestListProjectBeads_MalformedEnvJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "proj-bad-env",
					Title:  "bad-env",
					Type:   "project",
					Fields: json.RawMessage(`{"prefix":"kd","env_json":"not-valid-json"}`),
				},
			},
			Total: 1,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Project is still returned, just without env overrides.
	p, ok := projects["bad-env"]
	if !ok {
		t.Fatal("expected project 'bad-env' in map despite malformed env_json")
	}
	if len(p.EnvOverrides) != 0 {
		t.Errorf("expected no EnvOverrides for malformed env_json, got %v", p.EnvOverrides)
	}
}
