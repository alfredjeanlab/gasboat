package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"gasboat/controller/internal/advice"
	"gasboat/controller/internal/beadsapi"
)

// configBeadLister is the interface for querying config beads.
// Satisfied by *beadsapi.Client.
type configBeadLister interface {
	ListBeadsFiltered(ctx context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error)
}

// resolvedConfig holds a matched config bead with its specificity for sorting.
type resolvedConfig struct {
	labels      []string
	value       json.RawMessage
	specificity string // sortable key from advice.GroupSortKey
}

// ResolveConfigBeads queries config beads matching the given category and
// agent subscriptions, sorts by label specificity, and merges according
// to the category's merge strategy.
//
// Returns the merged config and the number of layers found.
// Returns nil, 0 if no matching config beads exist.
// buildRoleIndex returns a map from role name to its index in the subscription
// list. Earlier roles (lower index) have higher precedence.
func buildRoleIndex(subscriptions []string) map[string]int {
	idx := make(map[string]int)
	i := 0
	for _, s := range subscriptions {
		if strings.HasPrefix(s, "role:") {
			role := strings.TrimPrefix(s, "role:")
			if _, exists := idx[role]; !exists {
				idx[role] = i
				i++
			}
		}
	}
	return idx
}

func ResolveConfigBeads(ctx context.Context, lister configBeadLister, category string, subscriptions []string) (map[string]any, int) {
	cat := LookupCategory(category)
	if cat == nil {
		return nil, 0
	}

	result, err := lister.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"config"},
		Statuses: []string{"open"},
		Limit:    500,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[config] warning: failed to list config beads: %v\n", err)
		return nil, 0
	}

	// Build role index for multi-role specificity sorting.
	roleIndex := buildRoleIndex(subscriptions)

	// Filter: matching title (category) + matching subscriptions.
	var matched []resolvedConfig
	for _, bead := range result.Beads {
		if bead.Title != category {
			continue
		}
		if !advice.MatchesSubscriptions(bead.Labels, subscriptions) {
			continue
		}

		// Config value is stored in the bead's Description as JSON.
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(bead.Description), &raw); err != nil {
			fmt.Fprintf(os.Stderr, "[config] warning: invalid JSON in config bead %s: %v\n", bead.ID, err)
			continue
		}

		scope, target := advice.CategorizeScope(bead.Labels)
		sortKey := advice.GroupSortKey(scope, target)
		// For role-scoped beads with multi-role agents, use role index for
		// inter-role precedence. Earlier roles in BOAT_ROLE win (sort last,
		// since later items override earlier in merge).
		if scope == "role" && len(roleIndex) > 1 {
			idx, ok := roleIndex[target]
			if !ok {
				idx = 0 // unknown role = lowest precedence = sorted first
			} else {
				idx = 99 - idx // invert: first role (0) → 99, second (1) → 98
			}
			sortKey = fmt.Sprintf("2:%02d:%s", idx, target)
		}
		matched = append(matched, resolvedConfig{
			labels:      bead.Labels,
			value:       raw,
			specificity: sortKey,
		})
	}

	if len(matched) == 0 {
		return nil, 0
	}

	// Sort by specificity: global (0:) < project (1:) < role (2:) < agent (3:).
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].specificity < matched[j].specificity
	})

	// Merge layers in specificity order.
	layers := make([]json.RawMessage, len(matched))
	for i, m := range matched {
		layers[i] = m.value
	}

	return MergeLayers(cat.Strategy, layers), len(matched)
}

