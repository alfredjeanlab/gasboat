// Command controller is the Gasboat K8s controller — a thin reactive bridge
// between beads lifecycle events and Kubernetes pod operations.
//
// Architecture: Beads IS the control plane. This controller watches beads
// events via BD Daemon and translates them to pod create/delete operations.
// It does NOT use controller-runtime or CRD reconciliation loops.
//
// See docs/design/k8s-crd-schema.md and docs/design/k8s-reconciliation-loops.md.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"gasboat/controller/internal/bridge"
	"gasboat/controller/internal/client"
	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"
	"gasboat/controller/internal/reconciler"
	"gasboat/controller/internal/statusreporter"
	"gasboat/controller/internal/subscriber"
)

func main() {
	cfg := config.Parse()

	logger := setupLogger(cfg.LogLevel)
	logger.Info("starting gasboat controller",
		"beads_http", cfg.BeadsHTTPAddr,
		"namespace", cfg.Namespace)

	k8sClient, err := buildK8sClient(cfg.KubeConfig)
	if err != nil {
		logger.Error("failed to create K8s client", "error", err)
		os.Exit(1)
	}

	watcher := subscriber.NewSSEWatcher(subscriber.SSEConfig{
		BeadsHTTPAddr: cfg.BeadsHTTPAddr,
		Topics:        "beads.bead.*",
		Namespace:     cfg.Namespace,
		CoopImage:     cfg.CoopImage,
		BeadsGRPCAddr: cfg.BeadsGRPCAddr,
	}, logger)
	logger.Info("using SSE transport for beads events",
		"beads_http", cfg.BeadsHTTPAddr)
	pods := podmanager.New(k8sClient, logger)

	// Daemon client for HTTP access (used by reconciler, status reporter, and bridge).
	daemon, err := beadsapi.New(beadsapi.Config{HTTPAddr: cfg.BeadsHTTPAddr})
	if err != nil {
		logger.Error("failed to create beads daemon client", "error", err)
		os.Exit(1)
	}
	defer daemon.Close()
	status := statusreporter.NewHTTPReporter(daemon, k8sClient, cfg.Namespace, logger)

	// Register bead types, views, and context configs with the daemon.
	if err := bridge.EnsureConfigs(context.Background(), daemon, logger); err != nil {
		logger.Warn("failed to ensure beads configs (will retry on next sync)", "error", err)
	}

	// Populate project cache from daemon project beads.
	cfg.ProjectCache = make(map[string]config.ProjectCacheEntry)
	refreshProjectCache(context.Background(), logger, daemon, cfg)

	rec := reconciler.New(daemon, pods, cfg, logger, BuildSpecFromBeadInfo)

	// Slack notifications, decision watcher, and mail watcher are now handled
	// by the standalone slack-bridge binary (cmd/slack-bridge). The controller
	// only handles K8s pod lifecycle operations. See bd-8x8fy.

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	runFn := func(ctx context.Context) {
		if err := run(ctx, logger, cfg, k8sClient, watcher, pods, status, rec, daemon); err != nil {
			logger.Error("controller stopped", "error", err)
			os.Exit(1)
		}
	}

	if cfg.LeaderElection {
		runLeaderElection(ctx, logger, cfg, k8sClient, runFn)
	} else {
		runFn(ctx)
	}
}

// runLeaderElection starts the leader election loop. Only the leader runs
// the controller loop (runFn). When leadership is lost, the process exits
// so that Kubernetes restarts it and it can rejoin the election.
func runLeaderElection(ctx context.Context, logger *slog.Logger, cfg *config.Config, k8sClient kubernetes.Interface, runFn func(ctx context.Context)) {
	id := cfg.LeaderElectionIdentity
	logger.Info("starting leader election",
		"id", id,
		"lease", cfg.LeaderElectionID,
		"namespace", cfg.Namespace)

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      cfg.LeaderElectionID,
			Namespace: cfg.Namespace,
		},
		Client: k8sClient.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				logger.Info("elected as leader, starting controller")
				runFn(ctx)
			},
			OnStoppedLeading: func() {
				logger.Error("lost leader election, exiting")
				os.Exit(1)
			},
			OnNewLeader: func(identity string) {
				if identity == id {
					return
				}
				logger.Info("new leader elected", "leader", identity)
			},
		},
	})
}

// run is the main controller loop. It reads beads events and dispatches
// pod operations. Separated from main() for testability.
func run(ctx context.Context, logger *slog.Logger, cfg *config.Config, k8sClient kubernetes.Interface, watcher subscriber.Watcher, pods podmanager.Manager, status statusreporter.Reporter, rec *reconciler.Reconciler, daemon *client.Client) error {
	// Run reconciler once at startup to catch beads created during downtime.
	if rec != nil {
		logger.Info("running startup reconciliation")
		if err := rec.Reconcile(ctx); err != nil {
			logger.Warn("startup reconciliation failed", "error", err)
		}
	}

	// Start beads watcher in background.
	watcherDone := make(chan error, 1)
	go func() {
		watcherDone <- watcher.Start(ctx)
	}()

	// Start periodic SyncAll reconciliation.
	syncInterval := 60 * time.Second
	if cfg.CoopSyncInterval > 0 {
		syncInterval = cfg.CoopSyncInterval
	}
	// Seed the digest tracker with the default agent image so it starts
	// checking the registry for :latest updates immediately.
	if cfg.CoopImage != "" {
		go func() {
			dt := rec.DigestTracker()
			if dt == nil {
				return
			}
			digest, err := dt.CheckRegistryDigest(ctx, cfg.CoopImage)
			if err != nil {
				logger.Debug("initial image digest check failed", "image", cfg.CoopImage, "error", err)
				return
			}
			dt.RecordDigest(cfg.CoopImage, digest)
			logger.Info("seeded image digest tracker", "image", cfg.CoopImage, "digest", digest[:min(19, len(digest))])
		}()
	}
	go runPeriodicSync(ctx, logger, status, rec, daemon, cfg, syncInterval)

	logger.Info("controller ready, waiting for beads events",
		"sync_interval", syncInterval)

	for {
		select {
		case event, ok := <-watcher.Events():
			if !ok {
				return nil // channel closed, watcher shut down
			}
			if err := handleEvent(ctx, logger, cfg, event, pods, status); err != nil {
				logger.Error("failed to handle event", "type", event.Type, "agent", event.AgentName, "error", err)
			}

		case err := <-watcherDone:
			return fmt.Errorf("watcher stopped: %w", err)

		case <-ctx.Done():
			logger.Info("shutting down controller")
			return nil
		}
	}
}

// runPeriodicSync runs SyncAll, project cache refresh, and reconciliation at a regular interval.
func runPeriodicSync(ctx context.Context, logger *slog.Logger, status statusreporter.Reporter, rec *reconciler.Reconciler, daemon *client.Client, cfg *config.Config, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Check registry for image digest updates every 5 minutes (every 5th sync cycle at 60s interval).
	digestCheckCounter := 0
	digestCheckInterval := 5

	for {
		select {
		case <-ticker.C:
			if err := status.SyncAll(ctx); err != nil {
				logger.Warn("periodic status sync failed", "error", err)
			}
			// Refresh project cache from daemon.
			refreshProjectCache(ctx, logger, daemon, cfg)
			// Periodically check the OCI registry for image digest updates.
			digestCheckCounter++
			if rec != nil && digestCheckCounter >= digestCheckInterval {
				digestCheckCounter = 0
				if dt := rec.DigestTracker(); dt != nil {
					dt.RefreshImages(ctx)
				}
			}
			// Run reconciler to converge desired vs actual state.
			if rec != nil {
				if err := rec.Reconcile(ctx); err != nil {
					logger.Warn("periodic reconciliation failed", "error", err)
				}
			}
			// Log metrics snapshot after each sync.
			m := status.Metrics()
			logger.Info("metrics",
				"reports_total", m.StatusReportsTotal,
				"report_errors", m.StatusReportErrors,
				"sync_runs", m.SyncAllRuns,
				"sync_errors", m.SyncAllErrors)
		case <-ctx.Done():
			return
		}
	}
}

// handleEvent translates a beads lifecycle event into K8s pod operations.
func handleEvent(ctx context.Context, logger *slog.Logger, cfg *config.Config, event subscriber.Event, pods podmanager.Manager, status statusreporter.Reporter) error {
	logger.Info("handling beads event",
		"type", event.Type, "project", event.Project, "role", event.Role,
		"agent", event.AgentName, "bead", event.BeadID)

	// Use the canonical bead ID from the event. Fall back to constructing
	// from labels for backwards compatibility with older daemon versions.
	agentBeadID := event.BeadID
	if agentBeadID == "" {
		agentBeadID = fmt.Sprintf("%s-%s-%s-%s", event.Mode, event.Project, event.Role, event.AgentName)
	}

	switch event.Type {
	case subscriber.AgentSpawn:
		spec := buildAgentPodSpec(cfg, event)
		if err := pods.CreateAgentPod(ctx, spec); err != nil {
			return err
		}
		// Backend metadata (coop_url) is written by SyncAll once the pod has an IP.
		// We skip writing it here because the pod IP isn't available at creation time.
		// Report spawning status to beads.
		_ = status.ReportPodStatus(ctx, agentBeadID, statusreporter.PodStatus{
			PodName:   spec.PodName(),
			Namespace: spec.Namespace,
			Phase:     string("Pending"),
			Ready:     false,
		})
		return nil

	case subscriber.AgentDone, subscriber.AgentKill, subscriber.AgentStop:
		podName := fmt.Sprintf("%s-%s-%s-%s", event.Mode, event.Project, event.Role, event.AgentName)
		ns := namespaceFromEvent(event, cfg.Namespace)
		err := pods.DeleteAgentPod(ctx, podName, ns)
		// Clear backend metadata so stale Coop URLs don't linger.
		_ = status.ReportBackendMetadata(ctx, agentBeadID, statusreporter.BackendMetadata{})
		// Report done status to beads regardless of delete error.
		phase := "Succeeded"
		if event.Type == subscriber.AgentKill {
			phase = "Failed"
		}
		if event.Type == subscriber.AgentStop {
			phase = "Stopped"
		}
		_ = status.ReportPodStatus(ctx, agentBeadID, statusreporter.PodStatus{
			PodName:   podName,
			Namespace: ns,
			Phase:     phase,
			Ready:     false,
		})
		return err

	case subscriber.AgentStuck:
		// Delete and recreate the pod to restart the agent.
		podName := fmt.Sprintf("%s-%s-%s-%s", event.Mode, event.Project, event.Role, event.AgentName)
		ns := namespaceFromEvent(event, cfg.Namespace)
		if err := pods.DeleteAgentPod(ctx, podName, ns); err != nil {
			logger.Warn("failed to delete stuck pod (may not exist)", "pod", podName, "error", err)
		}
		spec := buildAgentPodSpec(cfg, event)
		if err := pods.CreateAgentPod(ctx, spec); err != nil {
			return err
		}
		// Report restarting status.
		_ = status.ReportPodStatus(ctx, agentBeadID, statusreporter.PodStatus{
			PodName:   spec.PodName(),
			Namespace: spec.Namespace,
			Phase:     string("Pending"),
			Ready:     false,
			Message:   "restarted due to stuck detection",
		})
		return nil

	case subscriber.AgentUpdate:
		// Metadata updates are handled by the reconciler during periodic sync.
		// No immediate pod action needed.
		return nil

	default:
		logger.Warn("unknown event type", "type", event.Type)
		return nil
	}
}

func buildK8sClient(kubeconfig string) (kubernetes.Interface, error) {
	var cfg *rest.Config
	var err error

	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("building k8s config: %w", err)
	}

	return kubernetes.NewForConfig(cfg)
}

// refreshProjectCache queries the daemon for project beads and updates cfg.ProjectCache.
func refreshProjectCache(ctx context.Context, logger *slog.Logger, daemon *client.Client, cfg *config.Config) {
	rigs, err := daemon.ListProjectBeads(ctx)
	if err != nil {
		logger.Warn("failed to refresh project cache", "error", err)
		return
	}
	for name, info := range rigs {
		cfg.ProjectCache[name] = config.ProjectCacheEntry{
			Prefix:        info.Prefix,
			GitURL:        info.GitURL,
			DefaultBranch: info.DefaultBranch,
			Image:         info.Image,
			StorageClass:  info.StorageClass,
		}
	}
	logger.Info("refreshed project cache", "count", len(rigs))
}

func setupLogger(level string) *slog.Logger {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
}
