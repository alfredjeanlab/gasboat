// Package advice provides subscription matching and advice listing
// for the beads advice system. Agents receive advice based on label
// matching with AND/OR group semantics.
package advice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gasboat/controller/internal/beadsapi"
)

// BuildAgentSubscriptions creates auto-subscription labels for an agent.
// Always includes "global" and "agent:<agentID>", plus project/role labels
// parsed from the agent ID (format: project/role_plural/name).
func BuildAgentSubscriptions(agentID string, extra []string) []string {
	subs := make([]string, 0, len(extra)+4)
	subs = append(subs, extra...)
	subs = append(subs, "global")
	subs = append(subs, "agent:"+agentID)

	parts := strings.Split(agentID, "/")
	if len(parts) >= 1 && parts[0] != "" {
		subs = append(subs, "project:"+parts[0])
		subs = append(subs, "rig:"+parts[0]) // deprecated, keep for backward compat
	}
	if len(parts) >= 2 {
		rolePlural := parts[1]
		subs = append(subs, "role:"+rolePlural)
		if roleSingular := Singularize(rolePlural); roleSingular != rolePlural {
			subs = append(subs, "role:"+roleSingular)
		}
	}
	return subs
}

// EnrichAgentSubscriptions looks up the agent bead from the daemon and
// adds/removes custom advice subscriptions from the agent's configuration.
func EnrichAgentSubscriptions(ctx context.Context, daemon *beadsapi.Client, agentID string, subs []string) []string {
	parts := strings.Split(agentID, "/")
	agentName := parts[len(parts)-1]

	agentBead, err := daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		return subs // fail silently
	}

	if raw, ok := agentBead.Fields["advice_subscriptions"]; ok && raw != "" {
		var extra []string
		if json.Unmarshal([]byte(raw), &extra) == nil {
			subs = append(subs, extra...)
		}
	}

	// Derive role: subscription from the agent bead's role field.
	if role, ok := agentBead.Fields["role"]; ok && role != "" {
		subs = append(subs, "role:"+role)
	}

	// Derive role:, project:, and rig: subscriptions from the agent bead's own labels.
	for _, label := range agentBead.Labels {
		if strings.HasPrefix(label, "role:") || strings.HasPrefix(label, "project:") || strings.HasPrefix(label, "rig:") {
			subs = append(subs, label)
		}
	}

	if raw, ok := agentBead.Fields["advice_subscriptions_exclude"]; ok && raw != "" {
		var exclude []string
		if json.Unmarshal([]byte(raw), &exclude) == nil && len(exclude) > 0 {
			excludeSet := make(map[string]bool, len(exclude))
			for _, exc := range exclude {
				excludeSet[exc] = true
			}
			filtered := subs[:0]
			for _, sub := range subs {
				if !excludeSet[sub] {
					filtered = append(filtered, sub)
				}
			}
			subs = filtered
		}
	}

	return subs
}

// MatchesSubscriptions checks if an advice bead should be delivered to an agent
// based on the agent's subscription labels.
func MatchesSubscriptions(adviceLabels, subscriptions []string) bool {
	subSet := make(map[string]bool, len(subscriptions))
	for _, s := range subscriptions {
		subSet[s] = true
	}

	// Check required labels: project:/rig:X and agent:X must be in subscriptions.
	for _, l := range adviceLabels {
		clean := StripGroupPrefix(l)
		if strings.HasPrefix(clean, "project:") && !subSet[clean] {
			// Also accept deprecated rig: equivalent
			rigEquiv := "rig:" + strings.TrimPrefix(clean, "project:")
			if !subSet[rigEquiv] {
				return false
			}
		}
		if strings.HasPrefix(clean, "rig:") && !subSet[clean] {
			// Also accept project: equivalent
			projEquiv := "project:" + strings.TrimPrefix(clean, "rig:")
			if !subSet[projEquiv] {
				return false
			}
		}
		if strings.HasPrefix(clean, "agent:") && !subSet[clean] {
			return false
		}
	}

	// Parse label groups for AND/OR matching.
	groups := ParseGroups(adviceLabels)

	// OR across groups: if any group fully matches, advice applies.
	for _, groupLabels := range groups {
		if len(groupLabels) == 0 {
			continue
		}
		allMatch := true
		for _, label := range groupLabels {
			if !subSet[label] {
				allMatch = false
				break
			}
		}
		if allMatch {
			return true
		}
	}
	return false
}

// FindMatchedLabels returns the subset of advice labels that match the subscriptions.
func FindMatchedLabels(adviceLabels, subscriptions []string) []string {
	subSet := make(map[string]bool, len(subscriptions))
	for _, s := range subscriptions {
		subSet[s] = true
	}
	seen := make(map[string]bool)
	var matched []string
	for _, l := range adviceLabels {
		clean := StripGroupPrefix(l)
		if subSet[clean] && !seen[clean] {
			matched = append(matched, clean)
			seen[clean] = true
		}
	}
	return matched
}

// CategorizeScope determines the targeting scope from advice labels.
// Returns scope (global/project/role/agent) and the target value.
func CategorizeScope(labels []string) (scope, target string) {
	for _, l := range labels {
		clean := StripGroupPrefix(l)
		switch {
		case strings.HasPrefix(clean, "agent:"):
			return "agent", strings.TrimPrefix(clean, "agent:")
		case strings.HasPrefix(clean, "role:"):
			scope, target = "role", strings.TrimPrefix(clean, "role:")
		case strings.HasPrefix(clean, "project:") && scope != "role":
			scope, target = "project", strings.TrimPrefix(clean, "project:")
		case strings.HasPrefix(clean, "rig:") && scope != "role" && scope != "project":
			scope, target = "project", strings.TrimPrefix(clean, "rig:") // rig: treated as project:
		case clean == "global" && scope == "":
			scope, target = "global", ""
		}
	}
	if scope == "" {
		scope = "global"
	}
	return scope, target
}

// BuildScopeHeader returns a human-readable header for the scope.
func BuildScopeHeader(scope, target string) string {
	switch scope {
	case "global":
		return "Global"
	case "project":
		return "Project: " + target
	case "role":
		return "Role: " + target
	case "agent":
		return "Agent: " + target
	default:
		return scope
	}
}

// GroupSortKey returns a sortable key for ordering scope groups.
func GroupSortKey(scope, target string) string {
	switch scope {
	case "global":
		return "0:" + target
	case "project":
		return "1:" + target
	case "role":
		return "2:" + target
	case "agent":
		return "3:" + target
	default:
		return "9:" + target
	}
}

// StripGroupPrefix removes the gN: prefix from a label if present.
// "g0:role:polecat" -> "role:polecat", "global" -> "global".
func StripGroupPrefix(label string) string {
	if len(label) >= 3 && label[0] == 'g' {
		for i := 1; i < len(label); i++ {
			if label[i] == ':' && i > 1 {
				return label[i+1:]
			}
			if label[i] < '0' || label[i] > '9' {
				break
			}
		}
	}
	return label
}

// ParseGroups extracts group numbers from label prefixes.
// Labels with gN: prefix are grouped together (AND within group).
// Labels without prefix are treated as separate groups (backward compat - OR behavior).
func ParseGroups(labels []string) map[int][]string {
	groups := make(map[int][]string)
	nextUnprefixed := 1000

	for _, label := range labels {
		if strings.HasPrefix(label, "g") {
			idx := strings.Index(label, ":")
			if idx > 1 {
				var groupNum int
				if _, err := fmt.Sscanf(label[:idx], "g%d", &groupNum); err == nil {
					groups[groupNum] = append(groups[groupNum], label[idx+1:])
					continue
				}
			}
		}
		// No valid gN: prefix -- treat as its own group (OR behavior).
		groups[nextUnprefixed] = append(groups[nextUnprefixed], label)
		nextUnprefixed++
	}
	return groups
}

// HasTargetingLabel checks whether any label is a targeting label
// (global, project:, rig:, role:, agent:).
func HasTargetingLabel(labels []string) bool {
	for _, l := range labels {
		l = StripGroupPrefix(l)
		switch {
		case l == "global":
			return true
		case strings.HasPrefix(l, "project:"):
			return true
		case strings.HasPrefix(l, "rig:"):
			return true
		case strings.HasPrefix(l, "role:"):
			return true
		case strings.HasPrefix(l, "agent:"):
			return true
		}
	}
	return false
}

// Singularize converts a plural role name to singular by stripping a trailing "s".
func Singularize(plural string) string {
	if strings.HasSuffix(plural, "s") {
		return strings.TrimSuffix(plural, "s")
	}
	return plural
}
