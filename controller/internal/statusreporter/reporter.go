// Package statusreporter syncs K8s pod status back to beads via the daemon HTTP API.
// This gives beads visibility into pod health.
package statusreporter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"gasboat/controller/internal/podmanager"
	"gasboat/controller/internal/reconciler"
)

// PodStatus represents the K8s pod state to report back to beads.
type PodStatus struct {
	PodName   string
	Namespace string
	Phase     string // Pending, Running, Succeeded, Failed, Unknown
	Ready     bool
	Message   string
}

// BackendMetadata holds connection info written to agent bead notes
// so that ResolveBackend() can discover how to connect to the agent.
type BackendMetadata struct {
	PodName   string // K8s pod name
	Namespace string // K8s namespace
	Backend   string // "coop" or "k8s"
	CoopURL   string // e.g., "http://crew-gasboat-crew-furiosa.gasboat.svc.cluster.local:8080"
	CoopToken string // auth token (optional)
}

// Reporter syncs pod status back to beads.
type Reporter interface {
	// ReportPodStatus sends a single pod's status to beads.
	// agentName is the agent bead ID (e.g., "crew-gasboat-crew-furiosa").
	// prewarmed indicates the pod is a prewarmed agent whose state should
	// not be overwritten to "working" while waiting for assignment.
	ReportPodStatus(ctx context.Context, agentName string, status PodStatus, prewarmed bool) error

	// ReportBackendMetadata writes backend connection info to the agent bead's notes.
	ReportBackendMetadata(ctx context.Context, agentName string, meta BackendMetadata) error

	// SyncAll reconciles all agent pod statuses with beads.
	SyncAll(ctx context.Context) error

	// Metrics returns the current metrics snapshot.
	Metrics() MetricsSnapshot
}

// MetricsSnapshot holds current metric values for logging/monitoring.
type MetricsSnapshot struct {
	StatusReportsTotal int64
	StatusReportErrors int64
	SyncAllRuns        int64
	SyncAllErrors      int64
	AgentsByState      map[string]int64 // state -> count
}

// PhaseToAgentState maps a K8s pod phase to a beads agent_state.
// In addition to standard K8s phases, it handles the synthetic "Stopped"
// phase emitted by the controller for AgentStop events.
func PhaseToAgentState(phase string) string {
	switch corev1.PodPhase(phase) {
	case corev1.PodPending:
		return "spawning"
	case corev1.PodRunning:
		return "working"
	case corev1.PodSucceeded:
		return "done"
	case corev1.PodFailed:
		return "failed"
	default:
		// Handle synthetic phases from controller events.
		if phase == "Stopped" {
			return "done"
		}
		return ""
	}
}

// agentBeadID returns the bead ID for a pod. It prefers the explicit
// gasboat.io/bead-id annotation (set by Helm or the controller). If absent,
// it constructs: {mode}-{project}-{role}-{agent}.
func agentBeadID(pod *corev1.Pod) string {
	if id := pod.Annotations[podmanager.AnnotationBeadID]; id != "" {
		return id
	}
	project := pod.Labels[podmanager.LabelProject]
	role := pod.Labels[podmanager.LabelRole]
	agent := pod.Labels[podmanager.LabelAgent]
	mode := pod.Labels[podmanager.LabelMode]
	if mode == "" {
		mode = "crew"
	}

	return fmt.Sprintf("%s-%s-%s-%s", mode, project, role, agent)
}

// detectCoopPort returns the coop API port if the pod has a container
// exposing port 8080 (CoopDefaultPort) or a container named "coop".
// Returns 0 if no coop capability detected.
func detectCoopPort(pod *corev1.Pod) int32 {
	for _, c := range pod.Spec.Containers {
		if c.Name == "coop" {
			for _, p := range c.Ports {
				if p.ContainerPort == 8080 {
					return p.ContainerPort
				}
			}
			return 8080 // coop sidecar default
		}
		for _, p := range c.Ports {
			if p.ContainerPort == 8080 {
				return p.ContainerPort
			}
		}
	}
	return 0
}

// BeadUpdater is the interface for updating beads via the daemon HTTP API.
type BeadUpdater interface {
	UpdateBeadNotes(ctx context.Context, beadID, notes string) error
	UpdateAgentState(ctx context.Context, beadID, state string) error
}

// HTTPReporter reports backend metadata to beads via the daemon HTTP API.
type HTTPReporter struct {
	daemon    BeadUpdater
	client    kubernetes.Interface
	namespace string
	logger    *slog.Logger

	reportsTotal atomic.Int64
	reportErrors atomic.Int64
	syncRuns     atomic.Int64
	syncErrors   atomic.Int64
}

// NewHTTPReporter creates a reporter that updates beads via daemon HTTP API.
func NewHTTPReporter(daemon BeadUpdater, client kubernetes.Interface, namespace string, logger *slog.Logger) *HTTPReporter {
	return &HTTPReporter{
		daemon:    daemon,
		client:    client,
		namespace: namespace,
		logger:    logger,
	}
}

// ReportPodStatus updates the agent's state in beads based on pod phase.
// Maps K8s pod phases to beads agent states via the daemon HTTP API.
// Set prewarmed=true to skip overwriting agent_state for prewarmed pods
// (their state stays "prewarmed" until explicitly assigned).
func (r *HTTPReporter) ReportPodStatus(ctx context.Context, agentName string, status PodStatus, prewarmed bool) error {
	r.reportsTotal.Add(1)

	state := PhaseToAgentState(status.Phase)
	if state == "" {
		r.logger.Debug("skipping status report for unknown phase",
			"agent", agentName, "phase", status.Phase)
		return nil
	}

	// Prewarmed agents stay prewarmed until explicitly assigned.
	// Don't overwrite "prewarmed" with "working" when the pod is Running.
	if prewarmed && state == "working" {
		r.logger.Debug("skipping status update for prewarmed agent",
			"agent", agentName, "phase", status.Phase)
		return nil
	}

	r.logger.Info("reporting pod status via HTTP",
		"agent", agentName, "pod", status.PodName,
		"phase", status.Phase, "state", state, "ready", status.Ready)

	if err := r.daemon.UpdateAgentState(ctx, agentName, state); err != nil {
		r.reportErrors.Add(1)
		r.logger.Warn("failed to report pod status",
			"agent", agentName, "state", state, "error", err)
		return fmt.Errorf("reporting status for %s: %w", agentName, err)
	}

	return nil
}

// ReportBackendMetadata writes backend connection info to the agent bead's
// notes field via the daemon HTTP API.
func (r *HTTPReporter) ReportBackendMetadata(ctx context.Context, agentName string, meta BackendMetadata) error {
	r.reportsTotal.Add(1)

	var lines []string
	if meta.Backend != "" {
		lines = append(lines, fmt.Sprintf("backend: %s", meta.Backend))
	}
	if meta.PodName != "" {
		lines = append(lines, fmt.Sprintf("pod_name: %s", meta.PodName))
	}
	if meta.Namespace != "" {
		lines = append(lines, fmt.Sprintf("pod_namespace: %s", meta.Namespace))
	}
	if meta.CoopURL != "" {
		lines = append(lines, fmt.Sprintf("coop_url: %s", meta.CoopURL))
	}
	if meta.CoopToken != "" {
		lines = append(lines, fmt.Sprintf("coop_token: %s", meta.CoopToken))
	}

	if len(lines) == 0 {
		return nil
	}

	notes := strings.Join(lines, "\n")
	r.logger.Info("reporting backend metadata via HTTP",
		"agent", agentName, "backend", meta.Backend, "coop_url", meta.CoopURL)

	if err := r.daemon.UpdateBeadNotes(ctx, agentName, notes); err != nil {
		r.reportErrors.Add(1)
		r.logger.Warn("failed to report backend metadata",
			"agent", agentName, "error", err)
		return fmt.Errorf("reporting backend metadata for %s: %w", agentName, err)
	}
	return nil
}

// SyncAll reconciles all agent pod statuses with beads.
func (r *HTTPReporter) SyncAll(ctx context.Context) error {
	r.syncRuns.Add(1)

	pods, err := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=gasboat",
	})
	if err != nil {
		r.syncErrors.Add(1)
		return fmt.Errorf("listing agent pods: %w", err)
	}

	for _, pod := range pods.Items {
		agentLabel := pod.Labels[podmanager.LabelAgent]
		projectLabel := pod.Labels[podmanager.LabelProject]
		roleLabel := pod.Labels[podmanager.LabelRole]
		if agentLabel == "" || projectLabel == "" || roleLabel == "" {
			continue
		}

		beadID := agentBeadID(&pod)
		status := PodStatus{
			PodName:   pod.Name,
			Namespace: pod.Namespace,
			Phase:     string(pod.Status.Phase),
			Ready:     reconciler.IsPodReady(&pod),
			Message:   pod.Status.Message,
		}

		isPrewarmed := pod.Annotations[podmanager.AnnotationPrewarmed] == "true"
		if err := r.ReportPodStatus(ctx, beadID, status, isPrewarmed); err != nil {
			r.logger.Warn("SyncAll: failed to report pod status",
				"bead", beadID, "pod", pod.Name, "error", err)
		}

		// Write backend metadata for coop-enabled pods so ResolveBackend() works
		// after controller restarts. Detect coop by checking for port 8080 on any container.
		// Uses pod IP because individual pods don't have DNS entries (no headless Service).
		if coopPort := detectCoopPort(&pod); coopPort > 0 && pod.Status.PodIP != "" {
			if err := r.ReportBackendMetadata(ctx, beadID, BackendMetadata{
				PodName:   pod.Name,
				Namespace: pod.Namespace,
				Backend:   "coop",
				CoopURL:   fmt.Sprintf("http://%s:%d", pod.Status.PodIP, coopPort),
			}); err != nil {
				r.logger.Warn("SyncAll: failed to report backend metadata",
					"bead", beadID, "pod", pod.Name, "error", err)
			}
		}
	}

	r.logger.Info("sync completed", "pods", len(pods.Items))
	return nil
}

// Metrics returns a snapshot of current metric values.
func (r *HTTPReporter) Metrics() MetricsSnapshot {
	return MetricsSnapshot{
		StatusReportsTotal: r.reportsTotal.Load(),
		StatusReportErrors: r.reportErrors.Load(),
		SyncAllRuns:        r.syncRuns.Load(),
		SyncAllErrors:      r.syncErrors.Load(),
	}
}
