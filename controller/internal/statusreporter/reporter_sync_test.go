package statusreporter

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"gasboat/controller/internal/podmanager"
)

// --- SyncAll helpers ---

func makePod(name, ns string, phase corev1.PodPhase, labels map[string]string, ip string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "agent"},
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
			PodIP: ip,
		},
	}
}

func agentLabels(project, role, agent string) map[string]string {
	return map[string]string{
		podmanager.LabelApp:     podmanager.LabelAppValue,
		podmanager.LabelProject: project,
		podmanager.LabelRole:    role,
		podmanager.LabelAgent:   agent,
		podmanager.LabelMode:    "crew",
	}
}

// --- SyncAll tests ---

func TestSyncAll_ReportsStatusForAgentPods(t *testing.T) {
	pod := makePod("crew-proj-dev-alpha", "ns", corev1.PodRunning,
		agentLabels("proj", "dev", "alpha"), "10.0.0.1")
	client := fake.NewSimpleClientset(pod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(daemon.stateCalls) != 1 {
		t.Fatalf("expected 1 state update, got %d", len(daemon.stateCalls))
	}
	if daemon.stateCalls[0].state != "working" {
		t.Errorf("expected state working, got %s", daemon.stateCalls[0].state)
	}
}

func TestSyncAll_SkipsPodsMissingLabels(t *testing.T) {
	// Pod has gasboat app label but missing agent/project/role labels.
	pod := makePod("gasboat-controller-xyz", "ns", corev1.PodRunning,
		map[string]string{
			podmanager.LabelApp: podmanager.LabelAppValue,
		}, "10.0.0.1")
	client := fake.NewSimpleClientset(pod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(daemon.stateCalls) != 0 {
		t.Errorf("expected 0 state updates for pod without agent labels, got %d", len(daemon.stateCalls))
	}
}

func TestSyncAll_ReportsBackendMetadataForCoopPods(t *testing.T) {
	labels := agentLabels("proj", "dev", "alpha")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crew-proj-dev-alpha",
			Namespace: "ns",
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "coop",
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8080},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.5",
		},
	}
	client := fake.NewSimpleClientset(pod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(daemon.notesCalls) != 1 {
		t.Fatalf("expected 1 notes update for coop pod, got %d", len(daemon.notesCalls))
	}
	notes := daemon.notesCalls[0].notes
	if !strings.Contains(notes, "coop_url: http://10.0.0.5:8080") {
		t.Errorf("expected coop_url with pod IP, got: %s", notes)
	}
}

func TestSyncAll_NoMetadataForNonCoopPods(t *testing.T) {
	pod := makePod("crew-proj-dev-alpha", "ns", corev1.PodRunning,
		agentLabels("proj", "dev", "alpha"), "10.0.0.1")
	client := fake.NewSimpleClientset(pod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(daemon.notesCalls) != 0 {
		t.Errorf("expected 0 notes updates for non-coop pod, got %d", len(daemon.notesCalls))
	}
}

func TestSyncAll_NoMetadataForCoopPodWithoutIP(t *testing.T) {
	labels := agentLabels("proj", "dev", "alpha")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crew-proj-dev-alpha",
			Namespace: "ns",
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "coop",
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8080},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			PodIP: "", // no IP yet
		},
	}
	client := fake.NewSimpleClientset(pod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(daemon.notesCalls) != 0 {
		t.Errorf("expected 0 notes updates for coop pod without IP, got %d", len(daemon.notesCalls))
	}
}

func TestSyncAll_MultiplePods(t *testing.T) {
	pod1 := makePod("crew-proj-dev-alpha", "ns", corev1.PodRunning,
		agentLabels("proj", "dev", "alpha"), "10.0.0.1")
	pod2 := makePod("crew-proj-dev-beta", "ns", corev1.PodPending,
		agentLabels("proj", "dev", "beta"), "")
	client := fake.NewSimpleClientset(pod1, pod2)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(daemon.stateCalls) != 2 {
		t.Fatalf("expected 2 state updates, got %d", len(daemon.stateCalls))
	}

	// Verify we see both working and spawning states.
	states := map[string]bool{}
	for _, c := range daemon.stateCalls {
		states[c.state] = true
	}
	if !states["working"] || !states["spawning"] {
		t.Errorf("expected working and spawning states, got calls: %+v", daemon.stateCalls)
	}
}

func TestSyncAll_NoPods(t *testing.T) {
	client := fake.NewSimpleClientset()
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(daemon.stateCalls) != 0 {
		t.Errorf("expected 0 state updates for no pods, got %d", len(daemon.stateCalls))
	}
}

func TestSyncAll_StatusReportError_ContinuesProcessing(t *testing.T) {
	// Even if one pod's status report fails, SyncAll should continue with others.
	pod1 := makePod("crew-proj-dev-alpha", "ns", corev1.PodRunning,
		agentLabels("proj", "dev", "alpha"), "10.0.0.1")
	pod2 := makePod("crew-proj-dev-beta", "ns", corev1.PodRunning,
		agentLabels("proj", "dev", "beta"), "10.0.0.2")

	callCount := 0
	daemon := &mockBeadUpdater{}
	// Override to fail on first call only.
	origErr := errors.New("transient")
	daemon.stateErr = origErr

	client := fake.NewSimpleClientset(pod1, pod2)
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	// SyncAll does not return errors for individual pod failures.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = callCount

	// Both pods should have been attempted.
	if len(daemon.stateCalls) != 2 {
		t.Errorf("expected 2 state update attempts, got %d", len(daemon.stateCalls))
	}
}

func TestSyncAll_MetricsTracking(t *testing.T) {
	pod := makePod("crew-proj-dev-alpha", "ns", corev1.PodRunning,
		agentLabels("proj", "dev", "alpha"), "10.0.0.1")
	client := fake.NewSimpleClientset(pod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	_ = r.SyncAll(context.Background())

	m := r.Metrics()
	if m.SyncAllRuns != 1 {
		t.Errorf("expected 1 SyncAllRuns, got %d", m.SyncAllRuns)
	}
	if m.SyncAllErrors != 0 {
		t.Errorf("expected 0 SyncAllErrors, got %d", m.SyncAllErrors)
	}
	if m.StatusReportsTotal != 1 {
		t.Errorf("expected 1 StatusReportsTotal, got %d", m.StatusReportsTotal)
	}
}

func TestSyncAll_UsesBeadIDFromAnnotation(t *testing.T) {
	labels := agentLabels("proj", "dev", "alpha")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crew-proj-dev-alpha",
			Namespace: "ns",
			Labels:    labels,
			Annotations: map[string]string{
				podmanager.AnnotationBeadID: "kd-custom-bead",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "agent"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.1",
		},
	}
	client := fake.NewSimpleClientset(pod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	_ = r.SyncAll(context.Background())

	if len(daemon.stateCalls) != 1 {
		t.Fatalf("expected 1 state call, got %d", len(daemon.stateCalls))
	}
	if daemon.stateCalls[0].beadID != "kd-custom-bead" {
		t.Errorf("expected bead ID from annotation, got %s", daemon.stateCalls[0].beadID)
	}
}

// --- SyncAll label selector test ---

func TestSyncAll_OnlyMatchesGasboatPods(t *testing.T) {
	// A pod in the same namespace but without the gasboat app label should not be listed.
	gasboatPod := makePod("crew-proj-dev-alpha", "ns", corev1.PodRunning,
		agentLabels("proj", "dev", "alpha"), "10.0.0.1")
	otherPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-other-pod",
			Namespace: "ns",
			Labels: map[string]string{
				"app": "something-else",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewSimpleClientset(gasboatPod, otherPod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only the gasboat pod should trigger a state update.
	if len(daemon.stateCalls) != 1 {
		t.Errorf("expected 1 state update (only gasboat pod), got %d", len(daemon.stateCalls))
	}
}

// --- Integration-style: SyncAll with coop metadata ---

func TestSyncAll_CoopMetadataUsesCorrectURL(t *testing.T) {
	labels := agentLabels("gasboat", "crew", "furiosa")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crew-gasboat-crew-furiosa",
			Namespace: "ns",
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "agent"},
				{
					Name: "coop",
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8080},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "172.16.0.42",
		},
	}
	client := fake.NewSimpleClientset(pod)
	daemon := &mockBeadUpdater{}
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	_ = r.SyncAll(context.Background())

	if len(daemon.notesCalls) != 1 {
		t.Fatalf("expected 1 notes call, got %d", len(daemon.notesCalls))
	}

	expectedURL := "coop_url: http://172.16.0.42:8080"
	if !strings.Contains(daemon.notesCalls[0].notes, expectedURL) {
		t.Errorf("expected notes to contain %s, got: %s", expectedURL, daemon.notesCalls[0].notes)
	}

	// beadID should be constructed from labels since no annotation.
	if daemon.notesCalls[0].beadID != "crew-gasboat-crew-furiosa" {
		t.Errorf("expected beadID crew-gasboat-crew-furiosa, got %s", daemon.notesCalls[0].beadID)
	}
}
