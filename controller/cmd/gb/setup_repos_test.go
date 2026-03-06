package main

import (
	"testing"
)

func TestParseBoatProjectsEnv(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []repoCloneEntry
	}{
		{
			name: "empty",
			raw:  "",
			want: nil,
		},
		{
			name: "single entry with prefix",
			raw:  "gasboat=https://github.com/groblegark/gasboat.git:kd",
			want: []repoCloneEntry{{Name: "gasboat", URL: "https://github.com/groblegark/gasboat.git"}},
		},
		{
			name: "two entries",
			raw:  "gasboat=https://github.com/groblegark/gasboat.git:kd,monorepo=https://gitlab.com/PiHealth/CoreFICS/monorepo:PE",
			want: []repoCloneEntry{
				{Name: "gasboat", URL: "https://github.com/groblegark/gasboat.git"},
				{Name: "monorepo", URL: "https://gitlab.com/PiHealth/CoreFICS/monorepo"},
			},
		},
		{
			name: "no prefix",
			raw:  "myrepo=https://github.com/org/repo",
			want: []repoCloneEntry{{Name: "myrepo", URL: "https://github.com/org/repo"}},
		},
		{
			name: "spaces around entries",
			raw:  " foo=https://host/foo:x , bar=https://host/bar:y ",
			want: []repoCloneEntry{
				{Name: "foo", URL: "https://host/foo"},
				{Name: "bar", URL: "https://host/bar"},
			},
		},
		{
			name: "malformed entry skipped",
			raw:  "noeq,good=https://host/good:x",
			want: []repoCloneEntry{{Name: "good", URL: "https://host/good"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBoatProjectsEnv(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d, want %d\n  got:  %+v\n  want: %+v", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry[%d]: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/org/my-repo.git", "my-repo"},
		{"https://gitlab.com/PiHealth/CoreFICS/monorepo", "monorepo"},
		{"https://github.com/org/repo", "repo"},
	}
	for _, tc := range tests {
		got := repoNameFromURL(tc.url)
		if got != tc.want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}
