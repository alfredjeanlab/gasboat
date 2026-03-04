package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

// UnreleasedResponse is the JSON response for the /api/unreleased endpoint.
type UnreleasedResponse struct {
	Repos   []UnreleasedRepo        `json:"repos"`
	Images  []UnreleasedImage       `json:"images,omitempty"`
	Cluster *controllerVersionInfo  `json:"cluster,omitempty"`
	Bridge  string                  `json:"bridge"`
}

// UnreleasedImage holds unreleased build context changes for a container image.
type UnreleasedImage struct {
	Name         string           `json:"name"`
	DeployedTag  string           `json:"deployedTag"`
	AheadBy      int              `json:"aheadBy"`      // total commits in range
	ImageAheadBy int              `json:"imageAheadBy"` // files touching image paths
	Files        []string         `json:"files,omitempty"`
	Commits      []UnreleasedCommit `json:"commits,omitempty"`
	Error        string           `json:"error,omitempty"`
}

// UnreleasedRepo holds unreleased commit info for a single repository.
type UnreleasedRepo struct {
	Repo      string           `json:"repo"`
	LatestTag string           `json:"latestTag,omitempty"`
	AheadBy   int              `json:"aheadBy"`
	Commits   []UnreleasedCommit `json:"commits,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// UnreleasedCommit is a single commit in the unreleased response.
type UnreleasedCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
}

// UnreleasedConfig holds the dependencies needed to serve unreleased data.
type UnreleasedConfig struct {
	GitHub        *GitHubClient
	Repos         []RepoRef
	ControllerURL string
	Version       string
	Images        []ImageTrackConfig
}

// GetUnreleasedData fetches unreleased commits across all tracked repos and
// the controller version info. It returns a JSON-serializable response.
func GetUnreleasedData(ctx context.Context, cfg UnreleasedConfig) *UnreleasedResponse {
	resp := &UnreleasedResponse{
		Repos:  make([]UnreleasedRepo, len(cfg.Repos)),
		Bridge: cfg.Version,
	}

	var wg sync.WaitGroup

	// Fetch all repos concurrently (requires GitHub client).
	if cfg.GitHub != nil {
		for i, repo := range cfg.Repos {
			wg.Add(1)
			go func(idx int, r RepoRef) {
				defer wg.Done()
				result := cfg.GitHub.GetUnreleased(ctx, r, "main")
				entry := UnreleasedRepo{
					Repo:      r.String(),
					LatestTag: result.LatestTag,
					AheadBy:   result.AheadBy,
				}
				if result.Error != nil {
					entry.Error = result.Error.Error()
				}
				for _, c := range result.Commits {
					entry.Commits = append(entry.Commits, UnreleasedCommit{
						SHA:     c.SHA,
						Message: firstLine(c.Message),
						Author:  c.Author,
					})
				}
				resp.Repos[idx] = entry
			}(i, repo)
		}
	}

	// Fetch image unreleased data concurrently.
	if cfg.GitHub != nil && len(cfg.Images) > 0 {
		resp.Images = make([]UnreleasedImage, len(cfg.Images))
		for i, img := range cfg.Images {
			wg.Add(1)
			go func(idx int, ic ImageTrackConfig) {
				defer wg.Done()
				result := cfg.GitHub.GetImageUnreleased(ctx, ic, "main")
				entry := UnreleasedImage{
					Name:         result.Name,
					DeployedTag:  result.DeployedTag,
					AheadBy:      result.AheadBy,
					ImageAheadBy: result.ImageAheadBy,
					Files:        result.Files,
				}
				if result.Error != nil {
					entry.Error = result.Error.Error()
				}
				for _, c := range result.Commits {
					entry.Commits = append(entry.Commits, UnreleasedCommit{
						SHA:     c.SHA,
						Message: firstLine(c.Message),
						Author:  c.Author,
					})
				}
				resp.Images[idx] = entry
			}(i, img)
		}
	}

	// Fetch controller version concurrently.
	if cfg.ControllerURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp.Cluster = fetchControllerVersion(ctx, cfg.ControllerURL)
		}()
	}

	wg.Wait()
	return resp
}

// NewGitHubClientIfConfigured creates a GitHubClient when a token or repos are
// provided. Returns nil otherwise (matching Bot constructor behaviour).
func NewGitHubClientIfConfigured(token string, repos []RepoRef, logger *slog.Logger) *GitHubClient {
	if token != "" || len(repos) > 0 {
		return NewGitHubClient(token, logger)
	}
	return nil
}

// DefaultGasboatImageConfigs returns ImageTrackConfigs for standard gasboat images.
// The tag is extracted from the deployed version (typically calver like "2026.63.1").
// Pass an empty tag to skip that image.
func DefaultGasboatImageConfigs(repo RepoRef, controllerTag, agentTag, bridgeTag string) []ImageTrackConfig {
	var configs []ImageTrackConfig

	if agentTag != "" {
		configs = append(configs, ImageTrackConfig{
			Name:  "agent",
			Repo:  repo,
			Tag:   agentTag,
			Paths: []string{"images/agent/", ".rwx/docker.yml", ".rwx/agent-versions.lock"},
		})
	}
	if controllerTag != "" {
		configs = append(configs, ImageTrackConfig{
			Name:  "controller",
			Repo:  repo,
			Tag:   controllerTag,
			Paths: []string{"controller/"},
		})
	}
	if bridgeTag != "" {
		configs = append(configs, ImageTrackConfig{
			Name:  "slack-bridge",
			Repo:  repo,
			Tag:   bridgeTag,
			Paths: []string{"images/slack-bridge/", "controller/"},
		})
	}
	return configs
}

// ExtractImageTag extracts the tag from an image reference like
// "ghcr.io/groblegark/gasboat/agent:2026.63.1" → "2026.63.1".
// Returns empty string if no tag is present.
func ExtractImageTag(imageRef string) string {
	if imageRef == "" {
		return ""
	}
	// Strip digest suffix.
	if i := strings.Index(imageRef, "@"); i > 0 {
		imageRef = imageRef[:i]
	}
	if i := strings.LastIndex(imageRef, ":"); i > 0 {
		tag := imageRef[i+1:]
		// Ensure it's a tag, not a port.
		if !strings.Contains(tag, "/") {
			return tag
		}
	}
	return ""
}

// HandleUnreleased returns an http.HandlerFunc that serves unreleased data as JSON.
func HandleUnreleased(cfg UnreleasedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := GetUnreleasedData(r.Context(), cfg)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(data)
	}
}
