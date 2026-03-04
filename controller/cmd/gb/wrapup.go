package main

// wrapup.go — Structured wrap-up message schema for agent despawn.
//
// When agents call gb stop (or gb done), they provide a wrap-up message
// summarizing their session. The wrap-up is stored as a JSON-serialized
// string in the agent bead's "wrapup" field, making it queryable and
// parseable by other tools (bridges, dashboards, successor agents).
//
// ## Storage approach
//
// The wrap-up is stored as a single JSON string in bead fields["wrapup"].
// This was chosen over alternatives because:
//   - Comments are immutable append-only text — not structured or queryable
//   - Child data beads add complexity and an extra entity to track
//   - A single JSON field is atomic, readable via kd show, and extensible
//
// The existing --reason flag is preserved for backward compatibility: if
// --reason is provided without --wrapup, behavior is unchanged (a plain
// comment is added). When --wrapup is provided, the structured wrap-up
// is stored AND a human-readable summary comment is also added.
//
// ## Extensibility via advice
//
// The WrapUpRequirements struct defines which fields are required vs
// optional, and can specify custom fields. Advice beads with category
// "wrapup" can override these defaults per role/rig/agent scope. The
// requirements are injected into agent context via gb prime so agents
// know what's expected before they reach gb stop.
//
// ## Field reference (stored in bead fields["wrapup"])
//
//   accomplishments  — What the agent did this session (required by default)
//   blockers         — What's preventing further progress (optional)
//   handoff_notes    — Context for the next agent picking up this work (optional)
//   beads_closed     — List of bead IDs closed during this session (auto-populated)
//   pull_requests    — List of PR URLs created during this session (optional)
//   custom           — Arbitrary key-value pairs for advice-driven extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"gasboat/controller/internal/advice"
)

// WrapUp is the structured wrap-up message stored on agent beads at stop time.
// It is serialized to JSON and stored in bead fields["wrapup"].
type WrapUp struct {
	// Accomplishments summarizes what the agent did this session.
	// Required by default (enforced by WrapUpRequirements).
	Accomplishments string `json:"accomplishments"`

	// Blockers lists anything preventing further progress on claimed work.
	// Optional by default.
	Blockers string `json:"blockers,omitempty"`

	// HandoffNotes provides context for the next agent or human picking
	// up this work — what state things are in, what to do next.
	// Optional by default.
	HandoffNotes string `json:"handoff_notes,omitempty"`

	// BeadsClosed is auto-populated with IDs of beads the agent closed
	// during this session. Populated by gb stop from the agent's activity.
	BeadsClosed []string `json:"beads_closed,omitempty"`

	// PullRequests lists URLs of PRs created during this session.
	// Optional — agents can include this for visibility.
	PullRequests []string `json:"pull_requests,omitempty"`

	// Custom holds arbitrary key-value pairs for advice-driven extensions.
	// Advice beads can declare custom required/optional fields that agents
	// must populate. Keys are field names, values are their content.
	Custom map[string]string `json:"custom,omitempty"`

	// Timestamp is set automatically when the wrap-up is created.
	Timestamp time.Time `json:"timestamp"`
}

// WrapUpRequirements defines what a wrap-up must contain. Loaded from
// advice beads with category "wrapup" and merged by scope precedence
// (global < rig < role < agent). See prime_advice.go for injection.
type WrapUpRequirements struct {
	// Required lists field names that must be non-empty in the wrap-up.
	// Default: ["accomplishments"]
	Required []string `json:"required"`

	// Optional lists field names that are encouraged but not enforced.
	// Default: ["blockers", "handoff_notes", "pull_requests"]
	Optional []string `json:"optional"`

	// CustomFields defines additional fields beyond the built-in set.
	// Each entry specifies a field name and whether it's required.
	CustomFields []CustomFieldDef `json:"custom_fields,omitempty"`

	// Enforce controls whether gb stop blocks on incomplete wrap-ups.
	// "hard" = error if required fields missing (default for production)
	// "soft" = warn but allow stop
	// "none" = no validation
	Enforce string `json:"enforce"`
}

// CustomFieldDef describes an advice-driven custom field for wrap-ups.
type CustomFieldDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
}

// DefaultWrapUpRequirements returns the baseline requirements used when
// no advice bead overrides are present.
func DefaultWrapUpRequirements() WrapUpRequirements {
	return WrapUpRequirements{
		Required: []string{"accomplishments"},
		Optional: []string{"blockers", "handoff_notes", "pull_requests"},
		Enforce:  "soft",
	}
}

// Validate checks a WrapUp against the given requirements. Returns a list
// of validation errors (empty if valid). Does not return an error for
// enforce="none".
func (r WrapUpRequirements) Validate(w *WrapUp) []string {
	if r.Enforce == "none" {
		return nil
	}

	var issues []string
	for _, field := range r.Required {
		if !wrapUpFieldPresent(w, field) {
			issues = append(issues, fmt.Sprintf("required field %q is empty", field))
		}
	}
	for _, cf := range r.CustomFields {
		if cf.Required && (w.Custom == nil || w.Custom[cf.Name] == "") {
			issues = append(issues, fmt.Sprintf("required custom field %q is empty", cf.Name))
		}
	}
	return issues
}

// wrapUpFieldPresent checks whether a named field has a non-empty value.
func wrapUpFieldPresent(w *WrapUp, field string) bool {
	switch field {
	case "accomplishments":
		return w.Accomplishments != ""
	case "blockers":
		return w.Blockers != ""
	case "handoff_notes":
		return w.HandoffNotes != ""
	case "beads_closed":
		return len(w.BeadsClosed) > 0
	case "pull_requests":
		return len(w.PullRequests) > 0
	default:
		// Check custom fields.
		return w.Custom != nil && w.Custom[field] != ""
	}
}

// MarshalWrapUp serializes a WrapUp to a JSON string suitable for storage
// in bead fields["wrapup"].
func MarshalWrapUp(w *WrapUp) (string, error) {
	if w.Timestamp.IsZero() {
		w.Timestamp = time.Now().UTC()
	}
	data, err := json.Marshal(w)
	if err != nil {
		return "", fmt.Errorf("marshalling wrap-up: %w", err)
	}
	return string(data), nil
}

// UnmarshalWrapUp deserializes a WrapUp from a JSON string (as stored in
// bead fields["wrapup"]).
func UnmarshalWrapUp(s string) (*WrapUp, error) {
	var w WrapUp
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		return nil, fmt.Errorf("unmarshalling wrap-up: %w", err)
	}
	return &w, nil
}

// WrapUpToComment renders a human-readable summary of the wrap-up for
// storage as a bead comment (in addition to the structured field).
func WrapUpToComment(w *WrapUp) string {
	var b []byte
	b = append(b, "gb stop wrap-up:\n"...)

	b = append(b, "\nAccomplishments: "...)
	b = append(b, w.Accomplishments...)

	if w.Blockers != "" {
		b = append(b, "\nBlockers: "...)
		b = append(b, w.Blockers...)
	}
	if w.HandoffNotes != "" {
		b = append(b, "\nHandoff: "...)
		b = append(b, w.HandoffNotes...)
	}
	if len(w.BeadsClosed) > 0 {
		b = append(b, "\nBeads closed: "...)
		for i, id := range w.BeadsClosed {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = append(b, id...)
		}
	}
	if len(w.PullRequests) > 0 {
		b = append(b, "\nPRs: "...)
		for i, url := range w.PullRequests {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = append(b, url...)
		}
	}
	for k, v := range w.Custom {
		b = append(b, "\n"...)
		b = append(b, k...)
		b = append(b, ": "...)
		b = append(b, v...)
	}

	return string(b)
}

// WrapUpFieldName is the bead field key where the structured wrap-up is stored.
const WrapUpFieldName = "wrapup"

// OutputWrapUpExpectations writes a markdown section describing wrap-up
// requirements to w. Called by gb prime to inject expectations into agent
// context. If no config beads are found, outputs default expectations.
func outputWrapUpExpectations(w io.Writer, agentID string) {
	ctx := context.Background()
	reqs := LoadWrapUpRequirements(ctx, daemon, agentID)

	// Don't output anything if enforcement is disabled.
	if reqs.Enforce == "none" {
		return
	}

	fmt.Fprintf(w, "\n## Wrap-Up Requirements\n\n")

	switch reqs.Enforce {
	case "hard":
		fmt.Fprintln(w, "You **must** provide a structured wrap-up when calling `gb stop`.")
	default:
		fmt.Fprintln(w, "You **should** provide a structured wrap-up when calling `gb stop`.")
	}

	fmt.Fprintln(w, "")

	if len(reqs.Required) > 0 {
		fmt.Fprint(w, "**Required fields:** ")
		for i, f := range reqs.Required {
			if i > 0 {
				fmt.Fprint(w, ", ")
			}
			fmt.Fprintf(w, "`%s`", f)
		}
		fmt.Fprintln(w)
	}

	if len(reqs.Optional) > 0 {
		fmt.Fprint(w, "**Optional fields:** ")
		for i, f := range reqs.Optional {
			if i > 0 {
				fmt.Fprint(w, ", ")
			}
			fmt.Fprintf(w, "`%s`", f)
		}
		fmt.Fprintln(w)
	}

	if len(reqs.CustomFields) > 0 {
		fmt.Fprintln(w, "**Custom fields:**")
		for _, cf := range reqs.CustomFields {
			reqStr := "optional"
			if cf.Required {
				reqStr = "required"
			}
			if cf.Description != "" {
				fmt.Fprintf(w, "- `%s` (%s) — %s\n", cf.Name, reqStr, cf.Description)
			} else {
				fmt.Fprintf(w, "- `%s` (%s)\n", cf.Name, reqStr)
			}
		}
	}

	fmt.Fprintf(w, "\n**Example:**\n```bash\ngb stop --wrapup '{\"accomplishments\":\"...\",\"blockers\":\"...\",\"handoff_notes\":\"...\"}'\n```\n")
}

// WrapUpConfigCategory is the config bead category for wrap-up requirements.
// Config beads with this title are resolved using the standard layered merge
// system (global < rig < role < agent), allowing different roles to have
// different wrap-up expectations.
const WrapUpConfigCategory = "wrapup-config"

// LoadWrapUpRequirements resolves wrap-up requirements from config beads
// matching the agent's subscriptions. Falls back to DefaultWrapUpRequirements
// if no config beads are found.
//
// The config bead value is a JSON object matching the WrapUpRequirements
// schema. Example config bead (created via gb config load):
//
//	{
//	  "title": "wrapup-config",
//	  "labels": ["role:crew"],
//	  "value": {
//	    "required": ["accomplishments", "blockers"],
//	    "optional": ["handoff_notes", "pull_requests"],
//	    "enforce": "hard"
//	  }
//	}
func LoadWrapUpRequirements(ctx context.Context, lister configBeadLister, agentID string) WrapUpRequirements {
	subs := advice.BuildAgentSubscriptions(agentID, nil)
	merged, count := ResolveConfigBeads(ctx, lister, WrapUpConfigCategory, subs)
	if count == 0 || merged == nil {
		return DefaultWrapUpRequirements()
	}

	// Re-serialize the merged map and unmarshal into WrapUpRequirements.
	data, err := json.Marshal(merged)
	if err != nil {
		return DefaultWrapUpRequirements()
	}

	var reqs WrapUpRequirements
	if err := json.Unmarshal(data, &reqs); err != nil {
		return DefaultWrapUpRequirements()
	}

	// Apply defaults for unset fields.
	if reqs.Enforce == "" {
		reqs.Enforce = "soft"
	}
	if len(reqs.Required) == 0 && len(reqs.Optional) == 0 {
		return DefaultWrapUpRequirements()
	}

	return reqs
}
