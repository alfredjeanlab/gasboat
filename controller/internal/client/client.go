// Package beadsapi queries the beads daemon for bead state via HTTP/JSON.
package beadsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AgentBead represents an active agent bead from the daemon.
type AgentBead struct {
	// ID is the bead identifier (e.g., "crew-town-crew-hq", "crew-gasboat-crew-k8s").
	ID string

	// Project is the project name (e.g., "town", "gasboat").
	Project string

	// Mode is the agent mode (e.g., "crew", "job").
	Mode string

	// Role is the agent role (e.g., "mate", "deckhand", "ops").
	Role string

	// AgentName is the agent's name within its role (e.g., "hq", "k8s").
	AgentName string

	// Metadata contains additional bead metadata from the daemon.
	Metadata map[string]string
}

// BeadLister lists active agent beads from the daemon.
type BeadLister interface {
	ListAgentBeads(ctx context.Context) ([]AgentBead, error)
}

// Config for the daemon HTTP client.
type Config struct {
	// HTTPAddr is the daemon HTTP address (e.g., "http://daemon:8080").
	// If the value does not start with "http", it is prefixed with "http://".
	HTTPAddr string
}

// Client queries the beads daemon via HTTP/JSON.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates an HTTP client for querying the beads daemon.
func New(cfg Config) (*Client, error) {
	addr := cfg.HTTPAddr
	if addr == "" {
		return nil, fmt.Errorf("HTTPAddr is required")
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	addr = strings.TrimRight(addr, "/")

	return &Client{
		baseURL: addr,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Close is a no-op for the HTTP client (satisfies the old interface contract).
func (c *Client) Close() error {
	return nil
}

// activeStatuses is the set of statuses that represent non-closed beads.
var activeStatuses = []string{"open", "in_progress", "blocked", "deferred"}

// ListAgentBeads queries the daemon for active agent beads (type=agent).
func (c *Client) ListAgentBeads(ctx context.Context) ([]AgentBead, error) {
	resp, err := c.listBeads(ctx, []string{"agent"}, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("listing agent beads: %w", err)
	}

	var beads []AgentBead
	for _, b := range resp.Beads {
		fields := b.fieldsMap()
		project := fields["project"]
		mode := fields["mode"]
		role := fields["role"]
		name := fields["agent"]
		if mode == "" {
			mode = "crew"
		}
		if role == "" || name == "" {
			continue
		}
		beads = append(beads, AgentBead{
			ID:        b.ID,
			Project:   project,
			Mode:      mode,
			Role:      role,
			AgentName: name,
			Metadata:  ParseNotes(b.Notes),
		})
	}

	return beads, nil
}

// ProjectInfo represents a registered project from daemon project beads.
type ProjectInfo struct {
	Name          string // Project name (from bead title)
	Prefix        string // Beads prefix (e.g., "kd", "bot")
	GitURL        string // Repository URL
	DefaultBranch string // Default branch (e.g., "main")
	Image         string // Per-project agent image override
	StorageClass  string // Per-project PVC storage class override
}

// ListProjectBeads queries the daemon for project beads (type=project) and extracts
// project metadata from fields. Returns a map of project name -> ProjectInfo.
func (c *Client) ListProjectBeads(ctx context.Context) (map[string]ProjectInfo, error) {
	resp, err := c.listBeads(ctx, []string{"project"}, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("listing project beads: %w", err)
	}

	rigs := make(map[string]ProjectInfo)
	for _, b := range resp.Beads {
		// Strip "Project: " prefix from title -- legacy project beads may have titles
		// like "Project: beads" instead of just "beads".
		name := strings.TrimPrefix(b.Title, "Project: ")
		fields := b.fieldsMap()
		info := ProjectInfo{
			Name:          name,
			Prefix:        fields["prefix"],
			GitURL:        fields["git_url"],
			DefaultBranch: fields["default_branch"],
			Image:         fields["image"],
			StorageClass:  fields["storage_class"],
		}
		if name != "" {
			rigs[name] = info
		}
	}

	return rigs, nil
}

// ListDecisionBeads queries the daemon for active decision beads (type=decision).
func (c *Client) ListDecisionBeads(ctx context.Context) ([]*BeadDetail, error) {
	resp, err := c.listBeads(ctx, []string{"decision"}, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("listing decision beads: %w", err)
	}
	beads := make([]*BeadDetail, 0, len(resp.Beads))
	for _, b := range resp.Beads {
		beads = append(beads, b.toDetail())
	}
	return beads, nil
}

// BeadDetail represents a full bead returned by the daemon.
type BeadDetail struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Type     string            `json:"type"`
	Status   string            `json:"status"`
	Assignee string            `json:"assignee"`
	Labels   []string          `json:"labels"`
	Notes    string            `json:"notes"`
	Fields   map[string]string `json:"fields"`
}

// GetBead fetches a single bead by ID from the daemon.
func (c *Client) GetBead(ctx context.Context, beadID string) (*BeadDetail, error) {
	var bead beadJSON
	if err := c.doJSON(ctx, http.MethodGet, "/v1/beads/"+url.PathEscape(beadID), nil, &bead); err != nil {
		return nil, fmt.Errorf("getting bead %s: %w", beadID, err)
	}
	return bead.toDetail(), nil
}

// FindAgentBead searches active agent beads for the one whose "agent" field
// matches agentName (e.g., "test-bot-2"). Returns an error if no match is found.
func (c *Client) FindAgentBead(ctx context.Context, agentName string) (*BeadDetail, error) {
	resp, err := c.listBeads(ctx, []string{"agent"}, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("listing agent beads: %w", err)
	}
	for _, b := range resp.Beads {
		if b.fieldsMap()["agent"] == agentName {
			return b.toDetail(), nil
		}
	}
	return nil, fmt.Errorf("agent bead not found for agent %q", agentName)
}

// UpdateBeadFields updates typed fields on a bead via a read-modify-write cycle.
// The daemon replaces the full fields JSON, so we must merge with existing fields.
func (c *Client) UpdateBeadFields(ctx context.Context, beadID string, fields map[string]string) error {
	// Read current fields.
	detail, err := c.GetBead(ctx, beadID)
	if err != nil {
		return fmt.Errorf("reading bead %s for field update: %w", beadID, err)
	}

	// Merge new fields into existing.
	existing := detail.Fields
	if existing == nil {
		existing = make(map[string]string)
	}
	for k, v := range fields {
		existing[k] = v
	}

	merged, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshalling merged fields for %s: %w", beadID, err)
	}

	body := map[string]json.RawMessage{
		"fields": merged,
	}
	if err := c.doJSON(ctx, http.MethodPatch, "/v1/beads/"+url.PathEscape(beadID), body, nil); err != nil {
		return fmt.Errorf("updating fields on bead %s: %w", beadID, err)
	}
	return nil
}

// UpdateBeadNotes updates the notes field of a bead.
func (c *Client) UpdateBeadNotes(ctx context.Context, beadID, notes string) error {
	body := map[string]any{
		"notes": notes,
	}
	if err := c.doJSON(ctx, http.MethodPatch, "/v1/beads/"+url.PathEscape(beadID), body, nil); err != nil {
		return fmt.Errorf("updating notes on bead %s: %w", beadID, err)
	}
	return nil
}

// UpdateAgentState updates the agent_state field of a bead.
func (c *Client) UpdateAgentState(ctx context.Context, beadID, state string) error {
	return c.UpdateBeadFields(ctx, beadID, map[string]string{"agent_state": state})
}

// CloseBead closes a bead by ID, optionally setting fields before closing.
func (c *Client) CloseBead(ctx context.Context, beadID string, fields map[string]string) error {
	if len(fields) > 0 {
		if err := c.UpdateBeadFields(ctx, beadID, fields); err != nil {
			return fmt.Errorf("updating fields before close: %w", err)
		}
	}

	body := map[string]string{
		"closed_by": "gasboat",
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/beads/"+url.PathEscape(beadID)+"/close", body, nil); err != nil {
		return fmt.Errorf("closing bead %s: %w", beadID, err)
	}
	return nil
}

// SetConfig upserts a config key/value in the daemon.
func (c *Client) SetConfig(ctx context.Context, key string, value []byte) error {
	body := map[string]json.RawMessage{
		"value": value,
	}
	if err := c.doJSON(ctx, http.MethodPut, "/v1/configs/"+url.PathEscape(key), body, nil); err != nil {
		return fmt.Errorf("setting config %s: %w", key, err)
	}
	return nil
}

// --- internal types and helpers ---

// beadJSON is the JSON representation of a bead from the HTTP API.
type beadJSON struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Type     string          `json:"type"`
	Status   string          `json:"status"`
	Assignee string          `json:"assignee"`
	Labels   []string        `json:"labels"`
	Notes    string          `json:"notes"`
	Fields   json.RawMessage `json:"fields"`
}

// ParseFieldsJSON decodes a raw JSON object into a map[string]string.
// String values are kept as-is; complex values (arrays, objects, numbers)
// are re-marshaled to their JSON representation. Returns an empty (non-nil)
// map when raw is nil or empty.
func ParseFieldsJSON(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return make(map[string]string)
	}
	m := make(map[string]string)
	// Try direct string map first (fast path).
	if err := json.Unmarshal(raw, &m); err == nil {
		return m
	}
	// Fall back to map[string]any — re-marshal complex values.
	var anyMap map[string]any
	if err := json.Unmarshal(raw, &anyMap); err != nil {
		return make(map[string]string)
	}
	for k, v := range anyMap {
		switch v := v.(type) {
		case string:
			m[k] = v
		default:
			if bs, err := json.Marshal(v); err == nil {
				m[k] = string(bs)
			} else {
				m[k] = fmt.Sprintf("%v", v)
			}
		}
	}
	return m
}

// fieldsMap decodes the JSON fields into a string map.
// Complex values (arrays, objects) are re-marshaled to JSON strings.
func (b *beadJSON) fieldsMap() map[string]string {
	return ParseFieldsJSON(b.Fields)
}

// toDetail converts a beadJSON to a BeadDetail.
func (b *beadJSON) toDetail() *BeadDetail {
	return &BeadDetail{
		ID:       b.ID,
		Title:    b.Title,
		Type:     b.Type,
		Status:   b.Status,
		Assignee: b.Assignee,
		Labels:   b.Labels,
		Notes:    b.Notes,
		Fields:   b.fieldsMap(),
	}
}

// listBeadsResponse is the JSON response from GET /v1/beads.
type listBeadsResponse struct {
	Beads []beadJSON `json:"beads"`
	Total int        `json:"total"`
}

// listBeads queries the daemon for beads matching the given type and status filters.
func (c *Client) listBeads(ctx context.Context, types, statuses []string) (*listBeadsResponse, error) {
	q := url.Values{}
	if len(types) > 0 {
		q.Set("type", strings.Join(types, ","))
	}
	if len(statuses) > 0 {
		q.Set("status", strings.Join(statuses, ","))
	}

	path := "/v1/beads"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var resp listBeadsResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// APIError represents an error response from the daemon HTTP API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// doJSON performs an HTTP request with optional JSON body and decodes the JSON response.
// If result is nil, the response body is discarded (for responses where we don't need the body).
func (c *Client) doJSON(ctx context.Context, method, path string, body any, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("performing request: %w", err)
	}
	defer resp.Body.Close()

	// 204 No Content -- success with no body.
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return &APIError{StatusCode: resp.StatusCode, Message: errResp.Error}
		}
		return &APIError{StatusCode: resp.StatusCode, Message: string(respBody)}
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}

// ParseNotes parses "key: value" lines from a bead's notes field into a map.
func ParseNotes(notes string) map[string]string {
	if notes == "" {
		return nil
	}
	m := make(map[string]string)
	for _, line := range strings.Split(notes, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
