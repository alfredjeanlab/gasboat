package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// overrideStopGateCooldownFile sets the cooldown file to a test path.
func overrideStopGateCooldownFile(path string) {
	stopGateCooldownFile = path
}

// restoreStopGateCooldownFile restores the original cooldown file path.
func restoreStopGateCooldownFile(original string) {
	stopGateCooldownFile = original
}

func TestStopGateInCooldown_NoFile(t *testing.T) {
	// Ensure the cooldown file doesn't exist.
	old := stopGateCooldownFile
	t.Cleanup(func() { restoreStopGateCooldownFile(old) })
	overrideStopGateCooldownFile(filepath.Join(t.TempDir(), "no-such-file"))

	if stopGateInCooldown() {
		t.Error("stopGateInCooldown() = true, want false when file does not exist")
	}
}

func TestStopGateInCooldown_RecentBlock(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "last-block")
	old := stopGateCooldownFile
	t.Cleanup(func() { restoreStopGateCooldownFile(old) })
	overrideStopGateCooldownFile(f)

	// Write a recent timestamp (now).
	if err := os.WriteFile(f, []byte(fmt.Sprintf("%d\n", time.Now().Unix())), 0o644); err != nil {
		t.Fatal(err)
	}

	if !stopGateInCooldown() {
		t.Error("stopGateInCooldown() = false, want true for recent block")
	}
}

func TestStopGateInCooldown_ExpiredBlock(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "last-block")
	old := stopGateCooldownFile
	t.Cleanup(func() { restoreStopGateCooldownFile(old) })
	overrideStopGateCooldownFile(f)

	// Write an old timestamp (60 seconds ago, cooldown is 30s by default).
	oldTime := time.Now().Unix() - 60
	if err := os.WriteFile(f, []byte(fmt.Sprintf("%d\n", oldTime)), 0o644); err != nil {
		t.Fatal(err)
	}

	if stopGateInCooldown() {
		t.Error("stopGateInCooldown() = true, want false for expired block")
	}
}

func TestStopGateInCooldown_CustomCooldown(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "last-block")
	old := stopGateCooldownFile
	t.Cleanup(func() { restoreStopGateCooldownFile(old) })
	overrideStopGateCooldownFile(f)

	// Block happened 10 seconds ago.
	blockTime := time.Now().Unix() - 10
	if err := os.WriteFile(f, []byte(fmt.Sprintf("%d\n", blockTime)), 0o644); err != nil {
		t.Fatal(err)
	}

	// With default 30s cooldown, should still be in cooldown.
	if !stopGateInCooldown() {
		t.Error("stopGateInCooldown() = false, want true (10s ago, 30s cooldown)")
	}

	// Override cooldown to 5 seconds — should now be expired.
	t.Setenv("STOP_GATE_COOLDOWN_SECS", "5")
	if stopGateInCooldown() {
		t.Error("stopGateInCooldown() = true, want false (10s ago, 5s cooldown)")
	}
}

func TestRecordAndClearStopGateBlock(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "last-block")
	old := stopGateCooldownFile
	t.Cleanup(func() { restoreStopGateCooldownFile(old) })
	overrideStopGateCooldownFile(f)

	// Initially no file.
	if _, err := os.Stat(f); err == nil {
		t.Fatal("cooldown file should not exist initially")
	}

	// Record a block.
	recordStopGateBlock()
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("cooldown file should exist after recording: %v", err)
	}

	// Should be in cooldown now.
	if !stopGateInCooldown() {
		t.Error("should be in cooldown after recording block")
	}

	// Clear.
	clearStopGateCooldown()
	if _, err := os.Stat(f); err == nil {
		t.Error("cooldown file should not exist after clearing")
	}
	if stopGateInCooldown() {
		t.Error("should not be in cooldown after clearing")
	}
}

func TestStopGateInCooldown_InvalidContent(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "last-block")
	old := stopGateCooldownFile
	t.Cleanup(func() { restoreStopGateCooldownFile(old) })
	overrideStopGateCooldownFile(f)

	// Write garbage.
	if err := os.WriteFile(f, []byte("not-a-number\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if stopGateInCooldown() {
		t.Error("stopGateInCooldown() = true, want false for invalid content")
	}
}

func TestInjectStopGateText_CustomFile(t *testing.T) {
	tmp := t.TempDir()
	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	textFile := filepath.Join(claudeDir, "stop-gate-text.md")
	customText := "<system-reminder>Custom stop gate text</system-reminder>\n"
	if err := os.WriteFile(textFile, []byte(customText), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", tmp)

	// Capture stdout.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	injectStopGateText()

	w.Close()
	out := make([]byte, 4096)
	n, _ := r.Read(out)
	os.Stdout = oldStdout

	got := string(out[:n])
	if got != customText {
		t.Errorf("injectStopGateText() = %q, want %q", got, customText)
	}
}

func TestInjectStopGateText_Fallback(t *testing.T) {
	// Set HOME to a dir with no .claude/stop-gate-text.md.
	t.Setenv("HOME", t.TempDir())

	// Capture stdout.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	injectStopGateText()

	w.Close()
	out := make([]byte, 8192)
	n, _ := r.Read(out)
	os.Stdout = oldStdout

	got := string(out[:n])
	if got == "" {
		t.Error("injectStopGateText() should produce fallback text")
	}
	if len(got) < 50 {
		t.Errorf("fallback text too short: %q", got)
	}
}
