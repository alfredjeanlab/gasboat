// Package bridge provides the JIRA polling loop.
//
// JiraPoller periodically queries JIRA for new issues matching configured
// JQL criteria and creates task beads in the beads daemon. It deduplicates
// by tracking JIRA key → bead ID mappings, and on startup runs a CatchUp
// pass to populate the tracked map from existing beads.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// JiraBeadClient is the subset of beadsapi.Client used by the JIRA poller.
type JiraBeadClient interface {
	CreateBead(ctx context.Context, req beadsapi.CreateBeadRequest) (string, error)
	ListTaskBeads(ctx context.Context) ([]*beadsapi.BeadDetail, error)
	AddDependency(ctx context.Context, beadID, dependsOnID, depType, createdBy string) error
	UpdateBeadFields(ctx context.Context, beadID string, fields map[string]string) error
}

// JiraPollerConfig holds configuration for the JIRA poller.
type JiraPollerConfig struct {
	Projects     []string          // JIRA project keys (e.g., ["PE", "DEVOPS"])
	Statuses     []string          // JIRA statuses to ingest
	IssueTypes   []string          // JIRA issue types to ingest
	PollInterval time.Duration     // Polling interval (default 60s)
	ProjectMap   map[string]string // JIRA prefix (upper) → boat project name (e.g., "PE" → "monorepo")
	Logger       *slog.Logger
}

// JiraPoller polls JIRA for new issues and creates task beads.
type JiraPoller struct {
	jira   *JiraClient
	daemon JiraBeadClient
	cfg    JiraPollerConfig

	mu        sync.Mutex
	tracked   map[string]string // JIRA key → bead ID
	wiredDeps map[string]bool   // "src:dst:type" dedup
}

// NewJiraPoller creates a new JIRA polling loop.
func NewJiraPoller(jira *JiraClient, daemon JiraBeadClient, cfg JiraPollerConfig) *JiraPoller {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 60 * time.Second
	}
	return &JiraPoller{
		jira:      jira,
		daemon:    daemon,
		cfg:       cfg,
		tracked:   make(map[string]string),
		wiredDeps: make(map[string]bool),
	}
}

// Run starts the polling loop. It runs CatchUp once, then polls at the
// configured interval until ctx is canceled.
func (p *JiraPoller) Run(ctx context.Context) error {
	// Populate tracked map from existing beads.
	p.CatchUp(ctx)

	p.cfg.Logger.Info("JIRA poller started",
		"projects", p.cfg.Projects,
		"statuses", p.cfg.Statuses,
		"issue_types", p.cfg.IssueTypes,
		"interval", p.cfg.PollInterval)

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	// Full scan on start to catch all matching issues across all pages.
	p.fullScan(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// CatchUp queries the daemon for existing task beads with source:jira label
// and populates the tracked map to prevent duplicate creation across restarts.
func (p *JiraPoller) CatchUp(ctx context.Context) {
	beads, err := p.daemon.ListTaskBeads(ctx)
	if err != nil {
		p.cfg.Logger.Warn("JIRA poller catch-up: failed to list task beads", "error", err)
		return
	}

	count := 0
	p.mu.Lock()
	for _, b := range beads {
		// Use jira_key field directly rather than the source:jira label — the list
		// API does not populate Labels (they live in a separate table), so label
		// checks here would silently skip every bead and prevent deduplication.
		jiraKey := b.Fields["jira_key"]
		if jiraKey != "" {
			p.tracked[jiraKey] = b.ID
			count++
		}
	}
	p.mu.Unlock()

	p.cfg.Logger.Info("JIRA poller catch-up complete", "tracked", count)
}

// fullScan paginates through all JIRA results using nextPageToken cursor
// to import issues missed by the single-page poll.
func (p *JiraPoller) fullScan(ctx context.Context) {
	jql := p.buildJQL()
	fields := []string{"summary", "description", "status", "issuetype", "priority", "reporter", "labels", "parent", "created", "updated", "issuelinks", "attachment"}

	totalCreated := 0
	totalSkipped := 0
	pageNum := 0
	var allIssues []JiraIssue
	pageToken := ""

	for {
		result, err := p.jira.SearchIssuesPage(ctx, jql, fields, 50, pageToken)
		if err != nil {
			// Retry once on rate limit.
			if strings.Contains(err.Error(), "429") {
				p.cfg.Logger.Warn("JIRA full scan rate limited, backing off", "page", pageNum)
				select {
				case <-ctx.Done():
					break
				case <-time.After(5 * time.Second):
				}
				result, err = p.jira.SearchIssuesPage(ctx, jql, fields, 50, pageToken)
			}
			if err != nil {
				p.cfg.Logger.Error("JIRA full scan page failed", "page", pageNum, "error", err)
				break
			}
		}

		if len(result.Issues) == 0 {
			break
		}

		for _, issue := range result.Issues {
			p.mu.Lock()
			_, exists := p.tracked[issue.Key]
			p.mu.Unlock()

			if exists {
				totalSkipped++
				continue
			}

			beadID, err := p.createBeadFromIssue(ctx, issue)
			if err != nil {
				p.cfg.Logger.Error("failed to create bead for JIRA issue",
					"key", issue.Key, "error", err)
				continue
			}

			p.mu.Lock()
			p.tracked[issue.Key] = beadID
			p.mu.Unlock()
			totalCreated++

			p.cfg.Logger.Info("created bead for JIRA issue",
				"key", issue.Key, "bead_id", beadID,
				"summary", issue.Fields.Summary)
		}

		allIssues = append(allIssues, result.Issues...)
		pageNum++

		// No more pages.
		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken

		// Pace to avoid rate limiting.
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}

	p.wireDependencies(ctx, allIssues)

	p.cfg.Logger.Info("JIRA full scan complete",
		"created", totalCreated, "skipped", totalSkipped, "pages", pageNum)
}

// poll executes a single JIRA poll cycle.
func (p *JiraPoller) poll(ctx context.Context) {
	// Refresh tracked from the daemon at the start of each poll. This makes
	// the poller idempotent across restarts — if CatchUp populated nothing
	// (e.g. due to a transient error), poll self-heals by checking live data.
	if beads, err := p.daemon.ListTaskBeads(ctx); err == nil {
		p.mu.Lock()
		for _, b := range beads {
			if key := b.Fields["jira_key"]; key != "" {
				if _, exists := p.tracked[key]; !exists {
					p.tracked[key] = b.ID
				}
			}
		}
		p.mu.Unlock()
	}

	jql := p.buildJQL()
	fields := []string{"summary", "description", "status", "issuetype", "priority", "reporter", "labels", "parent", "created", "updated", "issuelinks", "attachment"}

	issues, err := p.jira.SearchIssues(ctx, jql, fields, 50)
	if err != nil {
		p.cfg.Logger.Error("JIRA poll failed", "error", err)
		return
	}

	created := 0
	skipped := 0
	for _, issue := range issues {
		p.mu.Lock()
		_, exists := p.tracked[issue.Key]
		p.mu.Unlock()

		if exists {
			skipped++
			continue
		}

		beadID, err := p.createBeadFromIssue(ctx, issue)
		if err != nil {
			p.cfg.Logger.Error("failed to create bead for JIRA issue",
				"key", issue.Key, "error", err)
			continue
		}

		p.mu.Lock()
		p.tracked[issue.Key] = beadID
		p.mu.Unlock()
		created++

		p.cfg.Logger.Info("created bead for JIRA issue",
			"key", issue.Key, "bead_id", beadID,
			"summary", issue.Fields.Summary)
	}

	// Second pass: wire dependencies now that all beads from this batch exist.
	p.wireDependencies(ctx, issues)

	if created > 0 || p.cfg.Logger.Enabled(ctx, slog.LevelDebug) {
		p.cfg.Logger.Info("JIRA poll complete",
			"found", len(issues), "created", created, "skipped", skipped)
	}
}

// createBeadFromIssue creates a task bead from a JIRA issue.
func (p *JiraPoller) createBeadFromIssue(ctx context.Context, issue JiraIssue) (string, error) {
	// Build labels.
	labels := []string{
		"source:jira",
		"jira:" + issue.Key,
	}

	// Extract project key from issue key (e.g., "PE" from "PE-7001") and map
	// to the boat project name via ProjectMap. Falls back to the lowercased
	// JIRA prefix when no mapping is configured.
	project := ""
	if parts := strings.SplitN(issue.Key, "-", 2); len(parts) == 2 {
		jiraPrefix := strings.ToUpper(parts[0])
		boatProject, ok := p.cfg.ProjectMap[jiraPrefix]
		if !ok {
			boatProject = strings.ToLower(jiraPrefix)
		}
		project = jiraPrefix
		labels = append(labels, "project:"+boatProject)
	}

	// Add JIRA labels with prefix.
	for _, l := range issue.Fields.Labels {
		labels = append(labels, "jira-label:"+l)
	}

	// Tag epics for beads3d graph visibility.
	if issue.Fields.IssueType != nil && issue.Fields.IssueType.Name == "Epic" {
		labels = append(labels, "jira-epic")
	}

	// Build fields.
	fields := map[string]string{
		"jira_key":     issue.Key,
		"jira_project": strings.ToUpper(project),
		"jira_url":     p.jira.baseURL + "/browse/" + issue.Key,
	}
	if issue.Fields.IssueType != nil {
		fields["jira_type"] = issue.Fields.IssueType.Name
	}
	if issue.Fields.Status != nil {
		fields["jira_status"] = issue.Fields.Status.Name
	}
	if issue.Fields.Parent != nil {
		fields["jira_epic"] = issue.Fields.Parent.Key
	}
	if issue.Fields.Reporter != nil {
		fields["jira_reporter"] = issue.Fields.Reporter.DisplayName
	}

	// Issue link count for graph visibility.
	if len(issue.Fields.IssueLinks) > 0 {
		fields["jira_link_count"] = fmt.Sprintf("%d", len(issue.Fields.IssueLinks))
	}

	// Attachment metadata for beads3d media indicators.
	if len(issue.Fields.Attachments) > 0 {
		fields["jira_attachment_count"] = fmt.Sprintf("%d", len(issue.Fields.Attachments))
		var hasImages, hasVideo bool
		for _, att := range issue.Fields.Attachments {
			mime := strings.ToLower(att.MimeType)
			if strings.HasPrefix(mime, "image/") {
				hasImages = true
			}
			if strings.HasPrefix(mime, "video/") {
				hasVideo = true
			}
		}
		if hasImages {
			fields["jira_has_images"] = "true"
		}
		if hasVideo {
			fields["jira_has_video"] = "true"
		}
		// Always tag as media when attachments exist — the search/jql
		// endpoint often omits mimeType from attachment stubs, so the
		// image/video checks above may not fire even when screenshots
		// are present.
		labels = append(labels, "jira-has-media")
	}

	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("marshal fields: %w", err)
	}

	// Map priority.
	priority := 2 // default medium
	if issue.Fields.Priority != nil {
		priority = MapJiraPriority(issue.Fields.Priority.Name)
	}

	// Convert description from ADF to markdown.
	description := adfToMarkdown(issue.Fields.Description)

	title := fmt.Sprintf("[%s] %s", issue.Key, issue.Fields.Summary)

	beadID, err := p.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       title,
		Type:        "task",
		Kind:        "issue",
		Description: description,
		Labels:      labels,
		Priority:    priority,
		CreatedBy:   "jira-bridge",
		Fields:      fieldsJSON,
	})
	if err != nil {
		return "", fmt.Errorf("create bead: %w", err)
	}

	return beadID, nil
}

// wireDependencies wires child-of and issue-link edges for a batch of issues.
func (p *JiraPoller) wireDependencies(ctx context.Context, issues []JiraIssue) {
	p.mu.Lock()
	snapshot := make(map[string]string, len(p.tracked))
	for k, v := range p.tracked {
		snapshot[k] = v
	}
	p.mu.Unlock()

	wired := 0

	// Pass A: child-of edges (issue with Parent → parent bead).
	for _, issue := range issues {
		if issue.Fields.Parent == nil {
			continue
		}
		childID, childOK := snapshot[issue.Key]
		parentID, parentOK := snapshot[issue.Fields.Parent.Key]
		if !childOK || !parentOK {
			continue
		}
		dedupKey := childID + ":child-of:" + parentID
		if p.wiredDeps[dedupKey] {
			continue
		}
		if err := p.daemon.AddDependency(ctx, childID, parentID, "child-of", "jira-bridge"); err != nil {
			p.cfg.Logger.Warn("failed to wire child-of dependency",
				"child", issue.Key, "parent", issue.Fields.Parent.Key, "error", err)
			continue
		}
		p.wiredDeps[dedupKey] = true
		wired++
	}

	// Pass B: issue link edges.
	// Collect cross-project / unresolvable links per source bead.
	xlinks := make(map[string][]string) // bead ID → []"type:KEY"
	for _, issue := range issues {
		srcID, srcOK := snapshot[issue.Key]
		if !srcOK {
			continue
		}
		for _, link := range issue.Fields.IssueLinks {
			depType := MapJiraLinkType(link.Type.Name)

			// Determine the target issue key.
			var targetKey string
			if link.OutwardIssue != nil {
				targetKey = link.OutwardIssue.Key
			} else if link.InwardIssue != nil {
				targetKey = link.InwardIssue.Key
			}
			if targetKey == "" {
				continue
			}

			targetID, targetOK := snapshot[targetKey]
			if !targetOK {
				// Target not imported — store as cross-project link field.
				xlinks[srcID] = append(xlinks[srcID], depType+":"+targetKey)
				continue
			}

			dedupKey := srcID + ":" + depType + ":" + targetID
			if p.wiredDeps[dedupKey] {
				continue
			}
			if err := p.daemon.AddDependency(ctx, srcID, targetID, depType, "jira-bridge"); err != nil {
				p.cfg.Logger.Warn("failed to wire issue link dependency",
					"source", issue.Key, "target", targetKey, "type", depType, "error", err)
				continue
			}
			p.wiredDeps[dedupKey] = true
			wired++
		}
	}

	// Store cross-project links as a field on each source bead.
	for beadID, links := range xlinks {
		if err := p.daemon.UpdateBeadFields(ctx, beadID, map[string]string{
			"jira_xlinks": strings.Join(links, ","),
		}); err != nil {
			p.cfg.Logger.Warn("failed to store cross-project links",
				"bead", beadID, "links", links, "error", err)
		}
	}

	if wired > 0 {
		p.cfg.Logger.Info("wired JIRA dependencies", "count", wired)
	}
}

// buildJQL constructs the JQL query for polling.
func (p *JiraPoller) buildJQL() string {
	var parts []string

	if len(p.cfg.Projects) > 0 {
		parts = append(parts, "project IN ("+quoteJQL(p.cfg.Projects)+")")
	}
	if len(p.cfg.Statuses) > 0 {
		parts = append(parts, "status IN ("+quoteJQL(p.cfg.Statuses)+")")
	}
	if len(p.cfg.IssueTypes) > 0 {
		parts = append(parts, "issuetype IN ("+quoteJQL(p.cfg.IssueTypes)+")")
	}

	jql := strings.Join(parts, " AND ")
	jql += " ORDER BY created DESC"
	return jql
}

// quoteJQL wraps each value in double quotes for JQL IN clauses.
func quoteJQL(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = `"` + v + `"`
	}
	return strings.Join(quoted, ",")
}

// IsTracked returns true if the JIRA key is already tracked.
func (p *JiraPoller) IsTracked(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.tracked[key]
	return ok
}

// TrackedCount returns the number of tracked JIRA issues.
func (p *JiraPoller) TrackedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tracked)
}

// hasLabel is defined in chat.go — reused here for label filtering.
