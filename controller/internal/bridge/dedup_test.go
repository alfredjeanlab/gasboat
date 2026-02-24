package bridge

import (
	"context"
	"log/slog"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

func TestDedup_Seen(t *testing.T) {
	d := NewDedup(slog.Default())

	// First call: not seen.
	if d.Seen("created:dec-1") {
		t.Fatal("expected first call to return false")
	}

	// Second call: already seen.
	if !d.Seen("created:dec-1") {
		t.Fatal("expected second call to return true")
	}
}

func TestDedup_Mark(t *testing.T) {
	d := NewDedup(slog.Default())

	d.Mark("resolved:dec-2")

	// Now Seen should return true.
	if !d.Seen("resolved:dec-2") {
		t.Fatal("expected Seen to return true after Mark")
	}
}

func TestDedup_DifferentKeys(t *testing.T) {
	d := NewDedup(slog.Default())

	// Different prefixes for same bead should be independent.
	if d.Seen("created:dec-3") {
		t.Fatal("expected created key to be unseen")
	}
	if d.Seen("resolved:dec-3") {
		t.Fatal("expected resolved key to be unseen (different prefix)")
	}

	// Original key should now be seen.
	if !d.Seen("created:dec-3") {
		t.Fatal("expected created key to be seen after first Seen call")
	}
}

func TestDedup_CatchUpDecisions_Empty(t *testing.T) {
	d := NewDedup(slog.Default())
	daemon := newMockDaemon()
	notif := &mockNotifier{}

	d.CatchUpDecisions(context.Background(), daemon, notif, slog.Default())

	// No decisions to catch up.
	if len(notif.getCreated()) != 0 {
		t.Fatal("expected no notifications for empty daemon")
	}
}

func TestDedup_CatchUpDecisions_SkipsResolved(t *testing.T) {
	d := NewDedup(slog.Default())
	daemon := newMockDaemon()
	daemon.beads["dec-resolved"] = &beadsapi.BeadDetail{
		ID:   "dec-resolved",
		Type: "decision",
		Fields: map[string]string{
			"question": "Deploy?",
			"chosen":   "yes",
		},
	}
	notif := &mockNotifier{}

	d.CatchUpDecisions(context.Background(), daemon, notif, slog.Default())

	// Resolved decisions should not be notified.
	if len(notif.getCreated()) != 0 {
		t.Fatal("expected no notifications for resolved decisions")
	}

	// But should be marked as seen.
	if !d.Seen("resolved:dec-resolved") {
		t.Fatal("expected resolved decision to be marked")
	}
}

func TestDedup_CatchUpDecisions_NilDaemon(t *testing.T) {
	d := NewDedup(slog.Default())

	// Should not panic with nil daemon.
	d.CatchUpDecisions(context.Background(), nil, nil, slog.Default())
}

func TestDedup_CatchUpDecisions_PrePopulatesDedup(t *testing.T) {
	d := NewDedup(slog.Default())
	daemon := newMockDaemon()
	daemon.beads["dec-pending"] = &beadsapi.BeadDetail{
		ID:   "dec-pending",
		Type: "decision",
		Fields: map[string]string{
			"question": "Deploy?",
		},
	}

	// Catch up with nil notifier (just mark as seen).
	d.CatchUpDecisions(context.Background(), daemon, nil, slog.Default())

	// Should be marked as seen.
	if !d.Seen("created:dec-pending") {
		t.Fatal("expected pending decision to be marked after catch-up")
	}
}
