package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// --- Mock implementations ---

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

// --- Config bead dump/load tests ---

func TestDumpConfigBeads_CollectsAndSorts(t *testing.T) {
	lister := &mockBeadLister{
		beads: []*beadsapi.BeadDetail{
			{Title: "claude-mcp", Labels: []string{"global"}, Description: `{"mcpServers":{}}`},
			{Title: "claude-settings", Labels: []string{"role:captain"}, Description: `{"model":"opus"}`},
			{Title: "claude-settings", Labels: []string{"global"}, Description: `{"model":"sonnet"}`},
		},
	}

	entries, err := dumpConfigBeads(context.Background(), lister)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3, got %d", len(entries))
	}
	// Sorted by title then labels.
	if entries[0].Title != "claude-mcp" {
		t.Errorf("expected first=claude-mcp, got %s", entries[0].Title)
	}
	if entries[1].Title != "claude-settings" || entries[1].Labels[0] != "global" {
		t.Errorf("expected second=claude-settings [global], got %s %v", entries[1].Title, entries[1].Labels)
	}
	if entries[2].Title != "claude-settings" || entries[2].Labels[0] != "role:captain" {
		t.Errorf("expected third=claude-settings [role:captain], got %s %v", entries[2].Title, entries[2].Labels)
	}
}

func TestDumpConfigBeads_Empty(t *testing.T) {
	lister := &mockBeadLister{beads: []*beadsapi.BeadDetail{}}
	entries, err := dumpConfigBeads(context.Background(), lister)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0, got %d", len(entries))
	}
}

type mockConfigBeadCreator struct {
	beads   []*beadsapi.BeadDetail
	created []beadsapi.CreateBeadRequest
	err     error
}

func (m *mockConfigBeadCreator) ListBeadsFiltered(_ context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error) {
	return &beadsapi.ListBeadsResult{Beads: m.beads, Total: len(m.beads)}, nil
}

func (m *mockConfigBeadCreator) CreateBead(_ context.Context, req beadsapi.CreateBeadRequest) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	m.created = append(m.created, req)
	return fmt.Sprintf("kd-load-%d", len(m.created)), nil
}

func TestLoadConfigBeads_CreatesNew(t *testing.T) {
	client := &mockConfigBeadCreator{beads: []*beadsapi.BeadDetail{}}
	entries := []configBeadEntry{
		{Title: "claude-settings", Labels: []string{"global"}, Value: json.RawMessage(`{"model":"opus"}`)},
		{Title: "claude-mcp", Labels: []string{"global"}, Value: json.RawMessage(`{"mcpServers":{}}`)},
	}

	created, skipped, errors := loadConfigBeads(context.Background(), client, entries)
	if created != 2 || skipped != 0 || errors != 0 {
		t.Errorf("expected 2/0/0, got %d/%d/%d", created, skipped, errors)
	}
	if len(client.created) != 2 {
		t.Fatalf("expected 2 creates, got %d", len(client.created))
	}
	if client.created[0].Type != "config" {
		t.Errorf("wrong type: %s", client.created[0].Type)
	}
}

func TestLoadConfigBeads_SkipsDuplicates(t *testing.T) {
	client := &mockConfigBeadCreator{
		beads: []*beadsapi.BeadDetail{
			{Title: "claude-settings", Labels: []string{"global"}, Description: `{"model":"opus"}`},
		},
	}
	entries := []configBeadEntry{
		{Title: "claude-settings", Labels: []string{"global"}, Value: json.RawMessage(`{"model":"sonnet"}`)},
		{Title: "claude-mcp", Labels: []string{"global"}, Value: json.RawMessage(`{}`)},
	}

	created, skipped, errors := loadConfigBeads(context.Background(), client, entries)
	if created != 1 || skipped != 1 || errors != 0 {
		t.Errorf("expected 1/1/0, got %d/%d/%d", created, skipped, errors)
	}
}

func TestConfigDump_IncludesConfigBeads(t *testing.T) {
	dump := configDump{
		Configs: []configEntry{
			{Key: "claude-settings:global", Value: json.RawMessage(`{"model":"opus"}`)},
		},
		ConfigBeads: []configBeadEntry{
			{Title: "claude-settings", Labels: []string{"global"}, Value: json.RawMessage(`{"model":"opus"}`)},
		},
		Advice: []adviceEntry{
			{Title: "Rule", Description: "desc", Labels: []string{"global"}, Priority: 1},
		},
	}

	data, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}

	var loaded configDump
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if len(loaded.Configs) != 1 {
		t.Errorf("expected 1 config, got %d", len(loaded.Configs))
	}
	if len(loaded.ConfigBeads) != 1 {
		t.Errorf("expected 1 config bead, got %d", len(loaded.ConfigBeads))
	}
	if len(loaded.Advice) != 1 {
		t.Errorf("expected 1 advice, got %d", len(loaded.Advice))
	}
}

func TestConfigDump_OmitsEmptyConfigBeads(t *testing.T) {
	dump := configDump{
		Configs: []configEntry{{Key: "a", Value: json.RawMessage(`{}`)}},
	}

	data, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}

	// config_beads should be omitted when nil.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["config_beads"]; ok {
		t.Error("expected config_beads to be omitted when empty")
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

func TestParseKVKeyToLabels_TypeDefinition(t *testing.T) {
	title, labels := parseKVKeyToLabels("type:task")
	if title != "type:task" {
		t.Errorf("expected title=type:task, got %s", title)
	}
	if len(labels) != 1 || labels[0] != "global" {
		t.Errorf("expected [global] for type definitions, got %v", labels)
	}
}

func TestParseKVKeyToLabels_ViewDefinition(t *testing.T) {
	title, labels := parseKVKeyToLabels("view:agents:active")
	if title != "view:agents:active" {
		t.Errorf("expected title=view:agents:active, got %s", title)
	}
	if len(labels) != 1 || labels[0] != "global" {
		t.Errorf("expected [global] for view definitions, got %v", labels)
	}
}

func TestParseKVKeyToLabels_ContextRole(t *testing.T) {
	title, labels := parseKVKeyToLabels("context:captain")
	if title != "context" {
		t.Errorf("expected title=context, got %s", title)
	}
	if len(labels) != 1 || labels[0] != "role:captain" {
		t.Errorf("expected [role:captain], got %v", labels)
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
