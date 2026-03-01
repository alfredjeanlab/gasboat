package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// writeJSON encodes v as JSON to w, panicking on error (test-only helper).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic("writeJSON: " + err.Error())
	}
}

// newTestGitHubClient creates a GitHubClient pointing at a test server.
func newTestGitHubClient(baseURL, token string) *GitHubClient {
	return &GitHubClient{
		httpClient: http.DefaultClient,
		baseURL:    baseURL,
		token:      token,
		logger:     slog.Default(),
	}
}

func TestGetLatestTag_CalverPreferredOverSemver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/tags" {
			http.NotFound(w, r)
			return
		}
		// GitHub returns these in commit-date order which may put old
		// semver tags first — calver sort should pick 2026.60.2.
		writeJSON(w, []ghTag{
			{Name: "v0.3.14"},
			{Name: "v2026.58.11"},
			{Name: "2026.60.2"},
			{Name: "2026.59.4"},
			{Name: "v0.1.0"},
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	tag, err := client.GetLatestTag(context.Background(), RepoRef{Owner: "org", Repo: "repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "2026.60.2" {
		t.Errorf("got tag %q, want 2026.60.2", tag)
	}
}

func TestGetLatestTag_MixedVPrefixCalver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/tags" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, []ghTag{
			{Name: "v2026.58.11"},
			{Name: "2026.60.2"},
			{Name: "v2026.60.3"},
			{Name: "2026.59.4"},
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	tag, err := client.GetLatestTag(context.Background(), RepoRef{Owner: "org", Repo: "repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v2026.60.3" {
		t.Errorf("got tag %q, want v2026.60.3", tag)
	}
}

func TestGetLatestTag_FallbackNoCalver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/tags" {
			http.NotFound(w, r)
			return
		}
		// No calver tags — should fall back to the first tag.
		writeJSON(w, []ghTag{
			{Name: "v1.2.3"},
			{Name: "v0.9.0"},
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	tag, err := client.GetLatestTag(context.Background(), RepoRef{Owner: "org", Repo: "repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v1.2.3" {
		t.Errorf("got tag %q, want v1.2.3", tag)
	}
}

func TestGetLatestTag_NoTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []ghTag{})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	_, err := client.GetLatestTag(context.Background(), RepoRef{Owner: "org", Repo: "repo"})
	if err == nil {
		t.Fatal("expected error for no tags")
	}
}

func TestCompareTagToHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/compare/v1.0.0...main" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, ghCompare{
			AheadBy: 3,
			Commits: []struct {
				SHA    string `json:"sha"`
				Commit struct {
					Message string `json:"message"`
					Author  struct {
						Name string `json:"name"`
					} `json:"author"`
				} `json:"commit"`
			}{
				{SHA: "abc1234567890", Commit: struct {
					Message string `json:"message"`
					Author  struct {
						Name string `json:"name"`
					} `json:"author"`
				}{Message: "fix: bug", Author: struct {
					Name string `json:"name"`
				}{Name: "Alice"}}},
			},
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	cmp, err := client.CompareTagToHead(context.Background(), RepoRef{Owner: "org", Repo: "repo"}, "v1.0.0", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmp.AheadBy != 3 {
		t.Errorf("got AheadBy=%d, want 3", cmp.AheadBy)
	}
	if len(cmp.Commits) != 1 {
		t.Errorf("got %d commits, want 1", len(cmp.Commits))
	}
}

func TestCompareTagToHead_Identical(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, ghCompare{AheadBy: 0})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	cmp, err := client.CompareTagToHead(context.Background(), RepoRef{Owner: "org", Repo: "repo"}, "v1.0.0", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmp.AheadBy != 0 {
		t.Errorf("got AheadBy=%d, want 0", cmp.AheadBy)
	}
}

func TestGetUnreleased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/tags":
			writeJSON(w, []ghTag{{Name: "v2.0.0"}})
		case "/repos/org/repo/compare/v2.0.0...main":
			writeJSON(w, ghCompare{
				AheadBy: 2,
				Commits: []struct {
					SHA    string `json:"sha"`
					Commit struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					} `json:"commit"`
				}{
					{SHA: "aaa1111", Commit: struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					}{Message: "feat: new thing", Author: struct {
						Name string `json:"name"`
					}{Name: "Bob"}}},
					{SHA: "bbb2222", Commit: struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					}{Message: "fix: old thing", Author: struct {
						Name string `json:"name"`
					}{Name: "Carol"}}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "")
	result := client.GetUnreleased(context.Background(), RepoRef{Owner: "org", Repo: "repo"}, "main")

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.LatestTag != "v2.0.0" {
		t.Errorf("got tag %q, want v2.0.0", result.LatestTag)
	}
	if result.AheadBy != 2 {
		t.Errorf("got AheadBy=%d, want 2", result.AheadBy)
	}
	if len(result.Commits) != 2 {
		t.Errorf("got %d commits, want 2", len(result.Commits))
	}
}

func TestAuthHeader(t *testing.T) {
	t.Run("with token", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			writeJSON(w, []ghTag{{Name: "v1.0.0"}})
		}))
		defer srv.Close()

		client := newTestGitHubClient(srv.URL, "ghp_secret123")
		_, _ = client.GetLatestTag(context.Background(), RepoRef{Owner: "o", Repo: "r"})

		if gotAuth != "Bearer ghp_secret123" {
			t.Errorf("got Authorization=%q, want Bearer ghp_secret123", gotAuth)
		}
	})

	t.Run("without token", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			writeJSON(w, []ghTag{{Name: "v1.0.0"}})
		}))
		defer srv.Close()

		client := newTestGitHubClient(srv.URL, "")
		_, _ = client.GetLatestTag(context.Background(), RepoRef{Owner: "o", Repo: "r"})

		if gotAuth != "" {
			t.Errorf("got Authorization=%q, want empty", gotAuth)
		}
	})
}

func TestCalverKey(t *testing.T) {
	tests := []struct {
		tag              string
		wantOK           bool
		wantY, wantD, wantB int
	}{
		{"2026.60.2", true, 2026, 60, 2},
		{"v2026.58.11", true, 2026, 58, 11},
		{"v0.3.14", false, 0, 0, 0},
		{"not-a-tag", false, 0, 0, 0},
		{"v1.2", false, 0, 0, 0},
		{"latest", false, 0, 0, 0},
	}
	for _, tc := range tests {
		y, d, b, ok := calverKey(tc.tag)
		if ok != tc.wantOK {
			t.Errorf("calverKey(%q) ok=%v, want %v", tc.tag, ok, tc.wantOK)
			continue
		}
		if ok && (y != tc.wantY || d != tc.wantD || b != tc.wantB) {
			t.Errorf("calverKey(%q) = (%d,%d,%d), want (%d,%d,%d)", tc.tag, y, d, b, tc.wantY, tc.wantD, tc.wantB)
		}
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("abc1234567890"); got != "abc1234" {
		t.Errorf("shortSHA(long) = %q, want abc1234", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Errorf("shortSHA(short) = %q, want abc", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("line1\nline2"); got != "line1" {
		t.Errorf("firstLine(multi) = %q, want line1", got)
	}
	if got := firstLine("single"); got != "single" {
		t.Errorf("firstLine(single) = %q, want single", got)
	}
}
