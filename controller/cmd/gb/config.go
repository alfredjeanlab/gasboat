package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gasboat/controller/internal/beadsapi"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:     "config",
	Short:   "Config bead management",
	GroupID: "session",
}

// configNamespaces returns the known config bead namespaces to dump.
// Delegates to the config registry (config_registry.go).
func configNamespaces() []string {
	return ConfigCategoryNames()
}

// --- Serialization types ---

type configEntry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type adviceEntry struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels,omitempty"`
	Priority    int      `json:"priority"`
}

// configDump is the top-level dump/load format.
type configDump struct {
	Configs []configEntry `json:"configs"`
	Advice  []adviceEntry `json:"advice,omitempty"`
}

// --- Interfaces for testability ---

type configDumper interface {
	ListConfigs(ctx context.Context, namespace string) ([]beadsapi.ConfigEntry, error)
}

type beadLister interface {
	ListBeadsFiltered(ctx context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error)
}

type configLoader interface {
	SetConfig(ctx context.Context, key string, value []byte) error
}

// --- Core logic ---

func dumpConfigs(ctx context.Context, client configDumper, namespaces []string) ([]configEntry, error) {
	var entries []configEntry
	for _, ns := range namespaces {
		configs, err := client.ListConfigs(ctx, ns)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[config] warning: failed to list %s: %v\n", ns, err)
			continue
		}
		for _, c := range configs {
			entries = append(entries, configEntry{Key: c.Key, Value: c.Value})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})

	return entries, nil
}

func dumpAdvice(ctx context.Context, client beadLister) ([]adviceEntry, error) {
	result, err := client.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"advice"},
		Statuses: []string{"open"},
		Limit:    500,
	})
	if err != nil {
		return nil, fmt.Errorf("listing advice: %w", err)
	}

	entries := make([]adviceEntry, 0, len(result.Beads))
	for _, b := range result.Beads {
		entries = append(entries, adviceEntry{
			Title:       b.Title,
			Description: b.Description,
			Labels:      b.Labels,
			Priority:    b.Priority,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Title < entries[j].Title
	})

	return entries, nil
}

func loadConfigs(ctx context.Context, client configLoader, entries []configEntry) (restored int, errors int) {
	for _, e := range entries {
		if err := client.SetConfig(ctx, e.Key, e.Value); err != nil {
			fmt.Fprintf(os.Stderr, "[config] warning: failed to set %s: %v\n", e.Key, err)
			errors++
			continue
		}
		fmt.Fprintf(os.Stderr, "[config] restored %s\n", e.Key)
		restored++
	}
	return restored, errors
}

// --- Commands ---

var configDumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Dump all configs and advice as JSON (pipe to file for backup)",
	Long: `Fetches config entries from all known namespaces and all advice
beads, then prints them as a JSON object with "configs" and "advice"
sections. The output is suitable for saving to a file and restoring
later with 'gb config load'.

Sections:
  configs — key/value config beads (settings, hooks, MCP, types, views)
  advice  — advice beads (rules and guidance for agents)

Example:
  gb config dump > backup.json
  gb config dump | jq .advice`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		configs, err := dumpConfigs(ctx, daemon, configNamespaces())
		if err != nil {
			return err
		}

		advice, err := dumpAdvice(ctx, daemon)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[config] warning: failed to dump advice: %v\n", err)
		}

		dump := configDump{
			Configs: configs,
			Advice:  advice,
		}

		data, err := json.MarshalIndent(dump, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling: %w", err)
		}

		fmt.Fprintln(os.Stdout, string(data))
		fmt.Fprintf(os.Stderr, "[config] dumped %d configs, %d advice\n", len(configs), len(advice))
		return nil
	},
}

var configLoadCmd = &cobra.Command{
	Use:   "load <file>",
	Short: "Restore configs and advice from a dump file",
	Long: `Reads a JSON file produced by 'gb config dump' and restores
config entries and advice beads to the daemon.

Advice beads are matched by title — existing advice with the same
title is skipped to avoid duplicates.

Example:
  gb config load backup.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("reading file: %w", err)
		}

		var dump configDump
		if err := json.Unmarshal(data, &dump); err != nil {
			// Try legacy format (flat array of config entries).
			var legacy []configEntry
			if err2 := json.Unmarshal(data, &legacy); err2 == nil {
				dump.Configs = legacy
			} else {
				return fmt.Errorf("parsing JSON: %w", err)
			}
		}

		restored, _ := loadConfigs(ctx, daemon, dump.Configs)
		fmt.Fprintf(os.Stderr, "[config] loaded %d config entries\n", restored)

		if len(dump.Advice) > 0 {
			// Build set of existing advice titles for dedup.
			existing, _ := dumpAdvice(ctx, daemon)
			titleSet := make(map[string]bool, len(existing))
			for _, a := range existing {
				titleSet[a.Title] = true
			}

			created := 0
			for _, a := range dump.Advice {
				if titleSet[a.Title] {
					fmt.Fprintf(os.Stderr, "[config] advice exists, skipping: %s\n", a.Title)
					continue
				}
				_, err := daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
				Title:       a.Title,
				Type:        "advice",
				Description: a.Description,
				Labels:      a.Labels,
				Priority:    a.Priority,
			})
			if err != nil {
					fmt.Fprintf(os.Stderr, "[config] warning: failed to create advice %q: %v\n", a.Title, err)
					continue
				}
				fmt.Fprintf(os.Stderr, "[config] created advice: %s\n", a.Title)
				created++
			}
			fmt.Fprintf(os.Stderr, "[config] loaded %d advice beads (%d skipped)\n", created, len(dump.Advice)-created)
		}

		return nil
	},
}

// --- Migrate logic ---

// configMigrator is the interface for creating config beads during migration.
type configMigrator interface {
	ListConfigs(ctx context.Context, namespace string) ([]beadsapi.ConfigEntry, error)
	ListBeadsFiltered(ctx context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error)
	CreateBead(ctx context.Context, req beadsapi.CreateBeadRequest) (string, error)
}

// migrateAction describes one KV config entry to be migrated.
type migrateAction struct {
	Category string   // e.g. "claude-settings"
	Labels   []string // e.g. ["global"] or ["role:captain"]
	Value    string   // JSON string (stored in bead Description)
	Skipped  bool     // true if a matching bead already exists
}

// parseKVKeyToLabels converts a KV config key like "claude-settings:global"
// or "claude-settings:captain" into (category, labels).
func parseKVKeyToLabels(key string) (category string, labels []string) {
	idx := strings.Index(key, ":")
	if idx < 0 {
		return key, []string{"global"}
	}
	category = key[:idx]
	suffix := key[idx+1:]

	if suffix == "global" {
		return category, []string{"global"}
	}
	// Treat the suffix as a role name.
	return category, []string{"role:" + suffix}
}

// planMigration scans all KV config entries and checks for existing
// config beads to produce a list of migration actions.
func planMigration(ctx context.Context, client configMigrator) ([]migrateAction, error) {
	// Fetch all existing config beads to check for duplicates.
	existing, err := client.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"config"},
		Statuses: []string{"open"},
		Limit:    500,
	})
	if err != nil {
		return nil, fmt.Errorf("listing existing config beads: %w", err)
	}

	// Build set of existing config beads keyed by "title|label1,label2".
	existingSet := make(map[string]bool)
	for _, b := range existing.Beads {
		sort.Strings(b.Labels)
		key := b.Title + "|" + strings.Join(b.Labels, ",")
		existingSet[key] = true
	}

	var actions []migrateAction
	for _, ns := range ConfigCategoryNames() {
		entries, err := client.ListConfigs(ctx, ns)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[migrate] warning: failed to list %s: %v\n", ns, err)
			continue
		}

		for _, entry := range entries {
			category, labels := parseKVKeyToLabels(entry.Key)
			sortedLabels := make([]string, len(labels))
			copy(sortedLabels, labels)
			sort.Strings(sortedLabels)

			dedupKey := category + "|" + strings.Join(sortedLabels, ",")

			actions = append(actions, migrateAction{
				Category: category,
				Labels:   labels,
				Value:    string(entry.Value),
				Skipped:  existingSet[dedupKey],
			})
		}
	}

	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Category != actions[j].Category {
			return actions[i].Category < actions[j].Category
		}
		return strings.Join(actions[i].Labels, ",") < strings.Join(actions[j].Labels, ",")
	})

	return actions, nil
}

// executeMigration creates config beads for non-skipped actions.
func executeMigration(ctx context.Context, client configMigrator, actions []migrateAction) (created, skipped, errors int) {
	for _, a := range actions {
		if a.Skipped {
			skipped++
			continue
		}
		_, err := client.CreateBead(ctx, beadsapi.CreateBeadRequest{
			Title:       a.Category,
			Type:        "config",
			Kind:        "config",
			Description: a.Value,
			Labels:      a.Labels,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[migrate] error creating %s %v: %v\n", a.Category, a.Labels, err)
			errors++
			continue
		}
		fmt.Fprintf(os.Stderr, "[migrate] created config bead: %s %v\n", a.Category, a.Labels)
		created++
	}
	return created, skipped, errors
}

var configMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate KV config entries to label-based config beads",
	Long: `Converts existing KV config entries (namespace:key pattern) into
proper beads with type="config", using labels for scoping.

By default runs in dry-run mode showing what would be migrated.
Use --execute to perform the actual migration.

The migration is idempotent: existing config beads with matching
title and labels are skipped.

Example:
  gb config migrate             # dry-run: show what would happen
  gb config migrate --execute   # actually create the config beads`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		actions, err := planMigration(ctx, daemon)
		if err != nil {
			return err
		}

		if len(actions) == 0 {
			fmt.Fprintf(os.Stderr, "[migrate] no KV config entries found to migrate\n")
			return nil
		}

		execute, _ := cmd.Flags().GetBool("execute")

		if !execute {
			// Dry-run: just print the plan.
			fmt.Fprintf(os.Stderr, "[migrate] dry-run — %d entries found:\n", len(actions))
			for _, a := range actions {
				status := "would create"
				if a.Skipped {
					status = "skip (exists)"
				}
				fmt.Fprintf(os.Stderr, "  %s: %s %v\n", status, a.Category, a.Labels)
			}
			fmt.Fprintf(os.Stderr, "\nRun with --execute to perform the migration.\n")
			return nil
		}

		created, skipped, errors := executeMigration(ctx, daemon, actions)
		fmt.Fprintf(os.Stderr, "[migrate] done: %d created, %d skipped, %d errors\n", created, skipped, errors)
		return nil
	},
}

func init() {
	configMigrateCmd.Flags().Bool("execute", false, "actually perform the migration (default is dry-run)")
	configCmd.AddCommand(configDumpCmd)
	configCmd.AddCommand(configLoadCmd)
	configCmd.AddCommand(configMigrateCmd)
}
