// Package bridge provides the GitLab REST API v4 HTTP client.
//
// GitLabClient wraps GitLab REST API methods needed by the gitlab-bridge:
// merge request retrieval, merged MR polling, and pipeline status queries.
// It uses PRIVATE-TOKEN auth (Group Access Token) and returns typed Go structs.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GitLabClient is an HTTP client for the GitLab REST API v4.
type GitLabClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     *slog.Logger
}

// GitLabClientConfig holds configuration for creating a GitLabClient.
type GitLabClientConfig struct {
	BaseURL string // e.g., "https://gitlab.com"
	Token   string // Group Access Token with read_api scope
	Logger  *slog.Logger
}

// NewGitLabClient creates a new GitLab REST API client.
func NewGitLabClient(cfg GitLabClientConfig) *GitLabClient {
	return &GitLabClient{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		token:      cfg.Token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     cfg.Logger,
	}
}

// GitLabMR represents a GitLab merge request from the REST API.
type GitLabMR struct {
	ID             int            `json:"id"`
	IID            int            `json:"iid"`
	Title          string         `json:"title"`
	State          string         `json:"state"` // opened, closed, merged, locked
	MergedAt       *string        `json:"merged_at"`
	WebURL         string         `json:"web_url"`
	SourceBranch   string         `json:"source_branch"`
	TargetBranch   string         `json:"target_branch"`
	ProjectID      int            `json:"project_id"`
	Author         *GitLabUser    `json:"author"`
	MergedBy       *GitLabUser    `json:"merged_by"`
	HeadPipeline   *GitLabPipRef  `json:"head_pipeline"`
	SHA            string         `json:"sha"`
	UpdatedAt      string         `json:"updated_at"`
	Description    string         `json:"description"`
	Labels         []string       `json:"labels"`
	Draft          bool           `json:"draft"`
	HasConflicts   bool           `json:"has_conflicts"`
	MergeStatus    string         `json:"merge_status"` // can_be_merged, cannot_be_merged, etc.
}

// GitLabUser represents a GitLab user.
type GitLabUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// GitLabPipRef is a minimal reference to a pipeline embedded in an MR.
type GitLabPipRef struct {
	ID     int    `json:"id"`
	Status string `json:"status"` // created, pending, running, success, failed, canceled, skipped
	WebURL string `json:"web_url"`
}

// GitLabPipeline represents a full pipeline from the REST API.
type GitLabPipeline struct {
	ID        int    `json:"id"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha"`
	WebURL    string `json:"web_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// GetMergeRequest fetches a single merge request by project ID and MR IID.
// GET /projects/:id/merge_requests/:merge_request_iid
func (c *GitLabClient) GetMergeRequest(ctx context.Context, projectID, mrIID int) (*GitLabMR, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d", projectID, mrIID)
	var mr GitLabMR
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &mr); err != nil {
		return nil, fmt.Errorf("GitLab get MR %d!%d: %w", projectID, mrIID, err)
	}
	return &mr, nil
}

// GetMergeRequestByPath fetches a merge request using an encoded project path and MR IID.
// GET /projects/:id/merge_requests/:merge_request_iid
func (c *GitLabClient) GetMergeRequestByPath(ctx context.Context, projectPath string, mrIID int) (*GitLabMR, error) {
	encoded := url.PathEscape(projectPath)
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", encoded, mrIID)
	var mr GitLabMR
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &mr); err != nil {
		return nil, fmt.Errorf("GitLab get MR %s!%d: %w", projectPath, mrIID, err)
	}
	return &mr, nil
}

// ListMergedMRs lists merge requests with state=merged in a group, updated after the given time.
// GET /groups/:id/merge_requests?state=merged&updated_after=...
func (c *GitLabClient) ListMergedMRs(ctx context.Context, groupID int, updatedAfter time.Time) ([]GitLabMR, error) {
	q := url.Values{}
	q.Set("state", "merged")
	q.Set("updated_after", updatedAfter.UTC().Format(time.RFC3339))
	q.Set("per_page", "100")
	q.Set("order_by", "updated_at")
	q.Set("sort", "desc")

	path := fmt.Sprintf("/api/v4/groups/%d/merge_requests?%s", groupID, q.Encode())
	var mrs []GitLabMR
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &mrs); err != nil {
		return nil, fmt.Errorf("GitLab list merged MRs for group %d: %w", groupID, err)
	}
	return mrs, nil
}

// GetPipeline fetches a pipeline by project ID and pipeline ID.
// GET /projects/:id/pipelines/:pipeline_id
func (c *GitLabClient) GetPipeline(ctx context.Context, projectID, pipelineID int) (*GitLabPipeline, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/pipelines/%d", projectID, pipelineID)
	var pip GitLabPipeline
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &pip); err != nil {
		return nil, fmt.Errorf("GitLab get pipeline %d/%d: %w", projectID, pipelineID, err)
	}
	return &pip, nil
}

// GitLabJob represents a CI job within a pipeline.
type GitLabJob struct {
	ID         int            `json:"id"`
	Name       string         `json:"name"`
	Stage      string         `json:"stage"`
	Status     string         `json:"status"` // created, pending, running, success, failed, canceled, skipped
	WebURL     string         `json:"web_url"`
	Artifacts  []GitLabArtifact `json:"artifacts"`
	Pipeline   GitLabPipRef   `json:"pipeline"`
	FinishedAt *string        `json:"finished_at"`
}

// GitLabArtifact represents an artifact archive in a CI job.
type GitLabArtifact struct {
	FileType   string `json:"file_type"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	FileFormat string `json:"file_format"`
}

// ListPipelineJobs lists all jobs for a pipeline.
// GET /projects/:id/pipelines/:pipeline_id/jobs
func (c *GitLabClient) ListPipelineJobs(ctx context.Context, projectID, pipelineID int) ([]GitLabJob, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/pipelines/%d/jobs?per_page=100", projectID, pipelineID)
	var jobs []GitLabJob
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &jobs); err != nil {
		return nil, fmt.Errorf("GitLab list jobs for pipeline %d/%d: %w", projectID, pipelineID, err)
	}
	return jobs, nil
}

// ListPipelineJobsByPath lists all jobs for a pipeline using an encoded project path.
func (c *GitLabClient) ListPipelineJobsByPath(ctx context.Context, projectPath string, pipelineID int) ([]GitLabJob, error) {
	encoded := url.PathEscape(projectPath)
	path := fmt.Sprintf("/api/v4/projects/%s/pipelines/%d/jobs?per_page=100", encoded, pipelineID)
	var jobs []GitLabJob
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &jobs); err != nil {
		return nil, fmt.Errorf("GitLab list jobs for pipeline %s/%d: %w", projectPath, pipelineID, err)
	}
	return jobs, nil
}

// GetJobLog fetches the log (trace) output for a job.
// GET /projects/:id/jobs/:job_id/trace
func (c *GitLabClient) GetJobLog(ctx context.Context, projectPath string, jobID int) (string, error) {
	encoded := url.PathEscape(projectPath)
	reqURL := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/trace", c.baseURL, encoded, jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("create job log request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch job log: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("job log returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read job log: %w", err)
	}
	return string(data), nil
}

// DownloadJobArtifacts downloads the artifact archive for a job.
// GET /projects/:id/jobs/:job_id/artifacts
func (c *GitLabClient) DownloadJobArtifacts(ctx context.Context, projectPath string, jobID int, w io.Writer) error {
	encoded := url.PathEscape(projectPath)
	reqURL := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts", c.baseURL, encoded, jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("create artifacts request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch artifacts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("artifacts download returned %d", resp.StatusCode)
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("download artifacts: %w", err)
	}
	return nil
}

// UpdateMergeRequestDescription updates only the description field of a merge request.
// PUT /projects/:id/merge_requests/:merge_request_iid
func (c *GitLabClient) UpdateMergeRequestDescription(ctx context.Context, projectPath string, mrIID int, description string) error {
	encoded := url.PathEscape(projectPath)
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", encoded, mrIID)
	body := map[string]string{"description": description}
	if err := c.doJSON(ctx, http.MethodPut, path, body, nil); err != nil {
		return fmt.Errorf("GitLab update MR description %s!%d: %w", projectPath, mrIID, err)
	}
	return nil
}

// MRRef holds the parsed components of a GitLab MR URL.
type MRRef struct {
	ProjectPath string // e.g., "PiHealth/CoreFICS/fics-helm-chart"
	IID         int    // merge request IID (project-scoped)
}

// mrURLPattern matches GitLab MR URLs:
//   https://gitlab.com/PiHealth/CoreFICS/fics-helm-chart/-/merge_requests/211
var mrURLPattern = regexp.MustCompile(`^https?://[^/]+/(.+?)/-/merge_requests/(\d+)`)

// ParseMRURL extracts the project path and MR IID from a GitLab MR URL.
// Returns nil if the URL does not match the expected pattern.
func ParseMRURL(rawURL string) *MRRef {
	m := mrURLPattern.FindStringSubmatch(rawURL)
	if m == nil {
		return nil
	}
	iid, err := strconv.Atoi(m[2])
	if err != nil {
		return nil
	}
	return &MRRef{
		ProjectPath: m[1],
		IID:         iid,
	}
}

// doJSON performs an HTTP request against the GitLab API with JSON response handling.
func (c *GitLabClient) doJSON(ctx context.Context, method, path string, body any, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal GitLab request: %w", err)
		}
		bodyReader = strings.NewReader(string(data))
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return fmt.Errorf("create GitLab request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GitLab request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read GitLab response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitLab API %s %s returned %d: %s", method, path, resp.StatusCode, truncate(string(respBody), 512))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decode GitLab response: %w", err)
		}
	}
	return nil
}
