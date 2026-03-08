// Package bridge provides the wasteland join flow.
//
// JoinWasteland forks a commons database on DoltHub, clones it locally,
// adds the upstream remote, registers the rig, and persists config.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	dolthubAPIBase    = "https://www.dolthub.com/api/v1alpha1"
	dolthubRemoteBase = "https://doltremoteapi.dolthub.com"
)

// JoinConfig holds parameters for joining a wasteland.
type JoinConfig struct {
	Upstream    string // "org/db" format, e.g., "steveyegge/wl-commons"
	ForkOrg     string // DoltHub org to fork into
	Token       string // DoltHub API token
	RigHandle   string // rig handle for registry
	DisplayName string // optional display name
	OwnerEmail  string // optional owner email
	DataDir     string // local directory for dolt clone
	Logger      *slog.Logger
}

// JoinWasteland forks the upstream commons, clones locally, registers
// the rig, and returns the persisted config.
func JoinWasteland(ctx context.Context, cfg JoinConfig) (*WastelandConfig, error) {
	logger := cfg.Logger

	// Parse upstream "org/db".
	parts := strings.SplitN(cfg.Upstream, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid upstream format %q: expected org/db", cfg.Upstream)
	}
	upstreamOrg, upstreamDB := parts[0], parts[1]

	// 1. Fork on DoltHub.
	logger.Info("forking wasteland commons", "upstream", cfg.Upstream, "fork_org", cfg.ForkOrg)
	if err := dolthubFork(ctx, cfg.Token, cfg.ForkOrg, upstreamOrg, upstreamDB); err != nil {
		return nil, fmt.Errorf("fork: %w", err)
	}

	// 2. Clone fork locally.
	localDir := filepath.Join(cfg.DataDir, cfg.ForkOrg, upstreamDB)
	if err := os.MkdirAll(filepath.Dir(localDir), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	cloneURL := dolthubRemoteBase + "/" + cfg.ForkOrg + "/" + upstreamDB
	if _, err := os.Stat(localDir); err == nil {
		logger.Info("local clone already exists, pulling", "dir", localDir)
		if _, err := doltExec(ctx, localDir, "pull", "origin", "main"); err != nil {
			logger.Warn("pull failed, continuing with existing clone", "error", err)
		}
	} else {
		logger.Info("cloning fork", "url", cloneURL, "dir", localDir)
		cmd := exec.CommandContext(ctx, "dolt", "clone", cloneURL, localDir)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("dolt clone: %s: %w", stderr.String(), err)
		}
	}

	// 3. Add upstream remote.
	upstreamURL := dolthubRemoteBase + "/" + upstreamOrg + "/" + upstreamDB
	// Check if upstream remote already exists.
	remotes, _ := doltExec(ctx, localDir, "remote", "-v")
	if !strings.Contains(remotes, "upstream") {
		logger.Info("adding upstream remote", "url", upstreamURL)
		if _, err := doltExec(ctx, localDir, "remote", "add", "upstream", upstreamURL); err != nil {
			return nil, fmt.Errorf("add upstream remote: %w", err)
		}
	}

	// 4. Register rig.
	logger.Info("registering rig", "handle", cfg.RigHandle)
	registerSQL := fmt.Sprintf(
		`INSERT INTO rigs (handle, display_name, dolthub_org, owner_email, gt_version, trust_level, registered_at, last_seen) `+
			`VALUES ('%s', '%s', '%s', '%s', 'wl-bridge', 0, NOW(), NOW()) `+
			`ON DUPLICATE KEY UPDATE last_seen = NOW(), gt_version = 'wl-bridge'`,
		escapeSQLString(cfg.RigHandle),
		escapeSQLString(cfg.DisplayName),
		escapeSQLString(cfg.ForkOrg),
		escapeSQLString(cfg.OwnerEmail),
	)
	if _, err := doltExec(ctx, localDir, "sql", "-q", registerSQL); err != nil {
		return nil, fmt.Errorf("register rig: %w", err)
	}

	// 5. Commit + push.
	if _, err := doltExec(ctx, localDir, "add", "."); err != nil {
		return nil, fmt.Errorf("dolt add: %w", err)
	}
	commitMsg := fmt.Sprintf("Register rig: %s", cfg.RigHandle)
	if _, err := doltExec(ctx, localDir, "commit", "-m", commitMsg); err != nil {
		// "nothing to commit" is OK — rig already registered.
		if !strings.Contains(err.Error(), "nothing to commit") {
			return nil, fmt.Errorf("dolt commit: %w", err)
		}
	}
	if _, err := doltExec(ctx, localDir, "push", "origin", "main"); err != nil {
		return nil, fmt.Errorf("dolt push: %w", err)
	}

	// 6. Save config.
	wlCfg := &WastelandConfig{
		Upstream:  cfg.Upstream,
		ForkOrg:   cfg.ForkOrg,
		ForkDB:    upstreamDB,
		LocalDir:  localDir,
		RigHandle: cfg.RigHandle,
		JoinedAt:  time.Now(),
	}

	configPath := filepath.Join(cfg.DataDir, "wasteland-config.json")
	configJSON, err := json.MarshalIndent(wlCfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir config: %w", err)
	}
	if err := os.WriteFile(configPath, configJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}

	logger.Info("wasteland join complete",
		"upstream", cfg.Upstream,
		"fork", cfg.ForkOrg+"/"+upstreamDB,
		"local_dir", localDir,
		"config", configPath)

	return wlCfg, nil
}

// LoadWastelandConfig loads a persisted wasteland config from disk.
func LoadWastelandConfig(dataDir string) (*WastelandConfig, error) {
	configPath := filepath.Join(dataDir, "wasteland-config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read wasteland config: %w", err)
	}
	var cfg WastelandConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal wasteland config: %w", err)
	}
	return &cfg, nil
}

// dolthubFork forks a database on DoltHub. Treats "already exists" as success.
func dolthubFork(ctx context.Context, token, targetOrg, parentOrg, parentDB string) error {
	body := map[string]string{
		"ownerName":          targetOrg,
		"parentOwnerName":    parentOrg,
		"parentDatabaseName": parentDB,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dolthubAPIBase+"/fork", bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fork HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	// "already exists" is idempotent success.
	if strings.Contains(string(respBody), "already exists") {
		return nil
	}

	return fmt.Errorf("DoltHub fork returned %d: %s", resp.StatusCode, string(respBody))
}

// doltExec runs a dolt command in the given directory and returns stdout.
func doltExec(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "dolt", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("dolt %s: %s: %w", strings.Join(args, " "), stderr.String(), err)
	}
	return stdout.String(), nil
}
