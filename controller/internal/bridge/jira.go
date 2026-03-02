// Package bridge provides the JIRA REST API v3 HTTP client.
//
// JiraClient wraps JIRA Cloud REST API methods needed by the jira-bridge:
// search, get issue, transition, comment, and remote link operations.
// It uses basic auth (email:apiToken) and returns typed Go structs.
package bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// JiraClient is an HTTP client for the JIRA REST API v3.
type JiraClient struct {
	baseURL    string
	authHeader string
	httpClient *http.Client
	logger     *slog.Logger
}

// JiraClientConfig holds configuration for creating a JiraClient.
type JiraClientConfig struct {
	BaseURL  string // e.g., "https://pihealth.atlassian.net"
	Email    string
	APIToken string
	Logger   *slog.Logger
}

// NewJiraClient creates a new JIRA REST API client.
func NewJiraClient(cfg JiraClientConfig) *JiraClient {
	auth := base64.StdEncoding.EncodeToString([]byte(cfg.Email + ":" + cfg.APIToken))
	return &JiraClient{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		authHeader: "Basic " + auth,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     cfg.Logger,
	}
}

// JiraIssue represents a JIRA issue from the REST API.
type JiraIssue struct {
	Key    string          `json:"key"`
	ID     string          `json:"id"`
	Fields JiraIssueFields `json:"fields"`
}

// JiraIssueFields contains the fields of a JIRA issue.
type JiraIssueFields struct {
	Summary     string          `json:"summary"`
	Description json.RawMessage `json:"description"` // ADF format
	Status      *JiraNamedRef   `json:"status"`
	IssueType   *JiraNamedRef   `json:"issuetype"`
	Priority    *JiraNamedRef   `json:"priority"`
	Reporter    *JiraUser       `json:"reporter"`
	Assignee    *JiraUser       `json:"assignee"`
	Labels      []string        `json:"labels"`
	Parent      *JiraParentRef  `json:"parent"` // epic link
	Created     string           `json:"created"`
	Updated     string           `json:"updated"`
	IssueLinks  []JiraIssueLink  `json:"issuelinks"`
	Attachments []JiraAttachment `json:"attachment"`
}

// JiraNamedRef is a JIRA object with a name field (status, priority, issuetype).
type JiraNamedRef struct {
	Name string `json:"name"`
}

// JiraUser represents a JIRA user.
type JiraUser struct {
	DisplayName string `json:"displayName"`
	AccountID   string `json:"accountId"`
}

// JiraParentRef is a reference to a parent issue (epic).
type JiraParentRef struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
	} `json:"fields"`
}

// JiraIssueLink represents a link between two JIRA issues.
type JiraIssueLink struct {
	Type         JiraLinkType   `json:"type"`
	InwardIssue  *JiraLinkedRef `json:"inwardIssue,omitempty"`
	OutwardIssue *JiraLinkedRef `json:"outwardIssue,omitempty"`
}

// JiraLinkType describes the semantics of an issue link.
type JiraLinkType struct {
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// JiraLinkedRef is a minimal reference to a linked issue.
type JiraLinkedRef struct {
	Key string `json:"key"`
}

// JiraAttachment represents a JIRA issue attachment.
type JiraAttachment struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mimeType"`
	Size      int64  `json:"size"`
	Content   string `json:"content"`
	Thumbnail string `json:"thumbnail"`
	Created   string `json:"created"`
}

// jiraTransition represents a JIRA issue transition.
type jiraTransition struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	To   *JiraNamedRef `json:"to"`
}

// SearchIssues searches JIRA issues using JQL (single page).
func (c *JiraClient) SearchIssues(ctx context.Context, jql string, fields []string, maxResults int) ([]JiraIssue, error) {
	page, err := c.SearchIssuesPage(ctx, jql, fields, maxResults, "")
	if err != nil {
		return nil, err
	}
	return page.Issues, nil
}

// JiraSearchPage holds a page of JIRA search results with cursor token.
type JiraSearchPage struct {
	Issues        []JiraIssue
	NextPageToken string // empty when no more pages
}

// SearchIssuesPage fetches a single page of JIRA search results.
// Pass an empty pageToken for the first page.
func (c *JiraClient) SearchIssuesPage(ctx context.Context, jql string, fields []string, maxResults int, pageToken string) (*JiraSearchPage, error) {
	q := url.Values{}
	q.Set("jql", jql)
	if len(fields) > 0 {
		q.Set("fields", strings.Join(fields, ","))
	}
	if maxResults > 0 {
		q.Set("maxResults", fmt.Sprintf("%d", maxResults))
	}
	if pageToken != "" {
		q.Set("nextPageToken", pageToken)
	}

	var result struct {
		Issues        []JiraIssue `json:"issues"`
		NextPageToken string      `json:"nextPageToken"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/rest/api/3/search/jql?"+q.Encode(), nil, &result); err != nil {
		return nil, fmt.Errorf("JIRA search: %w", err)
	}
	return &JiraSearchPage{
		Issues:        result.Issues,
		NextPageToken: result.NextPageToken,
	}, nil
}

// GetIssue fetches a single JIRA issue by key.
func (c *JiraClient) GetIssue(ctx context.Context, key string) (*JiraIssue, error) {
	var issue JiraIssue
	if err := c.doJSON(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key), nil, &issue); err != nil {
		return nil, fmt.Errorf("JIRA get issue %s: %w", key, err)
	}
	return &issue, nil
}

// TransitionIssue transitions a JIRA issue to the named status.
// It fetches available transitions and finds one matching transitionName.
func (c *JiraClient) TransitionIssue(ctx context.Context, key, transitionName string) error {
	// Get available transitions.
	var result struct {
		Transitions []jiraTransition `json:"transitions"`
	}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/transitions"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return fmt.Errorf("JIRA get transitions for %s: %w", key, err)
	}

	// Find matching transition.
	var transitionID string
	for _, t := range result.Transitions {
		if strings.EqualFold(t.Name, transitionName) || strings.EqualFold(t.To.Name, transitionName) {
			transitionID = t.ID
			break
		}
	}
	if transitionID == "" {
		return fmt.Errorf("JIRA transition %q not available for %s", transitionName, key)
	}

	// Execute transition.
	body := map[string]any{
		"transition": map[string]string{"id": transitionID},
	}
	if err := c.doJSON(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("JIRA transition %s to %s: %w", key, transitionName, err)
	}
	return nil
}

// AddComment adds a comment to a JIRA issue in Atlassian Document Format.
func (c *JiraClient) AddComment(ctx context.Context, key, text string) error {
	body := map[string]any{
		"body": adfParagraph(text),
	}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/comment"
	if err := c.doJSON(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("JIRA add comment to %s: %w", key, err)
	}
	return nil
}

// AddRemoteLink adds a remote link to a JIRA issue.
func (c *JiraClient) AddRemoteLink(ctx context.Context, key, linkURL, title string) error {
	body := map[string]any{
		"object": map[string]any{
			"url":   linkURL,
			"title": title,
		},
	}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/remotelink"
	if err := c.doJSON(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("JIRA add remote link to %s: %w", key, err)
	}
	return nil
}

// doJSON performs an HTTP request against the JIRA API with JSON body/response.
func (c *JiraClient) doJSON(ctx context.Context, method, path string, body any, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal JIRA request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return fmt.Errorf("create JIRA request: %w", err)
	}
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("JIRA request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read JIRA response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("JIRA API %s %s returned %d: %s", method, path, resp.StatusCode, truncate(string(respBody), 512))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decode JIRA response: %w", err)
		}
	}
	return nil
}

// adfParagraph creates a minimal ADF document with a single paragraph.
func adfParagraph(text string) map[string]any {
	return map[string]any{
		"version": 1,
		"type":    "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": text,
					},
				},
			},
		},
	}
}

// adfToMarkdown converts an Atlassian Document Format JSON blob to markdown.
// Handles paragraph, text, heading, bulletList, orderedList, codeBlock,
// and hardBreak nodes. Unknown nodes are silently skipped.
func adfToMarkdown(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var doc struct {
		Content []adfNode `json:"content"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}

	var sb strings.Builder
	for _, node := range doc.Content {
		renderADFNode(&sb, node, 0)
	}
	return strings.TrimSpace(sb.String())
}

// adfNode is a recursive ADF content node.
type adfNode struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Content []adfNode       `json:"content,omitempty"`
	Attrs   json.RawMessage `json:"attrs,omitempty"`
	Marks   []adfMark       `json:"marks,omitempty"`
}

// adfMark represents an inline mark (bold, italic, code, link).
type adfMark struct {
	Type  string          `json:"type"`
	Attrs json.RawMessage `json:"attrs,omitempty"`
}

func renderADFNode(sb *strings.Builder, node adfNode, depth int) {
	switch node.Type {
	case "doc":
		for _, child := range node.Content {
			renderADFNode(sb, child, depth)
		}

	case "paragraph":
		renderADFInline(sb, node.Content)
		sb.WriteString("\n\n")

	case "heading":
		level := 1
		if len(node.Attrs) > 0 {
			var attrs struct {
				Level int `json:"level"`
			}
			if json.Unmarshal(node.Attrs, &attrs) == nil && attrs.Level > 0 {
				level = attrs.Level
			}
		}
		sb.WriteString(strings.Repeat("#", level) + " ")
		renderADFInline(sb, node.Content)
		sb.WriteString("\n\n")

	case "bulletList":
		for _, item := range node.Content {
			if item.Type == "listItem" {
				sb.WriteString("- ")
				for i, child := range item.Content {
					if i > 0 {
						sb.WriteString("  ")
					}
					renderADFInline(sb, child.Content)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")

	case "orderedList":
		for i, item := range node.Content {
			if item.Type == "listItem" {
				sb.WriteString(fmt.Sprintf("%d. ", i+1))
				for j, child := range item.Content {
					if j > 0 {
						sb.WriteString("   ")
					}
					renderADFInline(sb, child.Content)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")

	case "codeBlock":
		lang := ""
		if len(node.Attrs) > 0 {
			var attrs struct {
				Language string `json:"language"`
			}
			if json.Unmarshal(node.Attrs, &attrs) == nil {
				lang = attrs.Language
			}
		}
		sb.WriteString("```" + lang + "\n")
		renderADFInline(sb, node.Content)
		sb.WriteString("\n```\n\n")

	case "blockquote":
		for _, child := range node.Content {
			sb.WriteString("> ")
			renderADFInline(sb, child.Content)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
}

func renderADFInline(sb *strings.Builder, nodes []adfNode) {
	for _, n := range nodes {
		switch n.Type {
		case "text":
			text := n.Text
			// Apply marks (bold, italic, code).
			for _, mark := range n.Marks {
				switch mark.Type {
				case "strong":
					text = "**" + text + "**"
				case "em":
					text = "_" + text + "_"
				case "code":
					text = "`" + text + "`"
				case "link":
					var attrs struct {
						Href string `json:"href"`
					}
					if json.Unmarshal(mark.Attrs, &attrs) == nil && attrs.Href != "" {
						text = "[" + text + "](" + attrs.Href + ")"
					}
				}
			}
			sb.WriteString(text)
		case "hardBreak":
			sb.WriteString("\n")
		case "inlineCard":
			if len(n.Attrs) > 0 {
				var attrs struct {
					URL string `json:"url"`
				}
				if json.Unmarshal(n.Attrs, &attrs) == nil && attrs.URL != "" {
					sb.WriteString(attrs.URL)
				}
			}
		}
	}
}

// MapJiraPriority maps JIRA priority names to bead priority integers.
// Highest/Critical → 0, High → 1, Medium → 2, Low/Lowest → 3.
func MapJiraPriority(name string) int {
	switch strings.ToLower(name) {
	case "highest", "critical", "blocker":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low", "lowest", "trivial":
		return 3
	default:
		return 2
	}
}

// MapJiraLinkType maps JIRA issue link type names to canonical dependency types.
func MapJiraLinkType(name string) string {
	switch strings.ToLower(name) {
	case "blocks":
		return "blocks"
	case "relates":
		return "relates"
	case "duplicate":
		return "duplicate"
	case "action item":
		return "action-item"
	case "escalate":
		return "escalate"
	case "cloners":
		return "clones"
	default:
		return "jira-link"
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
