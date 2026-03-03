package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:     "config",
	Short:   "Config bead management",
	GroupID: "session",
}

// configNamespaces are the known config bead namespaces to dump.
// The daemon API requires a namespace parameter, so we enumerate all known ones.
var configNamespaces = []string{
	"claude-settings",
	"claude-hooks",
	"claude-mcp",
	"type",
	"context",
	"view",
}

var configDumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Dump all config beads as JSON (pipe to file for backup)",
	Long: `Fetches config entries from all known namespaces and prints them
as a JSON array. The output is suitable for saving to a file and
restoring later with 'gb config load'.

Example:
  gb config dump > configs-backup.json
  gb config dump | jq .`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		type entry struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}

		var entries []entry
		for _, ns := range configNamespaces {
			configs, err := daemon.ListConfigs(ctx, ns)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[config] warning: failed to list %s: %v\n", ns, err)
				continue
			}
			for _, c := range configs {
				entries = append(entries, entry{Key: c.Key, Value: c.Value})
			}
		}

		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Key < entries[j].Key
		})

		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling: %w", err)
		}

		fmt.Fprintln(os.Stdout, string(data))
		fmt.Fprintf(os.Stderr, "[config] dumped %d entries\n", len(entries))
		return nil
	},
}

var configLoadCmd = &cobra.Command{
	Use:   "load <file>",
	Short: "Restore config beads from a dump file",
	Long: `Reads a JSON file produced by 'gb config dump' and creates or
updates each config entry on the daemon.

Example:
  gb config load configs-backup.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("reading file: %w", err)
		}

		type entry struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		var entries []entry
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("parsing JSON: %w", err)
		}

		for _, e := range entries {
			if err := daemon.SetConfig(ctx, e.Key, e.Value); err != nil {
				fmt.Fprintf(os.Stderr, "[config] warning: failed to set %s: %v\n", e.Key, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "[config] restored %s\n", e.Key)
		}

		fmt.Fprintf(os.Stderr, "[config] loaded %d entries\n", len(entries))
		return nil
	},
}

func init() {
	configCmd.AddCommand(configDumpCmd)
	configCmd.AddCommand(configLoadCmd)
}
