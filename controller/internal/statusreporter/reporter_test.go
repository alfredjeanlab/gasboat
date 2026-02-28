package statusreporter

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"gasboat/controller/internal/podmanager"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- Mock implementations ---

type mockBeadUpdater struct {
	notesCalls []noteCall
	stateCalls []stateCall
	notesErr   error
	stateErr   error
}

type noteCall struct {
	beadID string
	notes  string
}

type stateCall struct {
	beadID string
	state  string
}

func (m *mockBeadUpdater) UpdateBeadNotes(_ context.Context, beadID, notes string) error {
	m.notesCalls = append(m.notesCalls, noteCall{beadID: beadID, notes: notes})
	return m.notesErr
}

func (m *mockBeadUpdater) UpdateAgentState(_ context.Context, beadID, state string) error {
	m.stateCalls = append(m.stateCalls, stateCall{beadID: beadID, state: state})
	return m.stateErr
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// --- PhaseToAgentState tests ---

func TestPhaseToAgentState(t *testing.T) {
	tests := []struct {
		phase string
		want  string
	}{
		{"Pending", "spawning"},
		{"Running", "working"},
		{"Succeeded", "done"},
		{"Failed", "failed"},
		{"Stopped", "done"},
		{"Unknown", ""},
		{"", ""},
		{"SomethingElse", ""},
	}
	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			got := PhaseToAgentState(tt.phase)
			if got != tt.want {
				t.Errorf("PhaseToAgentState(%q) = %q, want %q", tt.phase, got, tt.want)
			}
		})
	}
}

// --- agentBeadID tests ---

func TestAgentBeadID_FromAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				podmanager.AnnotationBeadID: "kd-explicit-id",
			},
			Labels: map[string]string{
				podmanager.LabelProject: "proj",
				podmanager.LabelRole:    "dev",
				podmanager.LabelAgent:   "alpha",
				podmanager.LabelMode:    "crew",
			},
		},
	}
	got := agentBeadID(pod)
	if got != "kd-explicit-id" {
		t.Errorf("expected annotation-based ID kd-explicit-id, got %s", got)
	}
}

func TestAgentBeadID_FromLabels(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				podmanager.LabelProject: "gasboat",
				podmanager.LabelRole:    "crew",
				podmanager.LabelAgent:   "furiosa",
				podmanager.LabelMode:    "crew",
			},
		},
	}
	got := agentBeadID(pod)
	if got != "crew-gasboat-crew-furiosa" {
		t.Errorf("expected crew-gasboat-crew-furiosa, got %s", got)
	}
}

func TestAgentBeadID_DefaultMode(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				podmanager.LabelProject: "proj",
				podmanager.LabelRole:    "dev",
				podmanager.LabelAgent:   "beta",
				// No LabelMode — defaults to "crew"
			},
		},
	}
	got := agentBeadID(pod)
	if got != "crew-proj-dev-beta" {
		t.Errorf("expected crew-proj-dev-beta, got %s", got)
	}
}

// --- detectCoopPort tests ---

func TestDetectCoopPort_CoopContainerWithPort(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "agent",
					Ports: []corev1.ContainerPort{
						{ContainerPort: 3000},
					},
				},
				{
					Name: "coop",
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8080},
					},
				},
			},
		},
	}
	got := detectCoopPort(pod)
	if got != 8080 {
		t.Errorf("expected 8080 for coop container with port 8080, got %d", got)
	}
}

func TestDetectCoopPort_CoopContainerNoPort(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "coop"}, // no ports defined
			},
		},
	}
	got := detectCoopPort(pod)
	if got != 8080 {
		t.Errorf("expected default 8080 for coop container without ports, got %d", got)
	}
}

func TestDetectCoopPort_NonCoopContainerWith8080(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "agent",
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8080},
					},
				},
			},
		},
	}
	got := detectCoopPort(pod)
	if got != 8080 {
		t.Errorf("expected 8080 for non-coop container exposing 8080, got %d", got)
	}
}

func TestDetectCoopPort_NoCoopCapability(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "agent",
					Ports: []corev1.ContainerPort{
						{ContainerPort: 3000},
					},
				},
			},
		},
	}
	got := detectCoopPort(pod)
	if got != 0 {
		t.Errorf("expected 0 for pod without coop capability, got %d", got)
	}
}

func TestDetectCoopPort_NoContainers(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{},
	}
	got := detectCoopPort(pod)
	if got != 0 {
		t.Errorf("expected 0 for pod with no containers, got %d", got)
	}
}

func TestDetectCoopPort_CoopContainerWithDifferentPort(t *testing.T) {
	// Coop container exists but exposes a non-8080 port — returns default 8080.
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "coop",
					Ports: []corev1.ContainerPort{
						{ContainerPort: 9090},
					},
				},
			},
		},
	}
	got := detectCoopPort(pod)
	if got != 8080 {
		t.Errorf("expected default 8080 for coop container with non-8080 port, got %d", got)
	}
}

// --- NewHTTPReporter tests ---

func TestNewHTTPReporter(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "test-ns", testLogger())
	if r == nil {
		t.Fatal("expected non-nil reporter")
	}
	if r.namespace != "test-ns" {
		t.Errorf("expected namespace test-ns, got %s", r.namespace)
	}
}

// --- ReportPodStatus tests ---

func TestReportPodStatus_Running(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.ReportPodStatus(context.Background(), "agent-1", PodStatus{
		PodName: "pod-1", Namespace: "ns", Phase: "Running", Ready: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(daemon.stateCalls) != 1 {
		t.Fatalf("expected 1 state update, got %d", len(daemon.stateCalls))
	}
	if daemon.stateCalls[0].beadID != "agent-1" {
		t.Errorf("expected beadID agent-1, got %s", daemon.stateCalls[0].beadID)
	}
	if daemon.stateCalls[0].state != "working" {
		t.Errorf("expected state working, got %s", daemon.stateCalls[0].state)
	}
}

func TestReportPodStatus_UnknownPhase_Skips(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.ReportPodStatus(context.Background(), "agent-1", PodStatus{
		PodName: "pod-1", Namespace: "ns", Phase: "Unknown",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(daemon.stateCalls) != 0 {
		t.Errorf("expected 0 state updates for unknown phase, got %d", len(daemon.stateCalls))
	}
}

func TestReportPodStatus_DaemonError(t *testing.T) {
	daemon := &mockBeadUpdater{stateErr: errors.New("daemon unreachable")}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.ReportPodStatus(context.Background(), "agent-1", PodStatus{
		PodName: "pod-1", Namespace: "ns", Phase: "Running",
	})
	if err == nil {
		t.Fatal("expected error when daemon fails")
	}
	if !strings.Contains(err.Error(), "reporting status for agent-1") {
		t.Errorf("expected wrapped error mentioning agent, got: %v", err)
	}
}

func TestReportPodStatus_AllPhases(t *testing.T) {
	phases := []struct {
		phase string
		state string
	}{
		{"Pending", "spawning"},
		{"Running", "working"},
		{"Succeeded", "done"},
		{"Failed", "failed"},
		{"Stopped", "done"},
	}
	for _, tt := range phases {
		t.Run(tt.phase, func(t *testing.T) {
			daemon := &mockBeadUpdater{}
			client := fake.NewSimpleClientset()
			r := NewHTTPReporter(daemon, client, "ns", testLogger())

			err := r.ReportPodStatus(context.Background(), "agent-1", PodStatus{
				PodName: "pod-1", Phase: tt.phase,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(daemon.stateCalls) != 1 {
				t.Fatalf("expected 1 call, got %d", len(daemon.stateCalls))
			}
			if daemon.stateCalls[0].state != tt.state {
				t.Errorf("expected state %s, got %s", tt.state, daemon.stateCalls[0].state)
			}
		})
	}
}

// --- ReportBackendMetadata tests ---

func TestReportBackendMetadata_FullMetadata(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.ReportBackendMetadata(context.Background(), "agent-1", BackendMetadata{
		PodName:   "pod-1",
		Namespace: "ns",
		Backend:   "coop",
		CoopURL:   "http://pod-1.ns.svc:8080",
		CoopToken: "tok123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(daemon.notesCalls) != 1 {
		t.Fatalf("expected 1 notes update, got %d", len(daemon.notesCalls))
	}
	notes := daemon.notesCalls[0].notes
	for _, expected := range []string{
		"backend: coop",
		"pod_name: pod-1",
		"pod_namespace: ns",
		"coop_url: http://pod-1.ns.svc:8080",
		"coop_token: tok123",
	} {
		if !strings.Contains(notes, expected) {
			t.Errorf("notes missing %q, got: %s", expected, notes)
		}
	}
}

func TestReportBackendMetadata_PartialMetadata(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.ReportBackendMetadata(context.Background(), "agent-1", BackendMetadata{
		Backend: "coop",
		CoopURL: "http://pod-1:8080",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(daemon.notesCalls) != 1 {
		t.Fatalf("expected 1 notes update, got %d", len(daemon.notesCalls))
	}
	notes := daemon.notesCalls[0].notes
	if strings.Contains(notes, "pod_name:") {
		t.Errorf("notes should not contain pod_name for empty PodName, got: %s", notes)
	}
	if !strings.Contains(notes, "backend: coop") {
		t.Errorf("notes missing backend, got: %s", notes)
	}
}

func TestReportBackendMetadata_EmptyMetadata_NoOp(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.ReportBackendMetadata(context.Background(), "agent-1", BackendMetadata{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(daemon.notesCalls) != 0 {
		t.Errorf("expected 0 notes updates for empty metadata, got %d", len(daemon.notesCalls))
	}
}

func TestReportBackendMetadata_DaemonError(t *testing.T) {
	daemon := &mockBeadUpdater{notesErr: errors.New("daemon down")}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	err := r.ReportBackendMetadata(context.Background(), "agent-1", BackendMetadata{
		Backend: "coop",
	})
	if err == nil {
		t.Fatal("expected error when daemon fails")
	}
	if !strings.Contains(err.Error(), "reporting backend metadata for agent-1") {
		t.Errorf("expected wrapped error mentioning agent, got: %v", err)
	}
}

// --- Metrics tests ---

func TestMetrics_InitialState(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	m := r.Metrics()
	if m.StatusReportsTotal != 0 || m.StatusReportErrors != 0 ||
		m.SyncAllRuns != 0 || m.SyncAllErrors != 0 {
		t.Errorf("expected all zeros initially, got %+v", m)
	}
}

func TestMetrics_AfterReports(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	// Successful status report
	_ = r.ReportPodStatus(context.Background(), "a", PodStatus{Phase: "Running"})
	// Successful metadata report
	_ = r.ReportBackendMetadata(context.Background(), "a", BackendMetadata{Backend: "coop"})

	m := r.Metrics()
	if m.StatusReportsTotal != 2 {
		t.Errorf("expected 2 total reports, got %d", m.StatusReportsTotal)
	}
	if m.StatusReportErrors != 0 {
		t.Errorf("expected 0 errors, got %d", m.StatusReportErrors)
	}
}

func TestMetrics_AfterErrors(t *testing.T) {
	daemon := &mockBeadUpdater{stateErr: errors.New("fail")}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	_ = r.ReportPodStatus(context.Background(), "a", PodStatus{Phase: "Running"})

	m := r.Metrics()
	if m.StatusReportsTotal != 1 {
		t.Errorf("expected 1 total report, got %d", m.StatusReportsTotal)
	}
	if m.StatusReportErrors != 1 {
		t.Errorf("expected 1 error, got %d", m.StatusReportErrors)
	}
}

func TestMetrics_SkippedPhaseStillCounts(t *testing.T) {
	daemon := &mockBeadUpdater{}
	client := fake.NewSimpleClientset()
	r := NewHTTPReporter(daemon, client, "ns", testLogger())

	// Unknown phase — skipped, but still counted as a report
	_ = r.ReportPodStatus(context.Background(), "a", PodStatus{Phase: "Unknown"})

	m := r.Metrics()
	if m.StatusReportsTotal != 1 {
		t.Errorf("expected 1 total report (even for skipped phase), got %d", m.StatusReportsTotal)
	}
	if m.StatusReportErrors != 0 {
		t.Errorf("expected 0 errors for skipped phase, got %d", m.StatusReportErrors)
	}
}
