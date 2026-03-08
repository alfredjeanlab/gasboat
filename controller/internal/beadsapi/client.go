// Package beadsapi queries the beads daemon for bead state via HTTP/JSON.
package beadsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

	// Role is the agent role (e.g., "crew", "lead", "ops").
	Role string

	// Title is the bead title (e.g., "crew-gasboat-crew-k8s").
	Title string

	// AgentName is the agent's name within its role (e.g., "hq", "k8s").
	AgentName string

	// AgentState is the agent_state field (spawning, prewarmed, working, done, failed).
	AgentState string

	// PodPhase is the pod_phase field (pending, running, succeeded, failed).
	PodPhase string

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

	// Token is an optional Bearer token for authenticating with the daemon.
	// When set, it is sent as "Authorization: Bearer <token>" on every request.
	Token string
}

// Client queries the beads daemon via HTTP/JSON.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	sseClient  *http.Client // long-lived client with no timeout for SSE streams
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
		token:   cfg.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		sseClient: &http.Client{Timeout: 0},
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
		// Merge bead fields and notes into metadata. Fields provide custom
		// overrides (e.g., mock_scenario, image); notes provide runtime state
		// (e.g., coop_url, pod_name). Notes take precedence over fields.
		meta := make(map[string]string, len(fields))
		for k, v := range fields {
			meta[k] = v
		}
		for k, v := range ParseNotes(b.Notes) {
			meta[k] = v
		}
		beads = append(beads, AgentBead{
			ID:         b.ID,
			Title:      b.Title,
			Project:    project,
			Mode:       mode,
			Role:       role,
			AgentName:  name,
			AgentState: fields["agent_state"],
			PodPhase:   fields["pod_phase"],
			Metadata:   meta,
		})
	}

	return beads, nil
}

// ListTaskBeads queries the daemon for active task beads (type=task).
func (c *Client) ListTaskBeads(ctx context.Context) ([]*BeadDetail, error) {
	resp, err := c.listBeads(ctx, []string{"task"}, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("listing task beads: %w", err)
	}
	beads := make([]*BeadDetail, 0, len(resp.Beads))
	for _, b := range resp.Beads {
		beads = append(beads, b.toDetail())
	}
	return beads, nil
}

// ListAssignedTask returns the first in-progress issue-kind bead claimed by
// agentName, or nil if none is found. Uses server-side kind=issue filtering
// to exclude infrastructure beads (agent, decision, project, etc.).
func (c *Client) ListAssignedTask(ctx context.Context, agentName string) (*BeadDetail, error) {
	q := url.Values{}
	q.Set("status", "in_progress")
	q.Set("assignee", agentName)
	q.Set("kind", "issue")
	var resp listBeadsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/beads?"+q.Encode(), nil, &resp); err != nil {
		return nil, fmt.Errorf("listing assigned beads: %w", err)
	}
	if len(resp.Beads) > 0 {
		return resp.Beads[0].toDetail(), nil
	}
	return nil, nil
}

// ResolveTicket looks up a bead by external ticket key (e.g., "PE-1234").
// It searches for beads with the label "jira:<key>". If no match is found
// by label, it falls back to a direct GetBead call (for kd-* bead IDs).
// Returns the matched bead detail or an error if the ticket cannot be resolved.
func (c *Client) ResolveTicket(ctx context.Context, ticketKey string) (*BeadDetail, error) {
	// For kd-* IDs, try a direct lookup first.
	if strings.HasPrefix(ticketKey, "kd-") {
		bead, err := c.GetBead(ctx, ticketKey)
		if err == nil && bead != nil && bead.ID != "" {
			return bead, nil
		}
	}

	// Search by jira label (e.g., "jira:PE-1234").
	result, err := c.ListBeadsFiltered(ctx, ListBeadsQuery{
		Labels: []string{"jira:" + ticketKey},
		Limit:  1,
	})
	if err != nil {
		return nil, fmt.Errorf("resolving ticket %q: %w", ticketKey, err)
	}
	if len(result.Beads) > 0 {
		return result.Beads[0], nil
	}

	return nil, fmt.Errorf("ticket %q not found", ticketKey)
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

// CreateBeadRequest contains the fields for creating a new bead.
type CreateBeadRequest struct {
	Title       string          `json:"title"`
	Type        string          `json:"type"`
	Kind        string          `json:"kind,omitempty"`
	Description string          `json:"description,omitempty"`
	Assignee    string          `json:"assignee,omitempty"`
	Labels      []string        `json:"labels,omitempty"`
	Priority    int             `json:"priority,omitempty"`
	CreatedBy   string          `json:"created_by,omitempty"`
	Fields      json.RawMessage `json:"fields,omitempty"`
}

// CreateBead creates a new bead and returns its ID.
func (c *Client) CreateBead(ctx context.Context, req CreateBeadRequest) (string, error) {
	var result struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/beads", req, &result); err != nil {
		return "", fmt.Errorf("creating bead: %w", err)
	}
	return result.ID, nil
}

// SpawnAgent creates a new agent bead with the given name, project, and role.
// The bead starts in open status; the reconciler picks it up and schedules a pod.
// agentName is the agent identifier (e.g., "my-bot"). project is the project name
// (e.g., "gasboat"); if empty the daemon uses its default project.
// role is the agent role (e.g., "crew", "captain"); if empty it defaults to "crew".
// taskID is an optional bead ID of a task to pre-assign this agent. When non-empty,
// the agent bead description is set to reference the task and a dependency is added
// (type "assigned") linking the agent bead to the task. The dependency is best-effort:
// if it fails the agent bead is still returned.
// customPrompt is an optional prompt string injected into the agent session at startup.
// When non-empty, it is stored in the bead's "prompt" field and passed as BOAT_PROMPT
// to the agent pod.
func (c *Client) SpawnAgent(ctx context.Context, agentName, project, taskID, role, customPrompt string) (string, error) {
	if role == "" {
		role = "crew"
	}
	fields := map[string]string{
		"agent":   agentName,
		"mode":    "crew",
		"role":    role,
		"project": project,
	}
	if customPrompt != "" {
		fields["prompt"] = customPrompt
	}
	if taskID != "" {
		fields["task_id"] = taskID
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("marshalling agent fields: %w", err)
	}
	req := CreateBeadRequest{
		Title:  agentName,
		Type:   "agent",
		Fields: json.RawMessage(fieldsJSON),
	}
	if taskID != "" {
		req.Description = "Assigned to task: " + taskID
	} else if customPrompt != "" {
		req.Description = customPrompt
	}
	id, err := c.CreateBead(ctx, req)
	if err != nil {
		return "", fmt.Errorf("spawning agent %q: %w", agentName, err)
	}
	if project != "" {
		// Best-effort: label the agent bead with its project so it appears in
		// project-scoped listings (kd list, gb ready with --project filter).
		if err := c.AddLabel(ctx, id, "project:"+project); err != nil {
			slog.Warn("failed to add project label to agent bead",
				"agent", agentName, "bead", id, "project", project, "error", err)
		}
	}
	// Best-effort: add role label(s) so gb prime advice matching can filter by role.
	// Supports comma-separated roles (e.g. "thread,crew").
	for _, r := range strings.Split(role, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if err := c.AddLabel(ctx, id, "role:"+r); err != nil {
			slog.Warn("failed to add role label to agent bead",
				"agent", agentName, "bead", id, "role", r, "error", err)
		}
	}
	if taskID != "" {
		// Best-effort: failure to link the task does not prevent agent creation.
		// The task reference is already captured in the bead description.
		if err := c.AddDependency(ctx, id, taskID, "assigned", agentName); err != nil {
			slog.Warn("failed to add task dependency to agent bead",
				"agent", agentName, "bead", id, "task", taskID, "error", err)
		}
	}
	return id, nil
}

// BeadDetail represents a full bead returned by the daemon.
type BeadDetail struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Kind        string            `json:"kind"`
	Type        string            `json:"type"`
	Status      string            `json:"status"`
	Assignee    string            `json:"assignee"`
	Priority    int               `json:"priority"`
	Labels      []string          `json:"labels"`
	Notes       string            `json:"notes"`
	Fields      map[string]string `json:"fields"`
	Description string            `json:"description"`
	CreatedBy   string            `json:"created_by"`
	DueAt       string            `json:"due_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at,omitempty"`
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
// Existing field types are preserved from the server response: if a field was
// stored as a JSON boolean, "true"/"false" updates keep it boolean; string
// fields stay strings. New fields are sent as strings.
func (c *Client) UpdateBeadFields(ctx context.Context, beadID string, fields map[string]string) error {
	// Fetch raw bead to preserve existing field types.
	var bead beadJSON
	if err := c.doJSON(ctx, http.MethodGet, "/v1/beads/"+url.PathEscape(beadID), nil, &bead); err != nil {
		return fmt.Errorf("reading bead %s for field update: %w", beadID, err)
	}

	// Parse existing fields preserving JSON types (bool stays bool, string stays string).
	var existing map[string]any
	if len(bead.Fields) > 0 {
		_ = json.Unmarshal(bead.Fields, &existing)
	}
	if existing == nil {
		existing = make(map[string]any)
	}

	// Merge new fields, preserving the type of existing values.
	for k, v := range fields {
		if prev, ok := existing[k]; ok {
			if _, isBool := prev.(bool); isBool {
				existing[k] = (v == "true")
				continue
			}
		}
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

// UpdateBeadFieldsTyped updates typed fields on a bead via a read-modify-write
// cycle. Unlike UpdateBeadFields, values are passed as any, allowing callers
// to provide proper JSON types (e.g., bool for boolean-typed schema fields).
func (c *Client) UpdateBeadFieldsTyped(ctx context.Context, beadID string, fields map[string]any) error {
	var bead beadJSON
	if err := c.doJSON(ctx, http.MethodGet, "/v1/beads/"+url.PathEscape(beadID), nil, &bead); err != nil {
		return fmt.Errorf("reading bead %s for field update: %w", beadID, err)
	}

	var existing map[string]any
	if len(bead.Fields) > 0 {
		_ = json.Unmarshal(bead.Fields, &existing)
	}
	if existing == nil {
		existing = make(map[string]any)
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

// CloseBead closes a bead by ID, optionally setting fields at the same time.
// Fields are sent in the close request body so the server merges them without
// type-config validation — this allows decision beads to carry artifact-lifecycle
// fields (required_artifact, artifact_status, rationale) that are not declared
// in the type:decision schema.
func (c *Client) CloseBead(ctx context.Context, beadID string, fields map[string]string) error {
	body := map[string]any{
		"closed_by": "gasboat",
	}
	for k, v := range fields {
		body[k] = v
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/beads/"+url.PathEscape(beadID)+"/close", body, nil); err != nil {
		return fmt.Errorf("closing bead %s: %w", beadID, err)
	}
	return nil
}

// SetConfig upserts a config key/value in the daemon.
//
// Deprecated: KV config is superseded by label-based config beads.
// Use CreateBead with type="config" instead.
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
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Kind        string          `json:"kind"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	Assignee    string          `json:"assignee"`
	Priority    int             `json:"priority"`
	Labels      []string        `json:"labels"`
	Notes       string          `json:"notes"`
	Fields      json.RawMessage `json:"fields"`
	Description string          `json:"description"`
	CreatedBy   string          `json:"created_by"`
	DueAt       string          `json:"due_at,omitempty"`
	UpdatedAt   string          `json:"updated_at,omitempty"`
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

// updatedAtFormats lists timestamp formats the daemon may use, in preference order.
var updatedAtFormats = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

// parseTimestamp parses a timestamp string using common formats.
// Returns zero time if the string is empty or cannot be parsed.
func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range updatedAtFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// toDetail converts a beadJSON to a BeadDetail.
func (b *beadJSON) toDetail() *BeadDetail {
	return &BeadDetail{
		ID:          b.ID,
		Title:       b.Title,
		Kind:        b.Kind,
		Type:        b.Type,
		Status:      b.Status,
		Assignee:    b.Assignee,
		Priority:    b.Priority,
		Labels:      b.Labels,
		Notes:       b.Notes,
		Fields:      b.fieldsMap(),
		Description: b.Description,
		CreatedBy:   b.CreatedBy,
		DueAt:       b.DueAt,
		UpdatedAt:   parseTimestamp(b.UpdatedAt),
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
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
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
