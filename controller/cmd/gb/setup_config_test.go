package main

import (
	"os"
	"testing"
)

func TestSplitRoles(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"crew", []string{"crew"}},
		{"thread,crew", []string{"thread", "crew"}},
		{"thread, crew", []string{"thread", "crew"}},
		{" thread , crew , captain ", []string{"thread", "crew", "captain"}},
		{"crew,,captain", []string{"crew", "captain"}},
	}
	for _, tc := range tests {
		got := splitRoles(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("splitRoles(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitRoles(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestBuildSubscriptions_SingleRole(t *testing.T) {
	t.Setenv("BOAT_PROJECT", "")
	t.Setenv("BOAT_RIG", "")
	subs := buildSubscriptions("crew")
	has := make(map[string]bool)
	for _, s := range subs {
		has[s] = true
	}
	if !has["global"] {
		t.Error("missing global subscription")
	}
	if !has["role:crew"] {
		t.Error("missing role:crew subscription")
	}
	if len(subs) != 2 {
		t.Errorf("expected 2 subscriptions, got %d: %v", len(subs), subs)
	}
}

func TestBuildSubscriptions_MultiRole(t *testing.T) {
	t.Setenv("BOAT_PROJECT", "")
	t.Setenv("BOAT_RIG", "")
	subs := buildSubscriptions("thread,crew")
	has := make(map[string]bool)
	for _, s := range subs {
		has[s] = true
	}
	if !has["global"] {
		t.Error("missing global subscription")
	}
	if !has["role:thread"] {
		t.Error("missing role:thread subscription")
	}
	if !has["role:crew"] {
		t.Error("missing role:crew subscription")
	}
	if len(subs) != 3 {
		t.Errorf("expected 3 subscriptions, got %d: %v", len(subs), subs)
	}
}

func TestBuildSubscriptions_Empty(t *testing.T) {
	t.Setenv("BOAT_PROJECT", "")
	t.Setenv("BOAT_RIG", "")
	subs := buildSubscriptions("")
	if len(subs) != 1 || subs[0] != "global" {
		t.Errorf("expected [global] for empty role, got %v", subs)
	}
}

func TestBuildSubscriptions_WithProject(t *testing.T) {
	t.Setenv("BOAT_PROJECT", "myproject")
	t.Setenv("BOAT_RIG", "")
	defer os.Unsetenv("BOAT_PROJECT")
	subs := buildSubscriptions("thread,crew")
	has := make(map[string]bool)
	for _, s := range subs {
		has[s] = true
	}
	if !has["project:myproject"] {
		t.Error("missing project:myproject subscription")
	}
	if !has["role:thread"] {
		t.Error("missing role:thread subscription")
	}
	if !has["role:crew"] {
		t.Error("missing role:crew subscription")
	}
}
