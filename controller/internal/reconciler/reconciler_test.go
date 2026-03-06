package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

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
	pods             []corev1.Pod
	listErr          error
	createErr        error
	deleteErr        error
	created          []podmanager.AgentPodSpec
	deleted          []string // pod names
	getResult        *corev1.Pod
	getErr           error
	deleteAllCalled  bool
	deleteAllForce   bool
	deleteAllResult  int
	deleteAllErr     error
}

func (m *mockManager) CreateAgentPod(_ context.Context, spec podmanager.AgentPodSpec) error {
	m.created = append(m.created, spec)
	return m.createErr
}

func (m *mockManager) DeleteAgentPod(_ context.Context, name, _ string) error {
	m.deleted = append(m.deleted, name)
	return m.deleteErr
}

func (m *mockManager) DeleteAllAgentPods(_ context.Context, _ string, force bool) (int, error) {
	m.deleteAllCalled = true
	m.deleteAllForce = force
	return m.deleteAllResult, m.deleteAllErr
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

// ── Autodestruct ────────────────────────────────────────────────────────────

func TestAutodestruct(t *testing.T) {
	mgr := &mockManager{deleteAllResult: 5}
	lister := &mockLister{}
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))

	deleted, err := r.Autodestruct(context.Background(), "operator")
	if err != nil {
		t.Fatalf("Autodestruct: %v", err)
	}
	if deleted != 5 {
		t.Errorf("deleted = %d, want 5", deleted)
	}
	if !mgr.deleteAllCalled {
		t.Error("expected DeleteAllAgentPods to be called")
	}
	if !mgr.deleteAllForce {
		t.Error("expected force=true")
	}
	if !r.IsDestructed() {
		t.Error("expected IsDestructed() = true after Autodestruct")
	}
}

func TestReconcile_SkipsWhenDestructed(t *testing.T) {
	mgr := &mockManager{deleteAllResult: 0}
	lister := &mockLister{beads: []beadsapi.AgentBead{
		{ID: "b1", Project: "gasboat", Mode: "crew", Role: "dev", AgentName: "a1"},
	}}
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))

	// Activate autodestruct.
	_, _ = r.Autodestruct(context.Background(), "test")

	// Reconcile should be a no-op.
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(mgr.created) != 0 {
		t.Errorf("expected 0 pods created during autodestruct, got %d", len(mgr.created))
	}
}

func TestClearAutodestruct(t *testing.T) {
	mgr := &mockManager{deleteAllResult: 0}
	lister := &mockLister{}
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))

	_, _ = r.Autodestruct(context.Background(), "operator")
	if !r.IsDestructed() {
		t.Fatal("expected IsDestructed() = true")
	}

	r.ClearAutodestruct("operator")
	if r.IsDestructed() {
		t.Error("expected IsDestructed() = false after ClearAutodestruct")
	}
}

func TestIsDestructed_DefaultFalse(t *testing.T) {
	mgr := &mockManager{}
	lister := &mockLister{}
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))

	if r.IsDestructed() {
		t.Error("expected IsDestructed() = false by default")
	}
}
