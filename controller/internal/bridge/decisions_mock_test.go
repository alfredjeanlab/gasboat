package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"gasboat/controller/internal/beadsapi"
)

// mockDaemon implements BeadClient for testing.
type mockDaemon struct {
	mu       sync.Mutex
	beads    map[string]*beadsapi.BeadDetail
	closed   []closeCall
	getCalls int
}

type closeCall struct {
	BeadID string
	Fields map[string]string
}

func newMockDaemon() *mockDaemon {
	return &mockDaemon{
		beads: make(map[string]*beadsapi.BeadDetail),
	}
}

// seedProject pre-populates the mock with a project bead so that project
// validation in handleSpawnCommand passes for tests that use a project name.
func (m *mockDaemon) seedProject(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "proj-" + name
	m.beads[id] = &beadsapi.BeadDetail{
		ID:    id,
		Title: name,
		Type:  "project",
	}
}

// seedProjectWithChannel pre-populates the mock with a project bead that has
// a slack_channel field for channel-to-project resolution tests.
func (m *mockDaemon) seedProjectWithChannel(name, channelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "proj-" + name
	m.beads[id] = &beadsapi.BeadDetail{
		ID:     id,
		Title:  name,
		Type:   "project",
		Fields: map[string]string{"slack_channel": channelID},
	}
}

func (m *mockDaemon) GetBead(_ context.Context, beadID string) (*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if b, ok := m.beads[beadID]; ok {
		return b, nil
	}
	return &beadsapi.BeadDetail{ID: beadID}, nil
}

func (m *mockDaemon) FindAgentBead(_ context.Context, agentName string) (*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if b, ok := m.beads[agentName]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("agent bead not found for agent %q", agentName)
}

func (m *mockDaemon) CloseBead(_ context.Context, beadID string, fields map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, closeCall{BeadID: beadID, Fields: fields})
	return nil
}

func (m *mockDaemon) CreateBead(_ context.Context, req beadsapi.CreateBeadRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("bd-chat-%d", len(m.beads)+1)
	var fields map[string]string
	if len(req.Fields) > 0 {
		_ = json.Unmarshal(req.Fields, &fields)
	}
	m.beads[id] = &beadsapi.BeadDetail{
		ID:       id,
		Title:    req.Title,
		Type:     req.Type,
		Assignee: req.Assignee,
		Labels:   req.Labels,
		Fields:   fields,
	}
	return id, nil
}

func (m *mockDaemon) SpawnAgent(_ context.Context, agentName, project, taskID, role, customPrompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if role == "" {
		role = "crew"
	}
	id := fmt.Sprintf("bd-agent-%d", len(m.beads)+1)
	desc := ""
	if taskID != "" {
		desc = "Assigned to task: " + taskID
	}
	fields := map[string]string{"agent": agentName, "project": project, "mode": "crew", "role": role}
	if customPrompt != "" {
		fields["prompt"] = customPrompt
	}
	if taskID != "" {
		fields["task_id"] = taskID
	}
	m.beads[id] = &beadsapi.BeadDetail{
		ID:          id,
		Title:       agentName,
		Type:        "agent",
		Description: desc,
		Fields:      fields,
	}
	return id, nil
}

func (m *mockDaemon) ListDecisionBeads(_ context.Context) ([]*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*beadsapi.BeadDetail
	for _, b := range m.beads {
		if b.Type == "decision" {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockDaemon) ListAssignedTask(_ context.Context, agentName string) (*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.beads {
		if b.Status == "in_progress" && b.Assignee == agentName && b.Kind == "issue" {
			return b, nil
		}
	}
	return nil, nil
}

func (m *mockDaemon) ListProjectBeads(_ context.Context) (map[string]beadsapi.ProjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]beadsapi.ProjectInfo)
	for _, b := range m.beads {
		if b.Type == "project" {
			info := beadsapi.ProjectInfo{Name: b.Title}
			if b.Fields != nil {
				if ch := b.Fields["slack_channel"]; ch != "" {
					info.SlackChannels = []string{ch}
				}
			}
			result[b.Title] = info
		}
	}
	return result, nil
}

func (m *mockDaemon) ListAgentBeads(_ context.Context) ([]beadsapi.AgentBead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []beadsapi.AgentBead
	for _, b := range m.beads {
		if b.Type == "agent" {
			result = append(result, beadsapi.AgentBead{
				ID:         b.ID,
				Title:      b.Title,
				Project:    b.Fields["project"],
				Mode:       "crew",
				Role:       b.Fields["role"],
				AgentName:  b.Fields["agent"],
				AgentState: b.Fields["agent_state"],
				Metadata:   b.Fields,
			})
		}
	}
	return result, nil
}

func (m *mockDaemon) ListBeadsFiltered(_ context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*beadsapi.BeadDetail
	for _, b := range m.beads {
		match := true
		if len(q.Types) > 0 {
			typeMatch := false
			for _, t := range q.Types {
				if b.Type == t {
					typeMatch = true
					break
				}
			}
			if !typeMatch {
				match = false
			}
		}
		if match {
			result = append(result, b)
		}
	}
	return &beadsapi.ListBeadsResult{Beads: result, Total: len(result)}, nil
}

func (m *mockDaemon) AddDependency(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (m *mockDaemon) UpdateBeadFields(_ context.Context, beadID string, fields map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bead, ok := m.beads[beadID]
	if !ok {
		return fmt.Errorf("bead %s not found", beadID)
	}
	if bead.Fields == nil {
		bead.Fields = make(map[string]string)
	}
	for k, v := range fields {
		bead.Fields[k] = v
	}
	return nil
}

func (m *mockDaemon) ResolveTicket(_ context.Context, ticketKey string) (*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Check direct bead ID match.
	if b, ok := m.beads[ticketKey]; ok {
		return b, nil
	}
	// Check jira_key field match.
	for _, b := range m.beads {
		if b.Fields != nil && b.Fields["jira_key"] == ticketKey {
			return b, nil
		}
	}
	return nil, fmt.Errorf("ticket %q not found", ticketKey)
}

func (m *mockDaemon) getClosed() []closeCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]closeCall{}, m.closed...)
}

func (m *mockDaemon) getGetCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getCalls
}

// mockNotifier records calls to NotifyDecision, UpdateDecision, NotifyEscalation, DismissDecision, and PostReport.
type mockNotifier struct {
	mu        sync.Mutex
	created   []BeadEvent
	updated   []updateCall
	escalated []BeadEvent
	dismissed []string
	reports   []reportCall
}

type reportCall struct {
	DecisionID string
	ReportType string
	Content    string
}

type updateCall struct {
	BeadID    string
	Chosen    string
	Rationale string
}

func (m *mockNotifier) NotifyDecision(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, bead)
	return nil
}

func (m *mockNotifier) UpdateDecision(_ context.Context, beadID, chosen, rationale string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updated = append(m.updated, updateCall{beadID, chosen, rationale})
	return nil
}

func (m *mockNotifier) NotifyEscalation(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.escalated = append(m.escalated, bead)
	return nil
}

func (m *mockNotifier) DismissDecision(_ context.Context, beadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dismissed = append(m.dismissed, beadID)
	return nil
}

func (m *mockNotifier) PostReport(_ context.Context, decisionID, reportType, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports = append(m.reports, reportCall{decisionID, reportType, content})
	return nil
}

func (m *mockNotifier) getReports() []reportCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]reportCall{}, m.reports...)
}

func (m *mockNotifier) getCreated() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.created...)
}

func (m *mockNotifier) getUpdated() []updateCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]updateCall{}, m.updated...)
}

func (m *mockNotifier) getEscalated() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.escalated...)
}

func (m *mockNotifier) getDismissed() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.dismissed...)
}

// marshalSSEBeadPayload wraps a BeadEvent in the kbeads SSE event format
// ({"bead": {...}}) for testing handleCreated/handleClosed which now accept
// raw SSE JSON data.
func marshalSSEBeadPayload(bead BeadEvent) []byte {
	data, _ := json.Marshal(map[string]any{"bead": bead})
	return data
}
