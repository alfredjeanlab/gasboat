package advice

import (
	"testing"
)

func TestMatchesSubscriptions_Global(t *testing.T) {
	labels := []string{"global"}
	subs := []string{"global", "agent:test/crew/bot"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("global advice should match any agent with global subscription")
	}
}

func TestMatchesSubscriptions_RoleMatch(t *testing.T) {
	labels := []string{"role:crew"}
	subs := []string{"global", "role:crew", "agent:gasboat/crews/bot"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("role:crew advice should match agent with role:crew subscription")
	}
}

func TestMatchesSubscriptions_RoleMismatch(t *testing.T) {
	labels := []string{"role:lead"}
	subs := []string{"global", "role:crew", "agent:gasboat/crews/bot"}
	if MatchesSubscriptions(labels, subs) {
		t.Error("role:lead advice should not match agent with only role:crew subscription")
	}
}

func TestMatchesSubscriptions_ProjectRequired(t *testing.T) {
	labels := []string{"project:gasboat", "role:crew"}
	subs := []string{"global", "project:other", "role:crew"}
	if MatchesSubscriptions(labels, subs) {
		t.Error("advice with project:gasboat should not match agent with project:other")
	}
}

func TestMatchesSubscriptions_RigBackwardCompat(t *testing.T) {
	// Old rig: labels should still match agents with project: subscriptions
	labels := []string{"rig:gasboat", "role:crew"}
	subs := []string{"global", "project:gasboat", "role:crew"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("advice with rig:gasboat should match agent with project:gasboat (backward compat)")
	}
}

func TestMatchesSubscriptions_AgentSpecific(t *testing.T) {
	labels := []string{"agent:gasboat/crews/bot"}
	subs := []string{"global", "agent:gasboat/crews/bot"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("agent-specific advice should match the exact agent")
	}
}

func TestMatchesSubscriptions_AgentMismatch(t *testing.T) {
	labels := []string{"agent:gasboat/crews/other"}
	subs := []string{"global", "agent:gasboat/crews/bot"}
	if MatchesSubscriptions(labels, subs) {
		t.Error("agent-specific advice should not match a different agent")
	}
}

func TestMatchesSubscriptions_ANDGroup(t *testing.T) {
	// g0:role:crew AND g0:project:gasboat -- both must match
	labels := []string{"g0:role:crew", "g0:project:gasboat"}
	subs := []string{"global", "role:crew", "project:gasboat"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("AND group should match when all labels in group match")
	}
}

func TestMatchesSubscriptions_ANDGroupPartial(t *testing.T) {
	labels := []string{"g0:role:crew", "g0:project:gasboat"}
	subs := []string{"global", "role:crew", "project:other"}
	if MatchesSubscriptions(labels, subs) {
		t.Error("AND group should not match when only some labels match")
	}
}

func TestMatchesSubscriptions_ORGroups(t *testing.T) {
	// Two separate groups: g0:role:crew OR g1:role:lead
	labels := []string{"g0:role:crew", "g1:role:lead"}
	subs := []string{"global", "role:lead"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("OR across groups should match when any group matches")
	}
}

func TestParseGroups_Grouped(t *testing.T) {
	labels := []string{"g0:role:crew", "g0:project:gasboat", "g1:role:lead"}
	groups := ParseGroups(labels)

	if len(groups[0]) != 2 {
		t.Errorf("group 0 should have 2 labels, got %d", len(groups[0]))
	}
	if len(groups[1]) != 1 {
		t.Errorf("group 1 should have 1 label, got %d", len(groups[1]))
	}
}

func TestParseGroups_Ungrouped(t *testing.T) {
	labels := []string{"global", "role:crew"}
	groups := ParseGroups(labels)

	// Each ungrouped label gets its own group (starting at 1000)
	count := 0
	for _, v := range groups {
		count += len(v)
	}
	if count != 2 {
		t.Errorf("expected 2 total labels across groups, got %d", count)
	}
}

func TestCategorizeScope_Global(t *testing.T) {
	scope, target := CategorizeScope([]string{"global"})
	if scope != "global" || target != "" {
		t.Errorf("expected global/'', got %s/%s", scope, target)
	}
}

func TestCategorizeScope_Role(t *testing.T) {
	scope, target := CategorizeScope([]string{"global", "role:crew"})
	if scope != "role" || target != "crew" {
		t.Errorf("expected role/crew, got %s/%s", scope, target)
	}
}

func TestCategorizeScope_Agent(t *testing.T) {
	scope, target := CategorizeScope([]string{"global", "role:crew", "agent:gasboat/crews/bot"})
	if scope != "agent" || target != "gasboat/crews/bot" {
		t.Errorf("expected agent/gasboat/crews/bot, got %s/%s", scope, target)
	}
}

func TestCategorizeScope_Project(t *testing.T) {
	scope, target := CategorizeScope([]string{"project:gasboat"})
	if scope != "project" || target != "gasboat" {
		t.Errorf("expected project/gasboat, got %s/%s", scope, target)
	}
}

func TestCategorizeScope_RigBackwardCompat(t *testing.T) {
	scope, target := CategorizeScope([]string{"rig:gasboat"})
	if scope != "project" || target != "gasboat" {
		t.Errorf("expected project/gasboat from rig: label, got %s/%s", scope, target)
	}
}

func TestCategorizeScope_GroupPrefix(t *testing.T) {
	scope, target := CategorizeScope([]string{"g0:role:crew", "g0:project:gasboat"})
	if scope != "role" || target != "crew" {
		t.Errorf("expected role/crew, got %s/%s", scope, target)
	}
}

func TestBuildAgentSubscriptions(t *testing.T) {
	subs := BuildAgentSubscriptions("gasboat/crews/bot", nil)

	expected := map[string]bool{
		"global":                  true,
		"agent:gasboat/crews/bot": true,
		"project:gasboat":        true,
		"rig:gasboat":            true, // backward compat
		"role:crews":             true,
		"role:crew":              true,
	}
	for _, s := range subs {
		delete(expected, s)
	}
	if len(expected) > 0 {
		t.Errorf("missing subscriptions: %v", expected)
	}
}

func TestBuildAgentSubscriptions_SimpleID(t *testing.T) {
	subs := BuildAgentSubscriptions("myproject", nil)

	has := make(map[string]bool)
	for _, s := range subs {
		has[s] = true
	}
	if !has["global"] {
		t.Error("should include global")
	}
	if !has["project:myproject"] {
		t.Error("should include project:myproject")
	}
	if !has["rig:myproject"] {
		t.Error("should include rig:myproject (backward compat)")
	}
}

func TestBuildAgentSubscriptions_WithExtra(t *testing.T) {
	subs := BuildAgentSubscriptions("gasboat/crews/bot", []string{"custom:label"})
	has := make(map[string]bool)
	for _, s := range subs {
		has[s] = true
	}
	if !has["custom:label"] {
		t.Error("should include extra labels")
	}
}

func TestStripGroupPrefix(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"g0:role:crew", "role:crew"},
		{"g12:project:gasboat", "project:gasboat"},
		{"global", "global"},
		{"role:crew", "role:crew"},
		{"g:bad", "g:bad"}, // g without number
	}
	for _, tt := range tests {
		got := StripGroupPrefix(tt.input)
		if got != tt.want {
			t.Errorf("StripGroupPrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSingularize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"crews", "crew"},
		{"leads", "lead"},
		{"crew", "crew"},
		{"s", ""},
	}
	for _, tt := range tests {
		got := Singularize(tt.input)
		if got != tt.want {
			t.Errorf("Singularize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHasTargetingLabel(t *testing.T) {
	tests := []struct {
		labels []string
		want   bool
	}{
		{[]string{"global"}, true},
		{[]string{"project:gasboat"}, true},
		{[]string{"rig:gasboat"}, true}, // backward compat
		{[]string{"role:crew"}, true},
		{[]string{"agent:bot"}, true},
		{[]string{"g0:role:crew"}, true},
		{[]string{"random"}, false},
		{nil, false},
	}
	for _, tt := range tests {
		got := HasTargetingLabel(tt.labels)
		if got != tt.want {
			t.Errorf("HasTargetingLabel(%v) = %v, want %v", tt.labels, got, tt.want)
		}
	}
}

func TestFindMatchedLabels(t *testing.T) {
	labels := []string{"g0:role:crew", "g0:project:gasboat", "global"}
	subs := []string{"global", "role:crew", "project:gasboat"}

	matched := FindMatchedLabels(labels, subs)
	if len(matched) != 3 {
		t.Errorf("expected 3 matched labels, got %d: %v", len(matched), matched)
	}
}

func TestMatchesSubscriptions_MultiRole(t *testing.T) {
	// Agent with multiple role subscriptions should match advice for either role
	subs := []string{"global", "role:thread", "role:crew", "project:gasboat"}

	if !MatchesSubscriptions([]string{"role:crew"}, subs) {
		t.Error("multi-role agent should match role:crew advice")
	}
	if !MatchesSubscriptions([]string{"role:thread"}, subs) {
		t.Error("multi-role agent should match role:thread advice")
	}
	if MatchesSubscriptions([]string{"role:captain"}, subs) {
		t.Error("multi-role agent should not match role:captain advice")
	}
}

func TestBuildScopeHeader(t *testing.T) {
	tests := []struct {
		scope, target, want string
	}{
		{"global", "", "Global"},
		{"project", "gasboat", "Project: gasboat"},
		{"role", "crew", "Role: crew"},
		{"agent", "gasboat/crews/bot", "Agent: gasboat/crews/bot"},
	}
	for _, tt := range tests {
		got := BuildScopeHeader(tt.scope, tt.target)
		if got != tt.want {
			t.Errorf("BuildScopeHeader(%q, %q) = %q, want %q", tt.scope, tt.target, got, tt.want)
		}
	}
}

func TestGroupSortKey(t *testing.T) {
	// Global should sort before project, project before role, role before agent
	keys := []string{
		GroupSortKey("agent", "bot"),
		GroupSortKey("global", ""),
		GroupSortKey("role", "crew"),
		GroupSortKey("project", "gasboat"),
	}
	if keys[1] >= keys[3] || keys[3] >= keys[2] || keys[2] >= keys[0] {
		t.Errorf("sort order wrong: global=%s project=%s role=%s agent=%s",
			keys[1], keys[3], keys[2], keys[0])
	}
}
