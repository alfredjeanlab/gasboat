package beadsapi

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ListBeadsQuery contains the full set of query parameters for listing beads.
type ListBeadsQuery struct {
	Types      []string // Filter by bead types (e.g., "decision", "mail")
	Kinds      []string // Filter by bead kinds (e.g., "issue", "data", "config")
	Statuses   []string // Filter by statuses (e.g., "open", "closed")
	Labels     []string // Filter by labels
	Assignee   string   // Filter by assignee
	Search     string   // Full-text search
	Sort       string   // Sort field (e.g., "priority", "created_at")
	NoOpenDeps bool     // Only return beads with no open/in_progress/deferred dependencies
	Limit      int      // Max results
	Offset     int      // Pagination offset
}

// ListBeadsResult is the response from a filtered bead listing.
type ListBeadsResult struct {
	Beads []*BeadDetail
	Total int
}

// ListBeadsFiltered queries the daemon with full query parameters.
func (c *Client) ListBeadsFiltered(ctx context.Context, q ListBeadsQuery) (*ListBeadsResult, error) {
	params := url.Values{}
	if len(q.Types) > 0 {
		params.Set("type", strings.Join(q.Types, ","))
	}
	if len(q.Kinds) > 0 {
		params.Set("kind", strings.Join(q.Kinds, ","))
	}
	if len(q.Statuses) > 0 {
		params.Set("status", strings.Join(q.Statuses, ","))
	}
	if len(q.Labels) > 0 {
		params.Set("labels", strings.Join(q.Labels, ","))
	}
	if q.Assignee != "" {
		params.Set("assignee", q.Assignee)
	}
	if q.Search != "" {
		params.Set("search", q.Search)
	}
	if q.Sort != "" {
		params.Set("sort", q.Sort)
	}
	if q.NoOpenDeps {
		params.Set("no_open_deps", "true")
	}
	if q.Limit > 0 {
		params.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		params.Set("offset", strconv.Itoa(q.Offset))
	}

	path := "/v1/beads"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var resp listBeadsResponse
	if err := c.doJSON(ctx, "GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("listing beads: %w", err)
	}

	beads := make([]*BeadDetail, 0, len(resp.Beads))
	for _, b := range resp.Beads {
		beads = append(beads, b.toDetail())
	}
	return &ListBeadsResult{Beads: beads, Total: resp.Total}, nil
}

// UpdateBeadRequest contains mutable fields for updating a bead.
type UpdateBeadRequest struct {
	Title       *string           `json:"title,omitempty"`
	Description *string           `json:"description,omitempty"`
	Assignee    *string           `json:"assignee,omitempty"`
	Status      *string           `json:"status,omitempty"`
	Notes       *string           `json:"notes,omitempty"`
	Priority    *int              `json:"priority,omitempty"`
	Fields      map[string]string `json:"-"` // Handled separately via UpdateBeadFields
}

// UpdateBead updates mutable fields on a bead.
func (c *Client) UpdateBead(ctx context.Context, beadID string, req UpdateBeadRequest) error {
	body := make(map[string]any)
	if req.Title != nil {
		body["title"] = *req.Title
	}
	if req.Description != nil {
		body["description"] = *req.Description
	}
	if req.Assignee != nil {
		body["assignee"] = *req.Assignee
	}
	if req.Status != nil {
		body["status"] = *req.Status
	}
	if req.Notes != nil {
		body["notes"] = *req.Notes
	}
	if req.Priority != nil {
		body["priority"] = *req.Priority
	}
	if len(body) == 0 {
		return nil
	}
	if err := c.doJSON(ctx, "PATCH", "/v1/beads/"+url.PathEscape(beadID), body, nil); err != nil {
		return fmt.Errorf("updating bead %s: %w", beadID, err)
	}
	return nil
}

// DeleteBead deletes a bead by ID.
func (c *Client) DeleteBead(ctx context.Context, beadID string) error {
	if err := c.doJSON(ctx, "DELETE", "/v1/beads/"+url.PathEscape(beadID), nil, nil); err != nil {
		return fmt.Errorf("deleting bead %s: %w", beadID, err)
	}
	return nil
}

// AddDependency adds a dependency between beads.
func (c *Client) AddDependency(ctx context.Context, beadID, dependsOnID, depType, createdBy string) error {
	body := map[string]string{
		"depends_on_id": dependsOnID,
		"type":          depType,
		"created_by":    createdBy,
	}
	path := "/v1/beads/" + url.PathEscape(beadID) + "/dependencies"
	if err := c.doJSON(ctx, "POST", path, body, nil); err != nil {
		return fmt.Errorf("adding dependency on %s: %w", beadID, err)
	}
	return nil
}

// GetDependencies lists dependencies for a bead.
func (c *Client) GetDependencies(ctx context.Context, beadID string) ([]Dependency, error) {
	var resp struct {
		Dependencies []Dependency `json:"dependencies"`
	}
	path := "/v1/beads/" + url.PathEscape(beadID) + "/dependencies"
	if err := c.doJSON(ctx, "GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("getting dependencies for %s: %w", beadID, err)
	}
	return resp.Dependencies, nil
}

// Dependency represents a bead dependency.
type Dependency struct {
	BeadID      string `json:"bead_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// Health checks the daemon health endpoint.
func (c *Client) Health(ctx context.Context) error {
	if err := c.doJSON(ctx, "GET", "/v1/health", nil, nil); err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	return nil
}
