// Package bridge provides the DoltClient for wasteland operations.
//
// DoltClient wraps the dolt CLI to query and mutate the local clone of
// a wasteland commons database (wanted items, rigs, completions).
package bridge

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DoltClient wraps dolt CLI for wasteland operations.
type DoltClient interface {
	QueryWanted(ctx context.Context, status string) ([]WastelandItem, error)
	ClaimWanted(ctx context.Context, wantedID, rigHandle string) error
	SubmitCompletion(ctx context.Context, completionID, wantedID, rigHandle, evidence string) error
	Push(ctx context.Context) error
	Pull(ctx context.Context) error
}

// WastelandItem mirrors the wanted table row.
type WastelandItem struct {
	ID          string
	Title       string
	Description string
	Project     string
	Type        string
	Priority    int
	Tags        []string
	PostedBy    string
	ClaimedBy   string
	Status      string
	EffortLevel string
}

// WastelandConfig is persisted to <data-dir>/wasteland-config.json.
type WastelandConfig struct {
	Upstream  string    `json:"upstream"`
	ForkOrg   string    `json:"fork_org"`
	ForkDB    string    `json:"fork_db"`
	LocalDir  string    `json:"local_dir"`
	RigHandle string    `json:"rig_handle"`
	JoinedAt  time.Time `json:"joined_at"`
}

// ExecDoltClient shells out to the dolt CLI operating on a local clone.
type ExecDoltClient struct {
	// LocalDir is the path to the local dolt clone.
	LocalDir string
}

// NewExecDoltClient creates a DoltClient that shells out to the dolt CLI.
// It verifies that dolt is on the PATH.
func NewExecDoltClient(localDir string) (*ExecDoltClient, error) {
	if _, err := exec.LookPath("dolt"); err != nil {
		return nil, fmt.Errorf("dolt not found on PATH — install from https://docs.dolthub.com/introduction/installation: %w", err)
	}
	return &ExecDoltClient{LocalDir: localDir}, nil
}

// dolt runs a dolt command in the local clone directory.
func (c *ExecDoltClient) dolt(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "dolt", args...)
	cmd.Dir = c.LocalDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("dolt %s: %s: %w", strings.Join(args, " "), stderr.String(), err)
	}
	return stdout.String(), nil
}

// QueryWanted queries the wanted table for items with the given status.
func (c *ExecDoltClient) QueryWanted(ctx context.Context, status string) ([]WastelandItem, error) {
	query := fmt.Sprintf(
		`SELECT id, title, description, project, type, priority, tags, posted_by, claimed_by, status, effort_level FROM wanted WHERE status = '%s'`,
		escapeSQLString(status),
	)
	out, err := c.dolt(ctx, "sql", "-r", "csv", "-q", query)
	if err != nil {
		return nil, fmt.Errorf("query wanted: %w", err)
	}
	return parseWantedCSV(out)
}

// ClaimWanted marks a wanted item as claimed by the given rig handle.
func (c *ExecDoltClient) ClaimWanted(ctx context.Context, wantedID, rigHandle string) error {
	query := fmt.Sprintf(
		`UPDATE wanted SET status = 'claimed', claimed_by = '%s', updated_at = NOW() WHERE id = '%s' AND status = 'open'`,
		escapeSQLString(rigHandle), escapeSQLString(wantedID),
	)
	if _, err := c.dolt(ctx, "sql", "-q", query); err != nil {
		return fmt.Errorf("claim wanted %s: %w", wantedID, err)
	}
	if _, err := c.dolt(ctx, "add", "."); err != nil {
		return fmt.Errorf("dolt add: %w", err)
	}
	msg := fmt.Sprintf("Claim wanted item %s by %s", wantedID, rigHandle)
	if _, err := c.dolt(ctx, "commit", "-m", msg); err != nil {
		return fmt.Errorf("dolt commit: %w", err)
	}
	return nil
}

// SubmitCompletion inserts a completion record and marks the wanted item in_review.
func (c *ExecDoltClient) SubmitCompletion(ctx context.Context, completionID, wantedID, rigHandle, evidence string) error {
	insert := fmt.Sprintf(
		`INSERT INTO completions (id, wanted_id, completed_by, evidence, completed_at) VALUES ('%s', '%s', '%s', '%s', NOW())`,
		escapeSQLString(completionID), escapeSQLString(wantedID),
		escapeSQLString(rigHandle), escapeSQLString(evidence),
	)
	if _, err := c.dolt(ctx, "sql", "-q", insert); err != nil {
		return fmt.Errorf("insert completion: %w", err)
	}

	update := fmt.Sprintf(
		`UPDATE wanted SET status = 'in_review', updated_at = NOW() WHERE id = '%s'`,
		escapeSQLString(wantedID),
	)
	if _, err := c.dolt(ctx, "sql", "-q", update); err != nil {
		return fmt.Errorf("update wanted status: %w", err)
	}

	if _, err := c.dolt(ctx, "add", "."); err != nil {
		return fmt.Errorf("dolt add: %w", err)
	}
	msg := fmt.Sprintf("Complete wanted item %s by %s", wantedID, rigHandle)
	if _, err := c.dolt(ctx, "commit", "-m", msg); err != nil {
		return fmt.Errorf("dolt commit: %w", err)
	}
	return nil
}

// Push pushes the local clone to origin.
func (c *ExecDoltClient) Push(ctx context.Context) error {
	_, err := c.dolt(ctx, "push", "origin", "main")
	return err
}

// Pull pulls upstream changes into the local clone.
func (c *ExecDoltClient) Pull(ctx context.Context) error {
	_, err := c.dolt(ctx, "pull", "upstream", "main")
	return err
}

// parseWantedCSV parses CSV output from `dolt sql -r csv` into WastelandItems.
func parseWantedCSV(data string) ([]WastelandItem, error) {
	r := csv.NewReader(strings.NewReader(data))

	// Read header.
	header, err := r.Read()
	if err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, fmt.Errorf("read CSV header: %w", err)
	}

	// Build column index.
	colIdx := make(map[string]int, len(header))
	for i, col := range header {
		colIdx[strings.TrimSpace(col)] = i
	}

	var items []WastelandItem
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read CSV row: %w", err)
		}

		item := WastelandItem{
			ID:          getCol(row, colIdx, "id"),
			Title:       getCol(row, colIdx, "title"),
			Description: getCol(row, colIdx, "description"),
			Project:     getCol(row, colIdx, "project"),
			Type:        getCol(row, colIdx, "type"),
			PostedBy:    getCol(row, colIdx, "posted_by"),
			ClaimedBy:   getCol(row, colIdx, "claimed_by"),
			Status:      getCol(row, colIdx, "status"),
			EffortLevel: getCol(row, colIdx, "effort_level"),
		}

		if p := getCol(row, colIdx, "priority"); p != "" {
			item.Priority, _ = strconv.Atoi(p)
		}

		if tags := getCol(row, colIdx, "tags"); tags != "" {
			// Tags are JSON array stored as string, e.g. ["go","cli"].
			tags = strings.Trim(tags, "[]\"")
			if tags != "" {
				for _, t := range strings.Split(tags, ",") {
					t = strings.Trim(t, " \"")
					if t != "" {
						item.Tags = append(item.Tags, t)
					}
				}
			}
		}

		items = append(items, item)
	}

	return items, nil
}

// getCol safely gets a column value from a CSV row by name.
func getCol(row []string, colIdx map[string]int, name string) string {
	i, ok := colIdx[name]
	if !ok || i >= len(row) {
		return ""
	}
	return row[i]
}

// escapeSQLString escapes single quotes for SQL string literals.
func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
