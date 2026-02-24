// Package bridge provides state persistence for the slack-bridge.
//
// StateManager persists message references (decision messages, dashboard)
// to a JSON file so that Slack message threading and update-in-place
// survive pod restarts.
package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// MessageRef tracks a Slack message by channel and timestamp.
type MessageRef struct {
	ChannelID string `json:"channel_id"`
	Timestamp string `json:"timestamp"`
	Agent     string `json:"agent,omitempty"` // agent identity (for decision messages)
}

// DashboardRef tracks the persistent dashboard message.
type DashboardRef struct {
	ChannelID string `json:"channel_id"`
	Timestamp string `json:"timestamp"`
	LastHash  string `json:"last_hash,omitempty"` // content hash for change detection
}

// StateData is the JSON-serialized state structure.
type StateData struct {
	DecisionMessages map[string]MessageRef  `json:"decision_messages,omitempty"` // bead ID → message ref
	Dashboard        *DashboardRef          `json:"dashboard,omitempty"`
	LastEventID      string                 `json:"last_event_id,omitempty"`     // SSE event ID for reconnection
}

// StateManager provides thread-safe persistence of Slack message references.
type StateManager struct {
	mu   sync.RWMutex
	path string
	data StateData
}

// NewStateManager creates a state manager that persists to the given path.
// If the file exists, its contents are loaded.
func NewStateManager(path string) (*StateManager, error) {
	sm := &StateManager{
		path: path,
		data: StateData{
			DecisionMessages: make(map[string]MessageRef),
		},
	}
	if err := sm.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load state: %w", err)
	}
	return sm, nil
}

// --- Decision Messages ---

// GetDecisionMessage returns the message ref for a decision bead.
func (sm *StateManager) GetDecisionMessage(beadID string) (MessageRef, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	ref, ok := sm.data.DecisionMessages[beadID]
	return ref, ok
}

// SetDecisionMessage stores a message ref for a decision bead and persists.
func (sm *StateManager) SetDecisionMessage(beadID string, ref MessageRef) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data.DecisionMessages[beadID] = ref
	return sm.saveLocked()
}

// RemoveDecisionMessage removes a message ref for a decision bead and persists.
func (sm *StateManager) RemoveDecisionMessage(beadID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.data.DecisionMessages, beadID)
	return sm.saveLocked()
}

// AllDecisionMessages returns a copy of all tracked decision messages.
func (sm *StateManager) AllDecisionMessages() map[string]MessageRef {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[string]MessageRef, len(sm.data.DecisionMessages))
	for k, v := range sm.data.DecisionMessages {
		out[k] = v
	}
	return out
}

// --- Dashboard ---

// GetDashboard returns the dashboard message ref.
func (sm *StateManager) GetDashboard() (*DashboardRef, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.data.Dashboard == nil {
		return nil, false
	}
	ref := *sm.data.Dashboard
	return &ref, true
}

// SetDashboard stores the dashboard message ref and persists.
func (sm *StateManager) SetDashboard(ref DashboardRef) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data.Dashboard = &ref
	return sm.saveLocked()
}

// --- SSE Event ID ---

// GetLastEventID returns the last processed SSE event ID.
func (sm *StateManager) GetLastEventID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.data.LastEventID
}

// SetLastEventID stores the last processed SSE event ID and persists.
func (sm *StateManager) SetLastEventID(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data.LastEventID = id
	return sm.saveLocked()
}

// --- Persistence ---

func (sm *StateManager) load() error {
	data, err := os.ReadFile(sm.path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &sm.data); err != nil {
		return fmt.Errorf("unmarshal state: %w", err)
	}
	// Ensure maps are initialized.
	if sm.data.DecisionMessages == nil {
		sm.data.DecisionMessages = make(map[string]MessageRef)
	}
	return nil
}

// saveLocked writes state to disk atomically. Caller must hold sm.mu.
func (sm *StateManager) saveLocked() error {
	data, err := json.MarshalIndent(sm.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	dir := filepath.Dir(sm.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	tmp := sm.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write state tmp: %w", err)
	}
	if err := os.Rename(tmp, sm.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
