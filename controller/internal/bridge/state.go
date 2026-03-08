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
	Agent     string `json:"agent,omitempty"`     // agent identity (for decision messages)
	ThreadTS  string `json:"thread_ts,omitempty"` // parent thread ts (set when message is a thread reply)
}

// DashboardRef tracks the persistent dashboard message.
type DashboardRef struct {
	ChannelID string `json:"channel_id"`
	Timestamp string `json:"timestamp"`
	LastHash  string `json:"last_hash,omitempty"` // content hash for change detection
}

// StateData is the JSON-serialized state structure.
type StateData struct {
	DecisionMessages map[string]MessageRef `json:"decision_messages,omitempty"` // bead ID → message ref
	ChatMessages     map[string]MessageRef `json:"chat_messages,omitempty"`     // bead ID → message ref (chat forwarding)
	AgentCards       map[string]MessageRef `json:"agent_cards,omitempty"`       // agent identity → status card message ref
	ThreadAgents     map[string]string     `json:"thread_agents,omitempty"`     // "{channel}:{thread_ts}" → agent identity
	ListenThreads    map[string]bool       `json:"listen_threads,omitempty"`    // "{channel}:{thread_ts}" → true if --listen mode
	Dashboard        *DashboardRef         `json:"dashboard,omitempty"`
	LastEventID      string                `json:"last_event_id,omitempty"` // SSE event ID for reconnection
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
			ChatMessages:     make(map[string]MessageRef),
			AgentCards:       make(map[string]MessageRef),
			ThreadAgents:     make(map[string]string),
			ListenThreads:    make(map[string]bool),
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

// --- Chat Messages ---

// GetChatMessage returns the message ref for a chat bead.
func (sm *StateManager) GetChatMessage(beadID string) (MessageRef, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	ref, ok := sm.data.ChatMessages[beadID]
	return ref, ok
}

// SetChatMessage stores a message ref for a chat bead and persists.
func (sm *StateManager) SetChatMessage(beadID string, ref MessageRef) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data.ChatMessages[beadID] = ref
	return sm.saveLocked()
}

// RemoveChatMessage removes a message ref for a chat bead and persists.
func (sm *StateManager) RemoveChatMessage(beadID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.data.ChatMessages, beadID)
	return sm.saveLocked()
}

// AllChatMessages returns a copy of all tracked chat messages.
func (sm *StateManager) AllChatMessages() map[string]MessageRef {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[string]MessageRef, len(sm.data.ChatMessages))
	for k, v := range sm.data.ChatMessages {
		out[k] = v
	}
	return out
}

// --- Agent Cards ---

// GetAgentCard returns the status card message ref for an agent.
func (sm *StateManager) GetAgentCard(agent string) (MessageRef, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	ref, ok := sm.data.AgentCards[agent]
	return ref, ok
}

// SetAgentCard stores a status card message ref for an agent and persists.
func (sm *StateManager) SetAgentCard(agent string, ref MessageRef) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data.AgentCards[agent] = ref
	return sm.saveLocked()
}

// RemoveAgentCard removes a status card message ref for an agent and persists.
func (sm *StateManager) RemoveAgentCard(agent string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.data.AgentCards, agent)
	return sm.saveLocked()
}

// AllAgentCards returns a copy of all tracked agent status card messages.
func (sm *StateManager) AllAgentCards() map[string]MessageRef {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[string]MessageRef, len(sm.data.AgentCards))
	for k, v := range sm.data.AgentCards {
		out[k] = v
	}
	return out
}

// --- Thread Agents ---

// threadAgentKey builds the map key for a thread-agent association.
func threadAgentKey(channel, threadTS string) string {
	return channel + ":" + threadTS
}

// GetThreadAgent returns the agent identity bound to a Slack thread.
func (sm *StateManager) GetThreadAgent(channel, threadTS string) (string, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	agent, ok := sm.data.ThreadAgents[threadAgentKey(channel, threadTS)]
	return agent, ok
}

// SetThreadAgent stores a thread→agent association and persists.
func (sm *StateManager) SetThreadAgent(channel, threadTS, agent string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data.ThreadAgents[threadAgentKey(channel, threadTS)] = agent
	return sm.saveLocked()
}

// RemoveThreadAgent removes a thread→agent association and persists.
func (sm *StateManager) RemoveThreadAgent(channel, threadTS string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.data.ThreadAgents, threadAgentKey(channel, threadTS))
	return sm.saveLocked()
}

// RemoveThreadAgentByAgent removes all thread associations for a given agent and persists.
func (sm *StateManager) RemoveThreadAgentByAgent(agent string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for k, v := range sm.data.ThreadAgents {
		if v == agent {
			delete(sm.data.ThreadAgents, k)
		}
	}
	return sm.saveLocked()
}

// AllThreadAgents returns a copy of all thread→agent mappings.
func (sm *StateManager) AllThreadAgents() map[string]string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[string]string, len(sm.data.ThreadAgents))
	for k, v := range sm.data.ThreadAgents {
		out[k] = v
	}
	return out
}

// ClearAllThreadAgents removes all thread→agent associations and persists.
func (sm *StateManager) ClearAllThreadAgents() (int, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	n := len(sm.data.ThreadAgents)
	sm.data.ThreadAgents = make(map[string]string)
	return n, sm.saveLocked()
}

// --- Listen Threads ---

// IsListenThread returns whether a thread has --listen mode enabled.
func (sm *StateManager) IsListenThread(channel, threadTS string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.data.ListenThreads[threadAgentKey(channel, threadTS)]
}

// SetListenThread marks a thread as having --listen mode and persists.
func (sm *StateManager) SetListenThread(channel, threadTS string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data.ListenThreads[threadAgentKey(channel, threadTS)] = true
	return sm.saveLocked()
}

// RemoveListenThread removes the --listen flag for a thread and persists.
func (sm *StateManager) RemoveListenThread(channel, threadTS string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.data.ListenThreads, threadAgentKey(channel, threadTS))
	return sm.saveLocked()
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
	if sm.data.ChatMessages == nil {
		sm.data.ChatMessages = make(map[string]MessageRef)
	}
	if sm.data.AgentCards == nil {
		sm.data.AgentCards = make(map[string]MessageRef)
	}
	if sm.data.ThreadAgents == nil {
		sm.data.ThreadAgents = make(map[string]string)
	}
	if sm.data.ListenThreads == nil {
		sm.data.ListenThreads = make(map[string]bool)
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
