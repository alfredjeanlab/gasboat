package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GitHubClient is a lightweight REST client for the GitHub API.
type GitHubClient struct {
	httpClient *http.Client
	baseURL    string // defaults to "https://api.github.com"
	token      string
	logger     *slog.Logger
}

// RepoRef identifies a GitHub repository.
type RepoRef struct {
	Owner string
	Repo  string
}

func (r RepoRef) String() string { return r.Owner + "/" + r.Repo }

// RepoUnreleased holds the result of checking unreleased commits for a repo.
type RepoUnreleased struct {
	Repo      RepoRef
	LatestTag string
	AheadBy   int
	Commits   []GitCommit
	Error     error
}

// GitCommit is a single commit from the GitHub compare API.
type GitCommit struct {
	SHA     string
	Message string
	Author  string
}

// ghTag is a tag from the GitHub tags API.
type ghTag struct {
	Name string `json:"name"`
}

// ghCompare is the response from the GitHub compare API.
type ghCompare struct {
	AheadBy int `json:"ahead_by"`
	Commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
	} `json:"commits"`
}

// NewGitHubClient creates a new GitHub REST API client.
// Token is optional; when set it is sent as a Bearer token for higher rate limits.
func NewGitHubClient(token string, logger *slog.Logger) *GitHubClient {
	return &GitHubClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseURL:    "https://api.github.com",
		token:      token,
		logger:     logger,
	}
}

// calverRe matches calver tags like "2026.60.2" or "v2026.60.2".
var calverRe = regexp.MustCompile(`^v?(\d{4})\.(\d+)\.(\d+)$`)

// calverKey extracts (year, dayOfYear, build) from a calver tag.
// Returns ok=false if the tag doesn't match.
func calverKey(tag string) (year, doy, build int, ok bool) {
	m := calverRe.FindStringSubmatch(tag)
	if m == nil {
		return 0, 0, 0, false
	}
	year, _ = strconv.Atoi(m[1])
	doy, _ = strconv.Atoi(m[2])
	build, _ = strconv.Atoi(m[3])
	return year, doy, build, true
}

// GetLatestTag returns the most recent calver tag for the repo.
// It fetches up to 100 tags, picks the highest calver tag by
// (year, dayOfYear, build), and falls back to the first tag if
// none match the calver format.
func (c *GitHubClient) GetLatestTag(ctx context.Context, repo RepoRef) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/tags?per_page=100", c.baseURL, repo.Owner, repo.Repo)
	var tags []ghTag
	if err := c.doJSON(ctx, url, &tags); err != nil {
		return "", err
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("no tags found for %s", repo)
	}

	// Collect tags that match calver format.
	type calverTag struct {
		name               string
		year, doy, build   int
	}
	var cv []calverTag
	for _, t := range tags {
		if y, d, b, ok := calverKey(t.Name); ok {
			cv = append(cv, calverTag{name: t.Name, year: y, doy: d, build: b})
		}
	}
	if len(cv) == 0 {
		return tags[0].Name, nil
	}

	sort.Slice(cv, func(i, j int) bool {
		if cv[i].year != cv[j].year {
			return cv[i].year > cv[j].year
		}
		if cv[i].doy != cv[j].doy {
			return cv[i].doy > cv[j].doy
		}
		return cv[i].build > cv[j].build
	})
	return cv[0].name, nil
}

// CompareTagToHead compares a tag to the head of a branch.
func (c *GitHubClient) CompareTagToHead(ctx context.Context, repo RepoRef, tag, branch string) (*ghCompare, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s", c.baseURL, repo.Owner, repo.Repo, tag, branch)
	var result ghCompare
	if err := c.doJSON(ctx, url, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetUnreleased fetches unreleased commits for a repo (tag → branch head).
func (c *GitHubClient) GetUnreleased(ctx context.Context, repo RepoRef, branch string) *RepoUnreleased {
	result := &RepoUnreleased{Repo: repo}

	tag, err := c.GetLatestTag(ctx, repo)
	if err != nil {
		result.Error = fmt.Errorf("get latest tag: %w", err)
		return result
	}
	result.LatestTag = tag

	cmp, err := c.CompareTagToHead(ctx, repo, tag, branch)
	if err != nil {
		result.Error = fmt.Errorf("compare %s...%s: %w", tag, branch, err)
		return result
	}
	result.AheadBy = cmp.AheadBy

	// Cap at 10 commits for Slack block limits.
	limit := len(cmp.Commits)
	if limit > 10 {
		limit = 10
	}
	for _, c := range cmp.Commits[len(cmp.Commits)-limit:] {
		result.Commits = append(result.Commits, GitCommit{
			SHA:     c.SHA,
			Message: c.Commit.Message,
			Author:  c.Commit.Author.Name,
		})
	}
	return result
}

// ghPackageVersion is a version from the GitHub Packages API.
type ghPackageVersion struct {
	ID       int    `json:"id"`
	Name     string `json:"name"` // For containers, this is the digest.
	Metadata struct {
		PackageType string `json:"package_type"`
		Container   struct {
			Tags []string `json:"tags"`
		} `json:"container"`
	} `json:"metadata"`
}

// GetCommitForDigest maps a GHCR image digest to the git commit SHA that
// produced it. It queries the GitHub Packages API for the container version
// matching the digest, then extracts the commit from sha-<hex> convention tags
// (set by docker/metadata-action during CI builds).
//
// imageRef is the image name (e.g., "ghcr.io/groblegark/gasboat" or
// "ghcr.io/groblegark/gasboat:latest"). The tag/digest suffix is stripped.
// digest is the registry content digest (e.g., "sha256:abc123...").
func (c *GitHubClient) GetCommitForDigest(ctx context.Context, imageRef, digest string) (string, error) {
	org, pkg, err := parseGHCRImageRef(imageRef)
	if err != nil {
		return "", err
	}

	// Paginate through package versions to find the matching digest.
	// Cap at 5 pages (500 versions) to avoid runaway requests.
	for page := 1; page <= 5; page++ {
		url := fmt.Sprintf("%s/orgs/%s/packages/container/%s/versions?per_page=100&page=%d",
			c.baseURL, org, pkg, page)
		var versions []ghPackageVersion
		if err := c.doJSON(ctx, url, &versions); err != nil {
			return "", fmt.Errorf("list package versions: %w", err)
		}
		if len(versions) == 0 {
			break
		}

		for _, v := range versions {
			if v.Name != digest {
				continue
			}
			// Found the version. Look for a sha-<hex> tag.
			for _, tag := range v.Metadata.Container.Tags {
				if sha, ok := strings.CutPrefix(tag, "sha-"); ok && isHexString(sha) {
					return sha, nil
				}
			}
			return "", fmt.Errorf("digest %s found in %s/%s but has no sha-* commit tag",
				shortDigest(digest), org, pkg)
		}
	}

	return "", fmt.Errorf("no package version matches digest %s in %s/%s",
		shortDigest(digest), org, pkg)
}

// parseGHCRImageRef extracts the org and package name from a GHCR image
// reference like "ghcr.io/groblegark/gasboat" or "ghcr.io/groblegark/gasboat:latest".
func parseGHCRImageRef(imageRef string) (org, pkg string, err error) {
	ref := imageRef
	// Strip tag suffix (e.g., ":latest").
	if i := strings.LastIndex(ref, ":"); i > 0 {
		if !strings.Contains(ref[i+1:], "/") {
			ref = ref[:i]
		}
	}
	// Strip digest suffix (e.g., "@sha256:...").
	if i := strings.Index(ref, "@"); i > 0 {
		ref = ref[:i]
	}

	// Expected format: "ghcr.io/org/package".
	parts := strings.SplitN(ref, "/", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("invalid GHCR image reference %q: expected ghcr.io/<org>/<package>", imageRef)
	}
	return parts[1], parts[2], nil
}

// isHexString reports whether s is a non-empty string of hexadecimal digits.
func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// shortDigest returns a truncated digest for error messages.
func shortDigest(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		return "sha256:" + d[:12] + "..."
	}
	return digest
}

// doJSON performs a GET request and decodes the JSON response.
func (c *GitHubClient) doJSON(ctx context.Context, url string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read GitHub response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitHub API %s returned %d: %s", url, resp.StatusCode, truncate(string(body), 256))
	}

	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}
