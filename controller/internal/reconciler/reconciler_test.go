package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"
)

// --- Mock implementations ---

// mockLister implements beadsapi.BeadLister.
type mockLister struct {
	beads []beadsapi.AgentBead
	err   error
}

func (m *mockLister) ListAgentBeads(_ context.Context) ([]beadsapi.AgentBead, error) {
	return m.beads, m.err
}

// mockManager implements podmanager.Manager, recording all calls.
type mockManager struct {
	pods       []corev1.Pod
	listErr    error
	createErr  error
	deleteErr  error
	created    []podmanager.AgentPodSpec
	deleted    []string // pod names
	getResult  *corev1.Pod
	getErr     error
}

func (m *mockManager) CreateAgentPod(_ context.Context, spec podmanager.AgentPodSpec) error {
	m.created = append(m.created, spec)
	return m.createErr
}

func (m *mockManager) DeleteAgentPod(_ context.Context, name, _ string) error {
	m.deleted = append(m.deleted, name)
	return m.deleteErr
}

func (m *mockManager) ListAgentPods(_ context.Context, _ string, _ map[string]string) ([]corev1.Pod, error) {
	return m.pods, m.listErr
}

func (m *mockManager) GetAgentPod(_ context.Context, name, _ string) (*corev1.Pod, error) {
	if m.getResult != nil {
		return m.getResult, nil
	}
	return nil, m.getErr
}

// --- Helpers ---

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testConfig(namespace string) *config.Config {
	return &config.Config{
		Namespace:      namespace,
		CoopBurstLimit: 3,
		CoopMaxPods:    0, // unlimited by default
	}
}

// simpleSpecBuilder returns a SpecBuilder that creates minimal AgentPodSpec values.
func simpleSpecBuilder(image string) SpecBuilder {
	return func(cfg *config.Config, project, mode, role, agentName string, metadata map[string]string) podmanager.AgentPodSpec {
		return podmanager.AgentPodSpec{
			Project:   project,
			Mode:      mode,
			Role:      role,
			AgentName: agentName,
			Image:     image,
			Namespace: cfg.Namespace,
		}
	}
}

// makePod creates a corev1.Pod with the given name, labels, and phase.
func makePod(name, namespace, mode, project, role, agent string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				podmanager.LabelApp:     podmanager.LabelAppValue,
				podmanager.LabelAgent:   agent,
				podmanager.LabelMode:    mode,
				podmanager.LabelProject: project,
				podmanager.LabelRole:    role,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "agent", Image: "ghcr.io/org/agent:v1"},
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
}

// --- Normal reconcile tests ---

func TestReconcile_CreatesPodsForNewBeads(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
			{ID: "bd-2", Project: "proj", Mode: "crew", Role: "dev", AgentName: "beta"},
		},
	}
	mgr := &mockManager{pods: nil} // no existing pods

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.created) != 2 {
		t.Fatalf("expected 2 pods created, got %d", len(mgr.created))
	}
	// Verify created specs have correct metadata.
	names := map[string]bool{}
	for _, spec := range mgr.created {
		names[spec.AgentName] = true
		if spec.Image != "img:v1" {
			t.Errorf("expected image img:v1, got %s", spec.Image)
		}
		if spec.Namespace != "ns" {
			t.Errorf("expected namespace ns, got %s", spec.Namespace)
		}
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected agents alpha and beta, got %v", names)
	}
}

func TestReconcile_DeletesOrphanPods(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	// Two pods exist, but only one is desired.
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
			makePod("crew-proj-dev-orphan", "ns", "crew", "proj", "dev", "orphan", corev1.PodRunning),
		},
	}

	// Use the same image as makePod to avoid drift on the desired pod.
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("ghcr.io/org/agent:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 pod deleted, got %d", len(mgr.deleted))
	}
	if mgr.deleted[0] != "crew-proj-dev-orphan" {
		t.Errorf("expected orphan pod deleted, got %s", mgr.deleted[0])
	}
	if len(mgr.created) != 0 {
		t.Errorf("expected 0 pods created, got %d", len(mgr.created))
	}
}

func TestReconcile_RecreatesFailedPods(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodFailed),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should delete the failed pod and create a replacement.
	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 pod deleted, got %d", len(mgr.deleted))
	}
	if mgr.deleted[0] != "crew-proj-dev-alpha" {
		t.Errorf("expected failed pod deleted, got %s", mgr.deleted[0])
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 pod created, got %d", len(mgr.created))
	}
}

func TestReconcile_RecreatesSucceededPods(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodSucceeded),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 deletion (terminal pod), got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 creation (replacement), got %d", len(mgr.created))
	}
}

func TestReconcile_NoOpWhenConverged(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("ghcr.io/org/agent:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.created) != 0 {
		t.Errorf("expected 0 creations when converged, got %d", len(mgr.created))
	}
	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions when converged, got %d", len(mgr.deleted))
	}
}

// --- Orphan protection tests ---

func TestReconcile_OrphanProtection_RefusesMassDelete(t *testing.T) {
	// Daemon returns zero beads but pods exist. Should NOT delete any pods.
	lister := &mockLister{beads: nil}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
			makePod("crew-proj-dev-beta", "ns", "crew", "proj", "dev", "beta", corev1.PodRunning),
			makePod("crew-proj-dev-gamma", "ns", "crew", "proj", "dev", "gamma", corev1.PodRunning),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 0 {
		t.Errorf("orphan protection failed: expected 0 deletions, got %d", len(mgr.deleted))
	}
}

func TestReconcile_OrphanProtection_AllowsDeleteWhenSomeBeadsExist(t *testing.T) {
	// Daemon returns some beads (not zero). Orphan deletion should proceed normally.
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
			makePod("crew-proj-dev-orphan", "ns", "crew", "proj", "dev", "orphan", corev1.PodRunning),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("ghcr.io/org/agent:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 orphan deletion, got %d", len(mgr.deleted))
	}
	if mgr.deleted[0] != "crew-proj-dev-orphan" {
		t.Errorf("expected crew-proj-dev-orphan deleted, got %s", mgr.deleted[0])
	}
}

func TestReconcile_OrphanProtection_EmptyStateIsNoOp(t *testing.T) {
	// Both beads and pods are empty. Nothing to do.
	lister := &mockLister{beads: nil}
	mgr := &mockManager{pods: nil}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.created) != 0 {
		t.Errorf("expected 0 creations, got %d", len(mgr.created))
	}
	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions, got %d", len(mgr.deleted))
	}
}

// --- Burst limiting tests ---

func TestReconcile_BurstLimit_CapsCreationsPerPass(t *testing.T) {
	beads := make([]beadsapi.AgentBead, 5)
	for i := range beads {
		beads[i] = beadsapi.AgentBead{
			ID:        fmt.Sprintf("bd-%d", i),
			Project:   "proj",
			Mode:      "crew",
			Role:      "dev",
			AgentName: fmt.Sprintf("agent%d", i),
		}
	}
	lister := &mockLister{beads: beads}
	mgr := &mockManager{pods: nil}

	cfg := testConfig("ns")
	cfg.CoopBurstLimit = 2

	r := New(lister, mgr, cfg, testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.created) != 2 {
		t.Errorf("expected burst limit of 2 pods created, got %d", len(mgr.created))
	}
}

func TestReconcile_BurstLimit_DefaultsToThreeWhenZero(t *testing.T) {
	beads := make([]beadsapi.AgentBead, 5)
	for i := range beads {
		beads[i] = beadsapi.AgentBead{
			ID:        fmt.Sprintf("bd-%d", i),
			Project:   "proj",
			Mode:      "crew",
			Role:      "dev",
			AgentName: fmt.Sprintf("agent%d", i),
		}
	}
	lister := &mockLister{beads: beads}
	mgr := &mockManager{pods: nil}

	cfg := testConfig("ns")
	cfg.CoopBurstLimit = 0 // should default to 3

	r := New(lister, mgr, cfg, testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.created) != 3 {
		t.Errorf("expected default burst limit of 3 pods created, got %d", len(mgr.created))
	}
}

func TestReconcile_MaxPods_CapsActiveCount(t *testing.T) {
	beads := make([]beadsapi.AgentBead, 5)
	for i := range beads {
		beads[i] = beadsapi.AgentBead{
			ID:        fmt.Sprintf("bd-%d", i),
			Project:   "proj",
			Mode:      "crew",
			Role:      "dev",
			AgentName: fmt.Sprintf("agent%d", i),
		}
	}
	lister := &mockLister{beads: beads}

	// 2 pods already exist and are running.
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-agent0", "ns", "crew", "proj", "dev", "agent0", corev1.PodRunning),
			makePod("crew-proj-dev-agent1", "ns", "crew", "proj", "dev", "agent1", corev1.PodRunning),
		},
	}

	cfg := testConfig("ns")
	cfg.CoopBurstLimit = 10 // high burst limit
	cfg.CoopMaxPods = 3     // only 3 total allowed

	r := New(lister, mgr, cfg, testLogger(), simpleSpecBuilder("ghcr.io/org/agent:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 2 active + 1 new = 3 (the max). Should only create 1.
	if len(mgr.created) != 1 {
		t.Errorf("expected 1 pod created (max pods cap), got %d", len(mgr.created))
	}
}

// --- Drift detection tests ---

func TestReconcile_DriftDetection_RecreatesOnImageChange(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	// Pod is running with old image; desired spec has new image.
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
		},
	}

	// The specBuilder returns img:v2, but the pod has img:v1 (from makePod).
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v2"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should delete the drifted pod and create a replacement.
	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 deletion for drift, got %d", len(mgr.deleted))
	}
	if mgr.deleted[0] != "crew-proj-dev-alpha" {
		t.Errorf("expected crew-proj-dev-alpha deleted for drift, got %s", mgr.deleted[0])
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 creation after drift delete, got %d", len(mgr.created))
	}
	if mgr.created[0].Image != "img:v2" {
		t.Errorf("expected new pod with img:v2, got %s", mgr.created[0].Image)
	}
}

func TestReconcile_DriftDetection_SkipsDriftForJobMode(t *testing.T) {
	// Jobs use UpgradeSkip — don't kill running jobs for drift.
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "job", Role: "ops", AgentName: "task1"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("job-proj-ops-task1", "ns", "job", "proj", "ops", "task1", corev1.PodRunning),
		},
	}

	// Desired image differs, but jobs should not be restarted.
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v2"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions for job drift (UpgradeSkip), got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 0 {
		t.Errorf("expected 0 creations for job drift, got %d", len(mgr.created))
	}
}

func TestReconcile_NoDrift_WhenImageMatches(t *testing.T) {
	image := "ghcr.io/org/agent:v1"
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder(image))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions when image matches, got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 0 {
		t.Errorf("expected 0 creations when image matches, got %d", len(mgr.created))
	}
}

// --- Digest change / rolling upgrade tests ---

func TestReconcile_DigestDrift_TriggersRecreate(t *testing.T) {
	image := "ghcr.io/org/agent:latest"
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}

	pod := makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning)
	// Set container image to match desired (no tag drift).
	pod.Spec.Containers[0].Image = image
	// Set running digest in container status.
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:    "agent",
			ImageID: "ghcr.io/org/agent@sha256:olddigest111111111111",
		},
	}

	mgr := &mockManager{pods: []corev1.Pod{pod}}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder(image))

	// Pre-seed the digest tracker with a newer registry-confirmed digest.
	// First, record the old digest as baseline (simulates first observation).
	r.digestTracker.RecordDigest(image, "sha256:olddigest111111111111")
	// Then record a different digest from the registry (confirmed).
	r.digestTracker.RecordRegistryDigest(image, "sha256:newdigest222222222222")
	r.digestTracker.RecordRegistryDigest(image, "sha256:newdigest222222222222") // second confirm

	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 deletion for digest drift, got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 creation after digest drift, got %d", len(mgr.created))
	}
}

func TestReconcile_RollingUpgrade_OnlyOnePerMode(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
			{ID: "bd-2", Project: "proj", Mode: "crew", Role: "dev", AgentName: "beta"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
			makePod("crew-proj-dev-beta", "ns", "crew", "proj", "dev", "beta", corev1.PodRunning),
		},
	}

	// Both pods have old image, both need drift update.
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v2"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Rolling upgrade: only one crew pod should be upgraded per pass.
	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 deletion for rolling upgrade, got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 creation for rolling upgrade, got %d", len(mgr.created))
	}
}

// --- Error handling tests ---

func TestReconcile_DaemonUnreachable_ReturnsError(t *testing.T) {
	lister := &mockLister{err: errors.New("connection refused")}
	mgr := &mockManager{pods: nil}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error when daemon is unreachable")
	}
	if mgr.deleted != nil {
		t.Errorf("should not delete any pods when daemon is unreachable")
	}
	if mgr.created != nil {
		t.Errorf("should not create any pods when daemon is unreachable")
	}
}

func TestReconcile_PodListError_ReturnsError(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{listErr: errors.New("k8s API down")}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error when pod listing fails")
	}
}

func TestReconcile_PodCreateError_ReturnsError(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods:      nil,
		createErr: errors.New("quota exceeded"),
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error when pod creation fails")
	}
}

func TestReconcile_PodDeleteError_ReturnsError(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
			makePod("crew-proj-dev-orphan", "ns", "crew", "proj", "dev", "orphan", corev1.PodRunning),
		},
		deleteErr: errors.New("forbidden"),
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("ghcr.io/org/agent:v1"))
	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error when pod deletion fails")
	}
}

// --- Edge case tests ---

func TestReconcile_EmptyState_NoBeadsNoPods(t *testing.T) {
	lister := &mockLister{beads: []beadsapi.AgentBead{}}
	mgr := &mockManager{pods: []corev1.Pod{}}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mgr.created) != 0 || len(mgr.deleted) != 0 {
		t.Errorf("expected no-op for empty state, got %d created, %d deleted", len(mgr.created), len(mgr.deleted))
	}
}

func TestReconcile_SingleBeadSinglePod(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "solo"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-solo", "ns", "crew", "proj", "dev", "solo", corev1.PodRunning),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("ghcr.io/org/agent:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mgr.created) != 0 || len(mgr.deleted) != 0 {
		t.Errorf("expected no-op for converged single-pod state")
	}
}

func TestReconcile_IgnoresPodsWithoutAgentLabel(t *testing.T) {
	// Pods without the gasboat.io/agent label should be ignored (e.g., controller pod).
	lister := &mockLister{beads: nil}
	infraPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gasboat-controller-xyz",
			Namespace: "ns",
			Labels: map[string]string{
				podmanager.LabelApp: podmanager.LabelAppValue,
				// No LabelAgent — this is an infrastructure pod.
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	mgr := &mockManager{pods: []corev1.Pod{infraPod}}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Infrastructure pod should NOT be deleted despite not being in desired set.
	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions (infra pod should be ignored), got %d", len(mgr.deleted))
	}
}

func TestReconcile_BeadIDPassedToSpec(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-custom-id", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{pods: nil}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 pod created, got %d", len(mgr.created))
	}
	if mgr.created[0].BeadID != "bd-custom-id" {
		t.Errorf("expected BeadID bd-custom-id, got %s", mgr.created[0].BeadID)
	}
}

func TestReconcile_ConcurrencySafe(t *testing.T) {
	// Verify that concurrent Reconcile calls don't panic (mutex protection).
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{pods: nil}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))

	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			done <- r.Reconcile(context.Background())
		}()
	}

	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("concurrent reconcile returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent reconciles")
		}
	}
}

// --- podDriftReason unit tests ---

func TestPodDriftReason_NoChange(t *testing.T) {
	spec := podmanager.AgentPodSpec{Image: "img:v1"}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "agent", Image: "img:v1"}},
		},
	}
	reason := podDriftReason(spec, pod, nil)
	if reason != "" {
		t.Errorf("expected no drift, got: %s", reason)
	}
}

func TestPodDriftReason_ImageTagChanged(t *testing.T) {
	spec := podmanager.AgentPodSpec{Image: "img:v2"}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "agent", Image: "img:v1"}},
		},
	}
	reason := podDriftReason(spec, pod, nil)
	if reason == "" {
		t.Error("expected drift reason for image tag change")
	}
}

func TestPodDriftReason_EmptyDesiredImage(t *testing.T) {
	spec := podmanager.AgentPodSpec{Image: ""}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "agent", Image: "img:v1"}},
		},
	}
	reason := podDriftReason(spec, pod, nil)
	if reason != "" {
		t.Errorf("expected no drift when desired image is empty, got: %s", reason)
	}
}

func TestPodDriftReason_NoAgentContainer(t *testing.T) {
	spec := podmanager.AgentPodSpec{Image: "img:v2"}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sidecar", Image: "other:v1"}},
		},
	}
	// No "agent" container means agentChanged returns false.
	reason := podDriftReason(spec, pod, nil)
	if reason != "" {
		t.Errorf("expected no drift when no agent container, got: %s", reason)
	}
}
