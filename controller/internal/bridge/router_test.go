package bridge

import (
	"strings"
	"testing"
)

func TestRouter_Resolve_Default(t *testing.T) {
	r := NewRouter(RouterConfig{DefaultChannel: "C-default"})
	result := r.Resolve("gasboat/crew/test-bot")
	if result.ChannelID != "C-default" {
		t.Errorf("got %q, want C-default", result.ChannelID)
	}
	if !result.IsDefault {
		t.Error("expected IsDefault=true")
	}
}

func TestRouter_Resolve_ExactPattern(t *testing.T) {
	r := NewRouter(RouterConfig{
		DefaultChannel: "C-default",
		Channels: map[string]string{
			"gasboat/crew/test-bot": "C-exact",
		},
	})
	result := r.Resolve("gasboat/crew/test-bot")
	if result.ChannelID != "C-exact" {
		t.Errorf("got %q, want C-exact", result.ChannelID)
	}
}

func TestRouter_Resolve_WildcardPattern(t *testing.T) {
	r := NewRouter(RouterConfig{
		DefaultChannel: "C-default",
		Channels: map[string]string{
			"gasboat/crew/*": "C-crew",
		},
	})
	result := r.Resolve("gasboat/crew/test-bot")
	if result.ChannelID != "C-crew" {
		t.Errorf("got %q, want C-crew", result.ChannelID)
	}
}

func TestRouter_Resolve_OverrideTakesPrecedence(t *testing.T) {
	r := NewRouter(RouterConfig{
		DefaultChannel: "C-default",
		Channels: map[string]string{
			"gasboat/crew/*": "C-crew",
		},
		Overrides: map[string]string{
			"gasboat/crew/test-bot": "C-breakout",
		},
	})
	result := r.Resolve("gasboat/crew/test-bot")
	if result.ChannelID != "C-breakout" {
		t.Errorf("got %q, want C-breakout (override)", result.ChannelID)
	}
	if result.MatchedBy != "(override)" {
		t.Errorf("got matchedBy %q, want (override)", result.MatchedBy)
	}
}

func TestRouter_Resolve_SpecificityOrdering(t *testing.T) {
	r := NewRouter(RouterConfig{
		DefaultChannel: "C-default",
		Channels: map[string]string{
			"*/*":            "C-catchall",
			"gasboat/*":      "C-gasboat",
			"gasboat/crew/*": "C-crew",
		},
	})

	// 3-segment agent should match the most specific 3-segment pattern.
	result := r.Resolve("gasboat/crew/bot")
	if result.ChannelID != "C-crew" {
		t.Errorf("got %q, want C-crew (3-segment match)", result.ChannelID)
	}

	// 2-segment agent should match "gasboat/*" over "*/*".
	result = r.Resolve("gasboat/standalone")
	if result.ChannelID != "C-gasboat" {
		t.Errorf("got %q, want C-gasboat (2-segment match)", result.ChannelID)
	}

	// Unmatched segment count falls to default.
	result = r.Resolve("other/project/agent/extra")
	if result.ChannelID != "C-default" {
		t.Errorf("got %q, want C-default (no match)", result.ChannelID)
	}
}

func TestRouter_OverrideCRUD(t *testing.T) {
	r := NewRouter(RouterConfig{DefaultChannel: "C-default"})

	if r.HasOverride("agent-a") {
		t.Error("expected no override initially")
	}

	r.AddOverride("agent-a", "C-breakout")
	if !r.HasOverride("agent-a") {
		t.Error("expected override after add")
	}

	result := r.Resolve("agent-a")
	if result.ChannelID != "C-breakout" {
		t.Errorf("got %q, want C-breakout", result.ChannelID)
	}

	if got := r.GetAgentByChannel("C-breakout"); got != "agent-a" {
		t.Errorf("reverse lookup got %q, want agent-a", got)
	}

	r.RemoveOverride("agent-a")
	if r.HasOverride("agent-a") {
		t.Error("expected no override after remove")
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		agent   string
		want    bool
	}{
		{"gasboat/crew/*", "gasboat/crew/bot", true},
		{"gasboat/crew/*", "gasboat/crew/other", true},
		{"gasboat/crew/*", "beads/crew/bot", false},
		{"*/crew/*", "gasboat/crew/bot", true},
		{"*/crew/*", "beads/crew/bot", true},
		{"*/*", "gasboat/crew", true},
		{"*/*", "gasboat/crew/bot", false}, // wrong segment count
		{"gasboat/crew/bot", "gasboat/crew/bot", true},
		{"gasboat/crew/bot", "gasboat/crew/other", false},
	}
	for _, tt := range tests {
		got := matchPattern(strings.Split(tt.pattern, "/"), strings.Split(tt.agent, "/"))
		if got != tt.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.agent, got, tt.want)
		}
	}
}
