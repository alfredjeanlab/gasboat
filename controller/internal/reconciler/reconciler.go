// Package reconciler diffs desired agent bead state (from daemon) against
// actual K8s pod state and creates/deletes pods to converge.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"
)

// SpecBuilder constructs an AgentPodSpec from config, bead identity, and metadata.
// The metadata map may contain per-bead overrides (e.g., image).
type SpecBuilder func(cfg *config.Config, project, mode, role, agentName string, metadata map[string]string) podmanager.AgentPodSpec

// Reconciler diffs desired state (agent beads) against actual state (K8s pods)
// and creates/deletes pods to converge.
type Reconciler struct {
	lister         beadsapi.BeadLister
	pods           podmanager.Manager
	cfg            *config.Config
	logger         *slog.Logger
	specBuilder    SpecBuilder
	mu             sync.Mutex // prevent concurrent reconciles
	digestTracker  *ImageDigestTracker
	upgradeTracker *UpgradeTracker
	destructed     atomic.Bool
	rateLimiter    *CreationRateLimiter
	circuitBreaker *CircuitBreaker
}

// New creates a Reconciler.
func New(
	lister beadsapi.BeadLister,
	pods podmanager.Manager,
	cfg *config.Config,
	logger *slog.Logger,
	specBuilder SpecBuilder,
) *Reconciler {
	return &Reconciler{
		lister:         lister,
		pods:           pods,
		cfg:            cfg,
		logger:         logger,
		specBuilder:    specBuilder,
		digestTracker:  NewImageDigestTracker(logger),
		upgradeTracker: NewUpgradeTracker(logger),
		rateLimiter:    NewCreationRateLimiter(cfg.CoopRateLimitMax, cfg.CoopRateLimitWindow),
		circuitBreaker: NewCircuitBreaker(cfg.CoopCircuitBreakerThreshold, cfg.CoopCircuitBreakerWindow, cfg.CoopCircuitBreakerCooldown),
	}
}

// DigestTracker returns the image digest tracker for external callers
// (e.g., periodic registry refresh from the main loop).
func (r *Reconciler) DigestTracker() *ImageDigestTracker {
	return r.digestTracker
}

// Autodestruct sets the autodestruct flag, force-deletes all agent pods, and
// returns the number of pods deleted. While the flag is set, Reconcile() is a
// no-op so deleted pods are not recreated.
func (r *Reconciler) Autodestruct(ctx context.Context, actor string) (int, error) {
	r.destructed.Store(true)
	r.logger.Warn("autodestruct activated", "actor", actor)
	return r.pods.DeleteAllAgentPods(ctx, r.cfg.Namespace, true)
}

// ClearAutodestruct clears the autodestruct flag, allowing Reconcile() to
// resume normal operation.
func (r *Reconciler) ClearAutodestruct(actor string) {
	r.destructed.Store(false)
	r.logger.Warn("autodestruct cleared", "actor", actor)
}

// IsDestructed returns true if autodestruct is active.
func (r *Reconciler) IsDestructed() bool {
	return r.destructed.Load()
}

// CircuitBreakerOpen returns true if the circuit breaker is currently tripped.
func (r *Reconciler) CircuitBreakerOpen() bool {
	if r.circuitBreaker == nil {
		return false
	}
	return r.circuitBreaker.IsOpen()
}

// ResetCircuitBreaker manually clears the circuit breaker, resuming pod creation.
func (r *Reconciler) ResetCircuitBreaker(actor string) {
	if r.circuitBreaker == nil {
		return
	}
	r.circuitBreaker.Reset()
	r.logger.Warn("circuit breaker manually reset", "actor", actor)
}

// Reconcile performs a single reconciliation pass:
// 1. List desired beads from daemon
// 2. List actual pods from K8s
// 3. Create missing pods, delete orphan pods, recreate failed pods
func (r *Reconciler) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.destructed.Load() {
		r.logger.Info("autodestruct active, skipping reconciliation")
		return nil
	}

	if r.circuitBreaker != nil && r.circuitBreaker.IsOpen() {
		r.logger.Warn("circuit breaker open, skipping pod creation this pass")
		return nil
	}

	// Get desired state from daemon.
	beads, err := r.lister.ListAgentBeads(ctx)
	if err != nil {
		// Fail-safe: if we can't reach the daemon, do NOT delete any pods.
		return fmt.Errorf("listing agent beads: %w", err)
	}

	// Build desired pod name set.
	desired := make(map[string]beadsapi.AgentBead)
	for _, b := range beads {
		podName := fmt.Sprintf("%s-%s-%s-%s", b.Mode, b.Project, b.Role, b.AgentName)
		desired[podName] = b
	}

	// Get actual state from K8s.
	actual, err := r.pods.ListAgentPods(ctx, r.cfg.Namespace, map[string]string{
		podmanager.LabelApp: podmanager.LabelAppValue,
	})
	if err != nil {
		return fmt.Errorf("listing agent pods: %w", err)
	}

	actualMap := make(map[string]corev1.Pod)
	for _, p := range actual {
		// Only consider pods with the gasboat.io/agent label — this
		// excludes the controller itself and other infrastructure pods
		// that share the app.kubernetes.io/name=gasboat label.
		if _, ok := p.Labels[podmanager.LabelAgent]; !ok {
			continue
		}
		actualMap[p.Name] = p
	}

	// Delete orphan pods (exist in K8s but not in desired).
	// Guard: if daemon returned zero beads but pods exist, this is likely a
	// transient daemon issue (restart, query race, etc.). Refuse to mass-delete
	// to prevent an "orphan storm" that kills all agent pods.
	if len(desired) == 0 && len(actualMap) > 0 {
		r.logger.Warn("desired state is empty but agent pods exist — skipping orphan deletion to prevent mass kill",
			"actual_pods", len(actualMap))
	} else {
		for name, pod := range actualMap {
			if _, ok := desired[name]; !ok {
				r.logger.Info("deleting orphan pod", "pod", name)
				if err := r.pods.DeleteAgentPod(ctx, name, pod.Namespace); err != nil {
					return fmt.Errorf("deleting orphan pod %s: %w", name, err)
				}
			}
		}
	}

	// Count active (non-terminal, non-orphan) pods for concurrency limiting.
	// Only count pods that are in the desired set — orphans were just deleted.
	// Exclude both Failed and Succeeded pods — they are terminal and will be
	// deleted+recreated below.
	activePods := 0
	for name, pod := range actualMap {
		if _, inDesired := desired[name]; inDesired &&
			pod.Status.Phase != corev1.PodFailed &&
			pod.Status.Phase != corev1.PodSucceeded {
			activePods++
		}
	}

	// Clean stale upgrade entries (pods deleted but never recreated).
	r.upgradeTracker.CleanStaleUpgrades(10 * time.Minute)
	r.upgradeTracker.Reset()

	// Phase 1: Scan all pods for drift and register with upgrade tracker.
	// This builds the full picture before making any upgrade decisions.
	driftReasons := make(map[string]string)
	for name, bead := range desired {
		pod, exists := actualMap[name]
		if !exists || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			continue // Missing or terminal pods are handled in phase 2
		}
		desiredSpec := r.specBuilder(r.cfg, bead.Project, bead.Mode, bead.Role, bead.AgentName, bead.Metadata)
		desiredSpec.BeadID = bead.ID
		reason := podDriftReason(desiredSpec, &pod, r.digestTracker)
		if reason != "" {
			driftReasons[name] = reason
			r.upgradeTracker.RegisterDrift(name, bead.Mode)
		}

		// Clear upgrade tracking for pods that have been successfully recreated.
		if IsPodReady(&pod) {
			r.upgradeTracker.ClearUpgrading(name)
		}
	}

	// Create missing pods and recreate failed pods.
	// Respect CoopBurstLimit (max pods created per pass) and
	// CoopMaxPods (total active pod cap).
	burstLimit := r.cfg.CoopBurstLimit
	if burstLimit <= 0 {
		burstLimit = 3 // safety default
	}
	created := 0

	for name, bead := range desired {
		if pod, exists := actualMap[name]; exists {
			// Pod exists. Check if it's in a terminal state (Failed or Succeeded).
			if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
				r.logger.Info("deleting terminal pod for recreation",
					"pod", name, "phase", pod.Status.Phase)
				if err := r.pods.DeleteAgentPod(ctx, name, pod.Namespace); err != nil {
					return fmt.Errorf("deleting terminal pod %s: %w", name, err)
				}
				// Fall through to create.
			} else if reason, hasDrift := driftReasons[name]; hasDrift {
				// Pod has spec drift. Use role-aware upgrade strategy.
				if !r.upgradeTracker.CanUpgrade(name, bead.Mode) {
					r.logger.Info("spec drift detected but upgrade deferred by strategy",
						"pod", name, "mode", bead.Mode, "reason", reason)
					continue
				}

				// Graceful drain: nudge the agent to checkpoint before deleting.
				// Derive the coop URL from the pod's IP.
				coopURL := ""
				if pod.Status.PodIP != "" {
					coopURL = fmt.Sprintf("http://%s:8080", pod.Status.PodIP)
				}

				if coopURL != "" && r.cfg.UpgradeDrainTimeout > 0 {
					// Start or continue drain.
					r.upgradeTracker.StartDrain(ctx, name, coopURL)
					if !r.upgradeTracker.DrainReady(ctx, name, r.cfg.UpgradeDrainTimeout) {
						r.logger.Info("spec drift detected, draining agent before upgrade",
							"pod", name, "mode", bead.Mode, "reason", reason)
						continue
					}
				}

				r.logger.Info("spec drift detected, upgrading pod",
					"pod", name, "mode", bead.Mode, "reason", reason)
				if err := r.pods.DeleteAgentPod(ctx, name, pod.Namespace); err != nil {
					return fmt.Errorf("deleting pod for update %s: %w", name, err)
				}
				r.upgradeTracker.MarkUpgrading(name)
				activePods-- // no longer active after deletion
				// Fall through to create with new spec.
			} else {
				continue
			}
		}

		// Check burst limit.
		if created >= burstLimit {
			r.logger.Info("spawn burst limit reached, deferring remaining pods",
				"limit", burstLimit, "deferred", name)
			continue
		}

		// Check max concurrent pods.
		if r.cfg.CoopMaxPods > 0 && activePods >= r.cfg.CoopMaxPods {
			r.logger.Info("max concurrent pods reached, deferring pod",
				"limit", r.cfg.CoopMaxPods, "active", activePods, "deferred", name)
			continue
		}

		// Check rate limiter — prevent stampede by capping creations per window.
		if r.rateLimiter != nil && !r.rateLimiter.Allow() {
			r.logger.Warn("pod creation rate limit exceeded, pausing all creation this pass",
				"window", r.cfg.CoopRateLimitWindow,
				"max", r.cfg.CoopRateLimitMax,
				"deferred", name)
			break // stop creating entirely this pass
		}

		// Create the pod.
		spec := r.specBuilder(r.cfg, bead.Project, bead.Mode, bead.Role, bead.AgentName, bead.Metadata)
		spec.BeadID = bead.ID
		r.logger.Info("creating pod", "pod", name)
		if err := r.pods.CreateAgentPod(ctx, spec); err != nil {
			// Clear upgrading state so the next sync can retry instead of
			// blocking all upgrades for this mode until the stale timeout.
			r.upgradeTracker.ClearUpgrading(name)
			r.logger.Warn("create after upgrade failed, will retry next sync",
				"pod", name, "error", err)
			continue
		}
		// Mark the image as deployed so digest drift is cleared.
		if r.digestTracker != nil && spec.Image != "" {
			r.digestTracker.MarkDeployed(spec.Image)
		}
		if r.rateLimiter != nil {
			r.rateLimiter.Record()
		}
		if r.circuitBreaker != nil {
			r.circuitBreaker.Record()
		}
		created++
		activePods++
	}

	if created > 0 || len(desired) > len(actualMap) {
		r.logger.Info("reconcile pass complete",
			"created", created, "active", activePods,
			"desired", len(desired), "burst_limit", burstLimit)
	}

	return nil
}

// podDriftReason returns a non-empty string describing why the pod needs
// recreation, or "" if the pod matches the desired spec.
func podDriftReason(desired podmanager.AgentPodSpec, actual *corev1.Pod, tracker *ImageDigestTracker) string {
	// Tag changed (e.g., latest → 2026.58.3).
	if agentChanged(desired.Image, actual) {
		return fmt.Sprintf("agent image changed: %s", desired.Image)
	}
	// Same tag, but registry digest changed (tag was re-pushed).
	// Compares registry-to-registry, never registry-to-pod, to avoid
	// manifest list vs platform digest mismatches.
	if tracker != nil && desired.Image != "" && tracker.HasDrift(desired.Image) {
		return fmt.Sprintf("image digest updated in registry: %s", desired.Image)
	}
	return ""
}

// agentChanged returns true if the desired agent image differs from the
// running pod's agent container image (compared by tag, not digest).
func agentChanged(desiredImage string, actual *corev1.Pod) bool {
	if desiredImage == "" {
		return false
	}
	for _, c := range actual.Spec.Containers {
		if c.Name == "agent" {
			return c.Image != desiredImage
		}
	}
	return false
}
