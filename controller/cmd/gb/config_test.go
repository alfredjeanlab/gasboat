package main

import (
	"context"
	"encoding/json"
	"fmt"
	"gasboat/controller/internal/beadsapi"
	"os"
	"path/filepath"
	"testing"
)

// --- Mock implementations ---

type mockDumper struct {
	configs map[string][]beadsapi.ConfigEntry
	err     error
}

func (m *mockDumper) ListConfigs(_ context.Context, namespace string) ([]beadsapi.ConfigEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.configs[namespace], nil
}

type mockBeadLister struct {
	beads []*beadsapi.BeadDetail
	err   error
}

func (m *mockBeadLister) ListBeadsFiltered(_ context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &beadsapi.ListBeadsResult{Beads: m.beads, Total: len(m.beads)}, nil
}

type mockLoader struct {
	stored map[string]json.RawMessage
	err    error
}

func (m *mockLoader) SetConfig(_ context.Context, key string, value []byte) error {
	if m.err != nil {
		return m.err
	}
	m.stored[key] = json.RawMessage(value)
	return nil
}

type failFirstDumper struct {
	inner  *mockDumper
	failNs string
}

func (f *failFirstDumper) ListConfigs(ctx context.Context, namespace string) ([]beadsapi.ConfigEntry, error) {
	if namespace == f.failNs {
		return nil, fmt.Errorf("connection refused")
	}
	return f.inner.ListConfigs(ctx, namespace)
}

// --- Config dump tests ---

func TestDumpConfigs_CollectsAllNamespaces(t *testing.T) {
	dumper := &mockDumper{
		configs: map[string][]beadsapi.ConfigEntry{
			"claude-settings": {
				{Key: "claude-settings:global", Value: json.RawMessage(`{"model":"opus"}`)},
			},
			"claude-mcp": {
				{Key: "claude-mcp:global", Value: json.RawMessage(`{"mcpServers":{}}`)},
			},
			"type": {
				{Key: "type:agent", Value: json.RawMessage(`{"kind":"agent"}`)},
				{Key: "type:task", Value: json.RawMessage(`{"kind":"task"}`)},
			},
		},
	}

	entries, err := dumpConfigs(context.Background(), dumper, []string{"claude-settings", "claude-mcp", "type"})
	if err != nil {
		t.Fatalf("dumpConfigs: %v", err)
	}

	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}
}

func TestDumpConfigs_SortsByKey(t *testing.T) {
	dumper := &mockDumper{
		configs: map[string][]beadsapi.ConfigEntry{
			"b-ns": {{Key: "b-ns:zebra", Value: json.RawMessage(`{}`)}},
			"a-ns": {{Key: "a-ns:alpha", Value: json.RawMessage(`{}`)}},
		},
	}

	entries, err := dumpConfigs(context.Background(), dumper, []string{"b-ns", "a-ns"})
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2, got %d", len(entries))
	}
	if entries[0].Key != "a-ns:alpha" {
		t.Errorf("expected first key a-ns:alpha, got %s", entries[0].Key)
	}
	if entries[1].Key != "b-ns:zebra" {
		t.Errorf("expected second key b-ns:zebra, got %s", entries[1].Key)
	}
}

func TestDumpConfigs_EmptyNamespace(t *testing.T) {
	dumper := &mockDumper{configs: map[string][]beadsapi.ConfigEntry{}}
	entries, err := dumpConfigs(context.Background(), dumper, []string{"claude-settings"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestDumpConfigs_ContinuesOnError(t *testing.T) {
	dumper := &mockDumper{
		configs: map[string][]beadsapi.ConfigEntry{
			"good-ns": {{Key: "good-ns:one", Value: json.RawMessage(`{}`)}},
		},
	}
	wrapper := &failFirstDumper{inner: dumper, failNs: "bad-ns"}

	entries, err := dumpConfigs(context.Background(), wrapper, []string{"bad-ns", "good-ns"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (skipping failed ns), got %d", len(entries))
	}
	if entries[0].Key != "good-ns:one" {
		t.Errorf("expected good-ns:one, got %s", entries[0].Key)
	}
}

// --- Advice dump tests ---

func TestDumpAdvice_CollectsAndSorts(t *testing.T) {
	lister := &mockBeadLister{
		beads: []*beadsapi.BeadDetail{
			{Title: "Zebra rule", Description: "z desc", Labels: []string{"global"}, Priority: 1},
			{Title: "Alpha rule", Description: "a desc", Labels: []string{"role:captain"}, Priority: 2},
		},
	}

	entries, err := dumpAdvice(context.Background(), lister)
	if err != nil {
		t.Fatalf("dumpAdvice: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2, got %d", len(entries))
	}
	if entries[0].Title != "Alpha rule" {
		t.Errorf("expected sorted, first=%s", entries[0].Title)
	}
	if entries[1].Title != "Zebra rule" {
		t.Errorf("expected sorted, second=%s", entries[1].Title)
	}
	if entries[0].Description != "a desc" {
		t.Errorf("wrong description: %s", entries[0].Description)
	}
}

func TestDumpAdvice_Empty(t *testing.T) {
	lister := &mockBeadLister{beads: []*beadsapi.BeadDetail{}}
	entries, err := dumpAdvice(context.Background(), lister)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0, got %d", len(entries))
	}
}

func TestDumpAdvice_Error(t *testing.T) {
	lister := &mockBeadLister{err: fmt.Errorf("server down")}
	_, err := dumpAdvice(context.Background(), lister)
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Config load tests ---

func TestLoadConfigs_RestoresAll(t *testing.T) {
	loader := &mockLoader{stored: make(map[string]json.RawMessage)}
	entries := []configEntry{
		{Key: "claude-settings:global", Value: json.RawMessage(`{"model":"opus"}`)},
		{Key: "claude-mcp:global", Value: json.RawMessage(`{"mcpServers":{}}`)},
	}

	restored, errors := loadConfigs(context.Background(), loader, entries)
	if restored != 2 || errors != 0 {
		t.Errorf("expected 2/0, got %d/%d", restored, errors)
	}
	if string(loader.stored["claude-settings:global"]) != `{"model":"opus"}` {
		t.Errorf("wrong value: %s", loader.stored["claude-settings:global"])
	}
}

func TestLoadConfigs_ContinuesOnError(t *testing.T) {
	loader := &mockLoader{stored: make(map[string]json.RawMessage), err: fmt.Errorf("fail")}
	entries := []configEntry{
		{Key: "a", Value: json.RawMessage(`{}`)},
		{Key: "b", Value: json.RawMessage(`{}`)},
	}

	restored, errors := loadConfigs(context.Background(), loader, entries)
	if restored != 0 || errors != 2 {
		t.Errorf("expected 0/2, got %d/%d", restored, errors)
	}
}

// --- Roundtrip test ---

func TestDumpLoad_Roundtrip(t *testing.T) {
	dumper := &mockDumper{
		configs: map[string][]beadsapi.ConfigEntry{
			"ns": {
				{Key: "ns:one", Value: json.RawMessage(`{"a":1}`)},
				{Key: "ns:two", Value: json.RawMessage(`{"b":2}`)},
			},
		},
	}
	lister := &mockBeadLister{
		beads: []*beadsapi.BeadDetail{
			{Title: "Rule one", Description: "do this", Labels: []string{"global"}, Priority: 1},
		},
	}

	// Dump.
	configs, _ := dumpConfigs(context.Background(), dumper, []string{"ns"})
	advice, _ := dumpAdvice(context.Background(), lister)

	dump := configDump{Configs: configs, Advice: advice}
	data, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	// Write to file and read back.
	tmpFile := filepath.Join(t.TempDir(), "dump.json")
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	readData, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	var loaded configDump
	if err := json.Unmarshal(readData, &loaded); err != nil {
		t.Fatal(err)
	}

	// Verify configs roundtrip.
	loader := &mockLoader{stored: make(map[string]json.RawMessage)}
	restored, errors := loadConfigs(context.Background(), loader, loaded.Configs)
	if restored != 2 || errors != 0 {
		t.Errorf("configs: expected 2/0, got %d/%d", restored, errors)
	}

	// Verify advice roundtrip.
	if len(loaded.Advice) != 1 {
		t.Fatalf("expected 1 advice, got %d", len(loaded.Advice))
	}
	if loaded.Advice[0].Title != "Rule one" {
		t.Errorf("expected 'Rule one', got %s", loaded.Advice[0].Title)
	}
	if loaded.Advice[0].Priority != 1 {
		t.Errorf("expected priority 1, got %d", loaded.Advice[0].Priority)
	}
}

func TestDumpFormat_LegacyFlatArray(t *testing.T) {
	// Verify the load command handles the old flat array format.
	legacy := `[{"key":"a","value":{"x":1}},{"key":"b","value":{"y":2}}]`

	var dump configDump
	err := json.Unmarshal([]byte(legacy), &dump)
	if err == nil {
		t.Fatal("expected flat array to fail parsing as configDump")
	}

	// Load command falls back to flat array — test the parsing.
	var entries []configEntry
	if err := json.Unmarshal([]byte(legacy), &entries); err != nil {
		t.Fatalf("legacy parse failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2, got %d", len(entries))
	}
}

// --- KV key parsing tests ---

func TestParseKVKeyToLabels_Global(t *testing.T) {
	cat, labels := parseKVKeyToLabels("claude-settings:global")
	if cat != "claude-settings" {
		t.Errorf("expected category=claude-settings, got %s", cat)
	}
	if len(labels) != 1 || labels[0] != "global" {
		t.Errorf("expected [global], got %v", labels)
	}
}

func TestParseKVKeyToLabels_Role(t *testing.T) {
	cat, labels := parseKVKeyToLabels("claude-hooks:captain")
	if cat != "claude-hooks" {
		t.Errorf("expected category=claude-hooks, got %s", cat)
	}
	if len(labels) != 1 || labels[0] != "role:captain" {
		t.Errorf("expected [role:captain], got %v", labels)
	}
}

func TestParseKVKeyToLabels_NoColon(t *testing.T) {
	cat, labels := parseKVKeyToLabels("orphan")
	if cat != "orphan" {
		t.Errorf("expected category=orphan, got %s", cat)
	}
	if len(labels) != 1 || labels[0] != "global" {
		t.Errorf("expected [global], got %v", labels)
	}
}

// --- Mock for migration ---

type mockMigrator struct {
	configs map[string][]beadsapi.ConfigEntry
	beads   []*beadsapi.BeadDetail
	created []beadsapi.CreateBeadRequest
	err     error
}

func (m *mockMigrator) ListConfigs(_ context.Context, namespace string) ([]beadsapi.ConfigEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.configs[namespace], nil
}

func (m *mockMigrator) ListBeadsFiltered(_ context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error) {
	return &beadsapi.ListBeadsResult{Beads: m.beads, Total: len(m.beads)}, nil
}

func (m *mockMigrator) CreateBead(_ context.Context, req beadsapi.CreateBeadRequest) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	m.created = append(m.created, req)
	return fmt.Sprintf("kd-migrate-%d", len(m.created)), nil
}

// --- Migration tests ---

func TestPlanMigration_BasicEntries(t *testing.T) {
	client := &mockMigrator{
		configs: map[string][]beadsapi.ConfigEntry{
			"claude-settings": {
				{Key: "claude-settings:global", Value: json.RawMessage(`{"model":"opus"}`)},
				{Key: "claude-settings:captain", Value: json.RawMessage(`{"model":"sonnet"}`)},
			},
		},
		beads: []*beadsapi.BeadDetail{},
	}

	actions, err := planMigration(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}

	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}

	// Should be sorted: global before role:captain.
	if actions[0].Category != "claude-settings" || actions[0].Labels[0] != "global" {
		t.Errorf("expected first action to be claude-settings [global], got %s %v", actions[0].Category, actions[0].Labels)
	}
	if actions[1].Labels[0] != "role:captain" {
		t.Errorf("expected second action labels [role:captain], got %v", actions[1].Labels)
	}
	if actions[0].Skipped || actions[1].Skipped {
		t.Error("expected no actions to be skipped")
	}
}

func TestPlanMigration_SkipsExisting(t *testing.T) {
	client := &mockMigrator{
		configs: map[string][]beadsapi.ConfigEntry{
			"claude-settings": {
				{Key: "claude-settings:global", Value: json.RawMessage(`{"model":"opus"}`)},
			},
		},
		beads: []*beadsapi.BeadDetail{
			{Title: "claude-settings", Labels: []string{"global"}, Description: `{"model":"opus"}`},
		},
	}

	actions, err := planMigration(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}

	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if !actions[0].Skipped {
		t.Error("expected action to be skipped (existing bead)")
	}
}

func TestPlanMigration_Empty(t *testing.T) {
	client := &mockMigrator{
		configs: map[string][]beadsapi.ConfigEntry{},
		beads:   []*beadsapi.BeadDetail{},
	}

	actions, err := planMigration(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 0 {
		t.Errorf("expected 0 actions, got %d", len(actions))
	}
}

func TestExecuteMigration_CreatesBeads(t *testing.T) {
	client := &mockMigrator{created: nil}
	actions := []migrateAction{
		{Category: "claude-settings", Labels: []string{"global"}, Value: `{"model":"opus"}`},
		{Category: "claude-hooks", Labels: []string{"role:captain"}, Value: `{"hooks":{}}`},
	}

	created, skipped, errors := executeMigration(context.Background(), client, actions)
	if created != 2 || skipped != 0 || errors != 0 {
		t.Errorf("expected 2/0/0, got %d/%d/%d", created, skipped, errors)
	}

	if len(client.created) != 2 {
		t.Fatalf("expected 2 create calls, got %d", len(client.created))
	}
	if client.created[0].Title != "claude-settings" {
		t.Errorf("wrong title: %s", client.created[0].Title)
	}
	if client.created[0].Type != "config" {
		t.Errorf("wrong type: %s", client.created[0].Type)
	}
	if client.created[0].Description != `{"model":"opus"}` {
		t.Errorf("wrong description: %s", client.created[0].Description)
	}
}

func TestExecuteMigration_SkipsExisting(t *testing.T) {
	client := &mockMigrator{created: nil}
	actions := []migrateAction{
		{Category: "claude-settings", Labels: []string{"global"}, Value: `{}`, Skipped: true},
		{Category: "claude-hooks", Labels: []string{"global"}, Value: `{}`},
	}

	created, skipped, errors := executeMigration(context.Background(), client, actions)
	if created != 1 || skipped != 1 || errors != 0 {
		t.Errorf("expected 1/1/0, got %d/%d/%d", created, skipped, errors)
	}
}

func TestExecuteMigration_ContinuesOnError(t *testing.T) {
	client := &mockMigrator{err: fmt.Errorf("fail")}
	actions := []migrateAction{
		{Category: "a", Labels: []string{"global"}, Value: `{}`},
		{Category: "b", Labels: []string{"global"}, Value: `{}`},
	}

	created, skipped, errors := executeMigration(context.Background(), client, actions)
	if created != 0 || skipped != 0 || errors != 2 {
		t.Errorf("expected 0/0/2, got %d/%d/%d", created, skipped, errors)
	}
}
