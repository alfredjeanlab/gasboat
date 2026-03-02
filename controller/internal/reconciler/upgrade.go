package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// UpgradeStrategy defines how a role's pods should be updated during drift.
type UpgradeStrategy int

const (
	// UpgradeSkip means don't kill running pods for image drift.
	// New pods spawned after the image change will use the new image.
	// Used for jobs: let running ones finish, new ones get new spec.
	UpgradeSkip UpgradeStrategy = iota

	// UpgradeRolling means update one pod at a time, waiting for the
	// replacement to become Ready before upgrading the next.
	UpgradeRolling

	// UpgradeLast means defer this role until all other non-Last roles
	// have been upgraded. Then apply UpgradeRolling.
	UpgradeLast
)

// modeUpgradeStrategy returns the upgrade strategy for a given mode.
func modeUpgradeStrategy(mode string) UpgradeStrategy {
	switch mode {
	case "job":
		return UpgradeSkip
	default:
		// crew and other persistent agents use rolling updates.
		return UpgradeRolling
	}
}

// drainEntry tracks the state of a pod being gracefully drained before upgrade.
type drainEntry struct {
	started time.Time
	coopURL string // http://{podIP}:8080
	nudged  bool   // whether the checkpoint nudge has been sent
}

// UpgradeTracker tracks the state of an ongoing rolling upgrade across pods.
// It ensures only one pod per role is being upgraded at a time.
type UpgradeTracker struct {
	mu     sync.Mutex
	logger *slog.Logger

	// upgrading tracks pods currently being upgraded (deleted for recreation).
	// Key: pod name, Value: time the upgrade started.
	upgrading map[string]time.Time

	// draining tracks pods that have been nudged to checkpoint before deletion.
	// Key: pod name, Value: drain state.
	draining map[string]*drainEntry

	// pendingByMode tracks pods that need upgrading, grouped by mode.
	// Key: mode, Value: list of pod names needing upgrade.
	pendingByMode map[string][]string

	httpClient *http.Client
}

// NewUpgradeTracker creates a new upgrade tracker.
func NewUpgradeTracker(logger *slog.Logger) *UpgradeTracker {
	return &UpgradeTracker{
		logger:        logger,
		upgrading:     make(map[string]time.Time),
		draining:      make(map[string]*drainEntry),
		pendingByMode: make(map[string][]string),
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Reset clears all tracking state. Called at the start of each reconcile pass.
func (t *UpgradeTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingByMode = make(map[string][]string)
}

// RegisterDrift records that a pod needs upgrading due to spec drift.
func (t *UpgradeTracker) RegisterDrift(podName, mode string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingByMode[mode] = append(t.pendingByMode[mode], podName)
}

// MarkUpgrading records that a pod upgrade has started (pod deleted for recreation).
func (t *UpgradeTracker) MarkUpgrading(podName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.upgrading[podName] = time.Now()
}

// ClearUpgrading removes a pod from the upgrading set (pod recreated and healthy).
func (t *UpgradeTracker) ClearUpgrading(podName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.upgrading, podName)
}

// IsUpgrading returns true if any pod of the given mode is currently being upgraded.
func (t *UpgradeTracker) IsUpgrading(mode string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name := range t.upgrading {
		// Pod names follow the pattern {mode}-{project}-{role}-{agentName}
		// We check if the mode segment matches.
		if podMode := extractModeFromPodName(name); podMode == mode {
			return true
		}
	}
	return false
}

// AllNonLastUpgraded returns true if all roles with non-Last strategies
// have no pending upgrades and nothing is currently upgrading.
func (t *UpgradeTracker) AllNonLastUpgraded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Check if any non-Last mode has pending upgrades.
	for mode, pods := range t.pendingByMode {
		if modeUpgradeStrategy(mode) == UpgradeLast {
			continue
		}
		if len(pods) > 0 {
			return false
		}
	}

	// Check if any non-Last mode is currently upgrading.
	for name := range t.upgrading {
		mode := extractModeFromPodName(name)
		if modeUpgradeStrategy(mode) != UpgradeLast {
			return false
		}
	}

	return true
}

// CanUpgrade determines whether a specific pod can be upgraded right now,
// based on its role's strategy and the current upgrade state.
func (t *UpgradeTracker) CanUpgrade(podName, mode string) bool {
	strategy := modeUpgradeStrategy(mode)

	switch strategy {
	case UpgradeSkip:
		// Never upgrade running jobs for drift.
		return false

	case UpgradeLast:
		// Defer until all other non-Last modes are done.
		if !t.AllNonLastUpgraded() {
			t.logger.Info("deferring upgrade until all other modes are upgraded",
				"pod", podName, "mode", mode)
			return false
		}
		return !t.IsUpgrading(mode)

	case UpgradeRolling:
		// Only one pod per mode at a time.
		return !t.IsUpgrading(mode)

	default:
		return false
	}
}

// CleanStaleUpgrades removes entries from the upgrading set that are older
// than the timeout. This handles the case where a pod was deleted but the
// replacement never became healthy.
func (t *UpgradeTracker) CleanStaleUpgrades(timeout time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for name, started := range t.upgrading {
		if now.Sub(started) > timeout {
			t.logger.Warn("upgrade timed out, clearing stale entry",
				"pod", name, "started", started)
			delete(t.upgrading, name)
		}
	}
}

// StartDrain begins the graceful drain process for a pod. It sends a nudge
// to the agent asking it to checkpoint and yield, then tracks the drain state.
// Returns true if the drain was started (or already in progress).
func (t *UpgradeTracker) StartDrain(ctx context.Context, podName, coopURL string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.draining[podName]; ok {
		return true // already draining
	}

	entry := &drainEntry{
		started: time.Now(),
		coopURL: coopURL,
	}

	// Send the checkpoint nudge.
	msg := "Image upgrade pending. Please checkpoint your work and yield. " +
		"Your session will be restarted with the updated image."
	if err := t.nudgeCoop(ctx, coopURL, msg); err != nil {
		t.logger.Warn("failed to nudge agent for drain, will retry next pass",
			"pod", podName, "error", err)
	} else {
		entry.nudged = true
		t.logger.Info("nudged agent to checkpoint for upgrade drain",
			"pod", podName)
	}

	t.draining[podName] = entry
	return true
}

// IsDraining returns true if the pod is currently in the drain phase.
func (t *UpgradeTracker) IsDraining(podName string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.draining[podName]
	return ok
}

// DrainReady checks if a draining pod is ready to be deleted. It returns true
// if the agent has reached idle state or if the drain timeout has expired.
func (t *UpgradeTracker) DrainReady(ctx context.Context, podName string, timeout time.Duration) bool {
	t.mu.Lock()
	entry, ok := t.draining[podName]
	if !ok {
		t.mu.Unlock()
		return true // not draining, ready to delete
	}
	coopURL := entry.coopURL
	started := entry.started
	nudged := entry.nudged
	t.mu.Unlock()

	// Check timeout.
	if time.Since(started) > timeout {
		t.logger.Warn("drain timeout expired, proceeding with forced delete",
			"pod", podName, "elapsed", time.Since(started).Round(time.Second))
		t.clearDrain(podName)
		return true
	}

	// Retry nudge if it failed on the first attempt.
	if !nudged {
		msg := "Image upgrade pending. Please checkpoint your work and yield."
		if err := t.nudgeCoop(ctx, coopURL, msg); err == nil {
			t.mu.Lock()
			if e, ok := t.draining[podName]; ok {
				e.nudged = true
			}
			t.mu.Unlock()
			t.logger.Info("retry nudge succeeded for drain", "pod", podName)
		}
	}

	// Check if the agent is idle or exited.
	state, err := t.getAgentState(ctx, coopURL)
	if err != nil {
		t.logger.Debug("could not check agent state during drain",
			"pod", podName, "error", err)
		return false
	}

	if state == "idle" || state == "exited" {
		t.logger.Info("agent reached drainable state, proceeding with upgrade",
			"pod", podName, "state", state,
			"elapsed", time.Since(started).Round(time.Second))
		t.clearDrain(podName)
		return true
	}

	t.logger.Debug("agent still working during drain, waiting",
		"pod", podName, "state", state,
		"elapsed", time.Since(started).Round(time.Second))
	return false
}

func (t *UpgradeTracker) clearDrain(podName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.draining, podName)
}

// nudgeCoop sends a nudge message to a coop agent.
func (t *UpgradeTracker) nudgeCoop(ctx context.Context, coopURL, message string) error {
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return fmt.Errorf("marshal nudge: %w", err)
	}

	url := coopURL + "/api/v1/agent/nudge"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("nudge status %d", resp.StatusCode)
	}
	return nil
}

// getAgentState queries the coop agent state endpoint.
func (t *UpgradeTracker) getAgentState(ctx context.Context, coopURL string) (string, error) {
	url := coopURL + "/api/v1/agent"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.State, nil
}

// extractModeFromPodName extracts the mode segment from a pod name.
// Pod names follow the pattern: {mode}-{project}-{role}-{agentName}
// e.g., "job-gasboat-devops-furiosa" -> "job"
//       "crew-gasboat-devops-toolbox" -> "crew"
func extractModeFromPodName(podName string) string {
	parts := strings.SplitN(podName, "-", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// IsPodReady returns true if a pod is in Running phase and all containers
// have passing readiness probes.
func IsPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
