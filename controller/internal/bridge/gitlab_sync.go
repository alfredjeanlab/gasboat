// Package bridge provides the GitLab MR merge watcher.
//
// GitLabSync subscribes to kbeads SSE bead events, watches for beads with
// mr_url fields pointing to GitLab MRs, and queries GitLab to detect merges.
// When an MR is merged, it sets mr_merged=true on the bead.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// GitLabBeadClient is the subset of beadsapi.Client used by GitLabSync.
type GitLabBeadClient interface {
	ListTaskBeads(ctx context.Context) ([]*beadsapi.BeadDetail, error)
	UpdateBeadFields(ctx context.Context, beadID string, fields map[string]string) error
	AddComment(ctx context.Context, beadID, author, text string) error
}

// GitLabSync watches for MR merges and updates bead fields.
type GitLabSync struct {
	gitlab *GitLabClient
	daemon GitLabBeadClient
	logger *slog.Logger
	nudge  NudgeFunc

	mu   sync.Mutex
	seen map[string]time.Time // dedup key → last check time
}

// NudgeFunc is an optional callback for sending nudges to agents.
type NudgeFunc func(ctx context.Context, agentName, message string) error

// GitLabSyncConfig holds configuration for the GitLab sync watcher.
type GitLabSyncConfig struct {
	GitLab *GitLabClient
	Daemon GitLabBeadClient
	Logger *slog.Logger
	Nudge  NudgeFunc // optional; if nil, nudges are skipped
}

// NewGitLabSync creates a new GitLab MR sync watcher.
func NewGitLabSync(cfg GitLabSyncConfig) *GitLabSync {
	return &GitLabSync{
		gitlab: cfg.GitLab,
		daemon: cfg.Daemon,
		logger: cfg.Logger,
		nudge:  cfg.Nudge,
		seen:   make(map[string]time.Time),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream.
// Watches for bead updates where mr_url is set — triggers MR status check,
// MR description sync, and review comment nudges.
func (s *GitLabSync) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.updated", s.handleUpdated)
	stream.On("beads.bead.updated", s.handleDescriptionSync)
	stream.On("beads.bead.updated", s.handleReviewNudge)
	s.logger.Info("GitLab sync watcher registered SSE handlers",
		"topics", []string{"beads.bead.updated"})
}

// handleUpdated checks if a bead's mr_url points to a GitLab MR and queries
// its merge status. If merged, sets mr_merged=true on the bead.
func (s *GitLabSync) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	mrURL := bead.Fields["mr_url"]
	if mrURL == "" {
		return
	}

	// Only process GitLab MR URLs.
	ref := ParseMRURL(mrURL)
	if ref == nil {
		return
	}

	// Skip if already marked as merged.
	if bead.Fields["mr_merged"] == "true" {
		return
	}

	// Dedup: don't re-check the same MR too frequently.
	dedupKey := "gitlab-mr:" + bead.ID + ":" + mrURL
	if s.isDuplicate(dedupKey) {
		return
	}

	s.logger.Info("checking GitLab MR status",
		"bead", bead.ID, "mr_url", mrURL, "project", ref.ProjectPath, "iid", ref.IID)

	mr, err := s.gitlab.GetMergeRequestByPath(ctx, ref.ProjectPath, ref.IID)
	if err != nil {
		s.logger.Error("failed to get GitLab MR",
			"bead", bead.ID, "mr_url", mrURL, "error", err)
		return
	}

	// Update bead with MR state regardless of merge status.
	fields := map[string]string{
		"mr_state":          mr.State,
		"gitlab_mr_iid":     strconv.Itoa(mr.IID),
		"gitlab_project_id": strconv.Itoa(mr.ProjectID),
	}
	if mr.HeadPipeline != nil {
		fields["mr_pipeline_status"] = mr.HeadPipeline.Status
		if mr.HeadPipeline.WebURL != "" {
			fields["mr_pipeline_url"] = mr.HeadPipeline.WebURL
		}
	}
	if mr.State == "merged" {
		fields["mr_merged"] = "true"
		s.logger.Info("MR merged — updating bead",
			"bead", bead.ID, "mr_url", mrURL)
	}

	if err := s.daemon.UpdateBeadFields(ctx, bead.ID, fields); err != nil {
		s.logger.Error("failed to update bead fields",
			"bead", bead.ID, "error", err)
	}
}

// mrDescFields are the bead fields that feed into the MR description section.
var mrDescFields = map[string]bool{
	"jira_key":            true,
	"jira_status":         true,
	"mr_state":            true,
	"mr_pipeline_status":  true,
	"mr_pipeline_url":     true,
	"mr_approved":         true,
	"mr_approvers":        true,
}

// handleDescriptionSync updates the gasboat-managed section of the GitLab MR
// description when relevant bead fields change.
func (s *GitLabSync) handleDescriptionSync(ctx context.Context, data []byte) {
	if s.gitlab == nil {
		return
	}

	bead := ParseBeadEvent(data)
	if bead == nil || bead.Type != "task" {
		return
	}

	mrURL := bead.Fields["mr_url"]
	if mrURL == "" {
		return
	}

	ref := ParseMRURL(mrURL)
	if ref == nil {
		return
	}

	// Only sync when a relevant field changed.
	if bead.Changes != nil {
		relevant := false
		for k := range bead.Changes {
			if mrDescFields[k] {
				relevant = true
				break
			}
		}
		if !relevant {
			return
		}
	}

	// Dedup: don't re-sync too frequently.
	dedupKey := "gitlab-mrdesc:" + bead.ID
	if s.isDuplicate(dedupKey) {
		return
	}

	section := MRDescriptionSection{
		BeadID:         bead.ID,
		JIRAKey:        bead.Fields["jira_key"],
		JIRAStatus:     bead.Fields["jira_status"],
		PipelineStatus: bead.Fields["mr_pipeline_status"],
		PipelineURL:    bead.Fields["mr_pipeline_url"],
		Approved:       bead.Fields["mr_approved"],
		Approvers:      bead.Fields["mr_approvers"],
		MRState:        bead.Fields["mr_state"],
	}

	if err := syncMRDescription(ctx, s.gitlab, ref.ProjectPath, ref.IID, section, s.logger); err != nil {
		s.logger.Error("failed to sync MR description",
			"bead", bead.ID, "mr_url", mrURL, "error", err)
	}
}

// handleReviewNudge nudges the assigned agent when mr_has_review_comments
// changes to true on a bead.
func (s *GitLabSync) handleReviewNudge(ctx context.Context, data []byte) {
	if s.nudge == nil {
		return
	}

	bead := ParseBeadEvent(data)
	if bead == nil || bead.Type != "task" {
		return
	}

	// Only nudge when mr_has_review_comments just changed.
	if bead.Changes == nil {
		return
	}
	if _, ok := bead.Changes["mr_has_review_comments"]; !ok {
		return
	}
	if bead.Fields["mr_has_review_comments"] != "true" {
		return
	}

	assignee := bead.Assignee
	if assignee == "" {
		return
	}

	// Dedup: don't nudge the same agent too frequently for the same bead.
	dedupKey := "gitlab-review-nudge:" + bead.ID
	if s.isDuplicate(dedupKey) {
		return
	}

	message := fmt.Sprintf("MR has new review comments — address them: %s", bead.Fields["mr_url"])
	if err := s.nudge(ctx, assignee, message); err != nil {
		s.logger.Error("failed to nudge agent for review comments",
			"bead", bead.ID, "agent", assignee, "error", err)
	} else {
		s.logger.Info("nudged agent for MR review comments",
			"bead", bead.ID, "agent", assignee)
	}
}

// isDuplicate returns true if the key was seen within the last 5 minutes.
func (s *GitLabSync) isDuplicate(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	// Periodic cleanup.
	for k, t := range s.seen {
		if now.Sub(t) > 5*time.Minute {
			delete(s.seen, k)
		}
	}

	if t, ok := s.seen[key]; ok && now.Sub(t) < 5*time.Minute {
		return true
	}
	s.seen[key] = now
	return false
}

// GitLabPoller periodically polls GitLab for recently merged MRs and updates
// matching beads. This is the fallback for when webhooks don't fire.
type GitLabPoller struct {
	gitlab       *GitLabClient
	daemon       GitLabBeadClient
	logger       *slog.Logger
	groupID      int
	pollInterval time.Duration
	lastPoll     time.Time
}

// GitLabPollerConfig holds configuration for the GitLab poller.
type GitLabPollerConfig struct {
	GitLab       *GitLabClient
	Daemon       GitLabBeadClient
	Logger       *slog.Logger
	GroupID      int
	PollInterval time.Duration
}

// NewGitLabPoller creates a new GitLab MR polling fallback.
func NewGitLabPoller(cfg GitLabPollerConfig) *GitLabPoller {
	return &GitLabPoller{
		gitlab:       cfg.GitLab,
		daemon:       cfg.Daemon,
		logger:       cfg.Logger,
		groupID:      cfg.GroupID,
		pollInterval: cfg.PollInterval,
		lastPoll:     time.Now().Add(-cfg.PollInterval), // poll immediately on first run
	}
}

// Run starts the polling loop. It blocks until ctx is canceled.
func (p *GitLabPoller) Run(ctx context.Context) error {
	p.logger.Info("starting GitLab MR poller",
		"group_id", p.groupID, "poll_interval", p.pollInterval)

	// Initial poll.
	p.poll(ctx)

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// poll checks GitLab for recently merged MRs and updates matching beads.
func (p *GitLabPoller) poll(ctx context.Context) {
	since := p.lastPoll
	p.lastPoll = time.Now()

	mrs, err := p.gitlab.ListMergedMRs(ctx, p.groupID, since)
	if err != nil {
		p.logger.Error("GitLab poll failed", "error", err)
		return
	}

	if len(mrs) == 0 {
		p.logger.Debug("no recently merged MRs", "since", since)
		return
	}

	p.logger.Info("found recently merged MRs", "count", len(mrs), "since", since)

	// Load beads with mr_url to match against.
	beads, err := p.daemon.ListTaskBeads(ctx)
	if err != nil {
		p.logger.Error("failed to list task beads", "error", err)
		return
	}

	// Build mr_url → bead index.
	urlIndex := make(map[string]*beadsapi.BeadDetail)
	for _, b := range beads {
		if u := b.Fields["mr_url"]; u != "" {
			urlIndex[u] = b
		}
	}

	// Match merged MRs to beads.
	for _, mr := range mrs {
		bead, ok := urlIndex[mr.WebURL]
		if !ok {
			continue
		}

		// Skip already-merged beads.
		if bead.Fields["mr_merged"] == "true" {
			continue
		}

		p.logger.Info("poll: MR merged — updating bead",
			"bead", bead.ID, "mr_url", mr.WebURL, "mr_iid", mr.IID)

		fields := map[string]string{
			"mr_merged":         "true",
			"mr_state":          "merged",
			"gitlab_mr_iid":     strconv.Itoa(mr.IID),
			"gitlab_project_id": strconv.Itoa(mr.ProjectID),
		}
		if mr.HeadPipeline != nil {
			fields["mr_pipeline_status"] = mr.HeadPipeline.Status
			if mr.HeadPipeline.WebURL != "" {
				fields["mr_pipeline_url"] = mr.HeadPipeline.WebURL
			}
		}

		if err := p.daemon.UpdateBeadFields(ctx, bead.ID, fields); err != nil {
			p.logger.Error("failed to update bead fields from poll",
				"bead", bead.ID, "error", err)
		}
	}
}

// GitLabWebhookHandler returns an http.Handler that processes GitLab webhook
// events for merge request merges.
func GitLabWebhookHandler(gitlab *GitLabClient, daemon GitLabBeadClient, webhookSecret string, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify webhook secret.
		if r.Header.Get("X-Gitlab-Token") != webhookSecret {
			logger.Warn("webhook: invalid X-Gitlab-Token")
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		var event struct {
			ObjectKind string `json:"object_kind"`
			User       struct {
				Username string `json:"username"`
			} `json:"user"`
			ObjectAttr struct {
				IID       int    `json:"iid"`
				State     string `json:"state"`
				Action    string `json:"action"`
				URL       string `json:"url"`
				ProjectID int    `json:"target_project_id"`
				// Pipeline-specific fields (when object_kind=pipeline).
				ID     int    `json:"id"`
				Status string `json:"status"`
				// Note-specific fields (when object_kind=note).
				Note         string `json:"note"`
				NoteableType string `json:"noteable_type"`
				System       bool   `json:"system"`
				Position     *struct {
					NewPath string `json:"new_path"`
					NewLine int    `json:"new_line"`
					OldPath string `json:"old_path"`
					OldLine int    `json:"old_line"`
				} `json:"position"`
			} `json:"object_attributes"`
			MergeRequest *struct {
				IID int    `json:"iid"`
				URL string `json:"url"`
			} `json:"merge_request"`
		}

		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			logger.Error("webhook: failed to decode body", "error", err)
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		switch event.ObjectKind {
		case "note":
			var pos *notePosition
			if event.ObjectAttr.Position != nil {
				pos = &notePosition{
					NewPath: event.ObjectAttr.Position.NewPath,
					NewLine: event.ObjectAttr.Position.NewLine,
					OldPath: event.ObjectAttr.Position.OldPath,
					OldLine: event.ObjectAttr.Position.OldLine,
				}
			}
			handleNoteWebhook(r.Context(), event.ObjectAttr.NoteableType, event.ObjectAttr.Note,
				event.ObjectAttr.System, event.User.Username, pos,
				event.MergeRequest, daemon, logger)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"processed","kind":"note"}`)
			return
		case "pipeline":
			handlePipelineWebhook(r.Context(), event.ObjectAttr.ID, event.ObjectAttr.Status, event.ObjectAttr.URL, event.MergeRequest, daemon, logger)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"processed","kind":"pipeline"}`)
			return
		case "merge_request":
			switch event.ObjectAttr.Action {
			case "merge":
				// Fall through to merge handling below.
			case "approved", "unapproved":
				handleApprovalWebhook(r.Context(), event.ObjectAttr.URL, event.ObjectAttr.Action, event.User.Username, daemon, logger)
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `{"status":"processed","action":"%s"}`, event.ObjectAttr.Action)
				return
			default:
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `{"status":"ignored","reason":"action=%s"}`, event.ObjectAttr.Action)
				return
			}
		default:
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"status":"ignored","reason":"kind=%s"}`, event.ObjectKind)
			return
		}

		logger.Info("webhook: MR merged",
			"mr_url", event.ObjectAttr.URL,
			"iid", event.ObjectAttr.IID,
			"project_id", event.ObjectAttr.ProjectID)

		// Find matching bead by mr_url.
		ctx := r.Context()
		beads, err := daemon.ListTaskBeads(ctx)
		if err != nil {
			logger.Error("webhook: failed to list beads", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		matched := false
		for _, bead := range beads {
			if bead.Fields["mr_url"] == event.ObjectAttr.URL {
				if bead.Fields["mr_merged"] == "true" {
					continue // already processed
				}
				logger.Info("webhook: updating bead for merged MR",
					"bead", bead.ID, "mr_url", event.ObjectAttr.URL)

				fields := map[string]string{
					"mr_merged":         "true",
					"mr_state":          "merged",
					"gitlab_mr_iid":     strconv.Itoa(event.ObjectAttr.IID),
					"gitlab_project_id": strconv.Itoa(event.ObjectAttr.ProjectID),
				}
				if err := daemon.UpdateBeadFields(ctx, bead.ID, fields); err != nil {
					logger.Error("webhook: failed to update bead",
						"bead", bead.ID, "error", err)
				}
				matched = true
			}
		}

		w.WriteHeader(http.StatusOK)
		if matched {
			fmt.Fprintf(w, `{"status":"processed"}`)
		} else {
			fmt.Fprintf(w, `{"status":"no_match","mr_url":"%s"}`, event.ObjectAttr.URL)
		}
	})
}

// handlePipelineWebhook processes a GitLab pipeline webhook event. It matches
// the pipeline's MR URL to a bead and updates the pipeline status fields.
func handlePipelineWebhook(ctx context.Context, pipelineID int, status, pipelineURL string, mr *struct {
	IID int    `json:"iid"`
	URL string `json:"url"`
}, daemon GitLabBeadClient, logger *slog.Logger) {
	if mr == nil || mr.URL == "" {
		logger.Debug("webhook: pipeline event has no merge_request, skipping")
		return
	}

	logger.Info("webhook: pipeline status update",
		"pipeline_id", pipelineID, "status", status, "mr_url", mr.URL)

	beads, err := daemon.ListTaskBeads(ctx)
	if err != nil {
		logger.Error("webhook: failed to list beads for pipeline event", "error", err)
		return
	}

	for _, bead := range beads {
		if bead.Fields["mr_url"] != mr.URL {
			continue
		}

		fields := map[string]string{
			"mr_pipeline_status": status,
		}
		if pipelineURL != "" {
			fields["mr_pipeline_url"] = pipelineURL
		}

		if err := daemon.UpdateBeadFields(ctx, bead.ID, fields); err != nil {
			logger.Error("webhook: failed to update pipeline status on bead",
				"bead", bead.ID, "error", err)
		} else {
			logger.Info("webhook: updated pipeline status on bead",
				"bead", bead.ID, "status", status, "pipeline_id", pipelineID)
		}
	}
}

// handleApprovalWebhook processes MR approved/unapproved webhook events.
func handleApprovalWebhook(ctx context.Context, mrURL, action, username string, daemon GitLabBeadClient, logger *slog.Logger) {
	if mrURL == "" {
		return
	}

	logger.Info("webhook: MR approval event",
		"mr_url", mrURL, "action", action, "user", username)

	beads, err := daemon.ListTaskBeads(ctx)
	if err != nil {
		logger.Error("webhook: failed to list beads for approval event", "error", err)
		return
	}

	for _, bead := range beads {
		if bead.Fields["mr_url"] != mrURL {
			continue
		}

		fields := map[string]string{}

		switch action {
		case "approved":
			fields["mr_approved"] = "true"
			// Append approver to comma-separated list.
			existing := bead.Fields["mr_approvers"]
			if existing == "" {
				fields["mr_approvers"] = username
			} else if !containsApprover(existing, username) {
				fields["mr_approvers"] = existing + "," + username
			}
		case "unapproved":
			fields["mr_approved"] = "false"
			// Remove approver from list.
			fields["mr_approvers"] = removeApprover(bead.Fields["mr_approvers"], username)
		}

		if err := daemon.UpdateBeadFields(ctx, bead.ID, fields); err != nil {
			logger.Error("webhook: failed to update approval status on bead",
				"bead", bead.ID, "error", err)
		} else {
			logger.Info("webhook: updated approval status on bead",
				"bead", bead.ID, "action", action, "user", username)
		}
	}
}

// notePosition mirrors the GitLab note position object for inline review comments.
type notePosition struct {
	NewPath string `json:"new_path"`
	NewLine int    `json:"new_line"`
	OldPath string `json:"old_path"`
	OldLine int    `json:"old_line"`
}

// handleNoteWebhook processes a GitLab note webhook event. It matches
// MR review comments to beads and creates bead comments with the review feedback.
func handleNoteWebhook(ctx context.Context, noteableType, note string, system bool, author string, position *notePosition, mr *struct {
	IID int    `json:"iid"`
	URL string `json:"url"`
}, daemon GitLabBeadClient, logger *slog.Logger) {
	// Only process MR discussion notes.
	if noteableType != "MergeRequest" {
		logger.Debug("webhook: note event is not for MergeRequest, skipping",
			"noteable_type", noteableType)
		return
	}

	// Skip system-generated notes (merge status changes, etc.).
	if system {
		logger.Debug("webhook: skipping system note")
		return
	}

	if mr == nil || mr.URL == "" {
		logger.Debug("webhook: note event has no merge_request, skipping")
		return
	}

	logger.Info("webhook: MR review comment",
		"mr_url", mr.URL, "author", author)

	beads, err := daemon.ListTaskBeads(ctx)
	if err != nil {
		logger.Error("webhook: failed to list beads for note event", "error", err)
		return
	}

	// Format the comment text for the bead.
	var commentText strings.Builder
	commentText.WriteString(fmt.Sprintf("**GitLab review comment** by @%s:\n\n", author))
	if position != nil && position.NewPath != "" {
		commentText.WriteString(fmt.Sprintf("`%s:%d`\n\n", position.NewPath, position.NewLine))
	}
	commentText.WriteString(note)

	for _, bead := range beads {
		if bead.Fields["mr_url"] != mr.URL {
			continue
		}

		// Add comment to the bead.
		if err := daemon.AddComment(ctx, bead.ID, "gitlab-bridge", commentText.String()); err != nil {
			logger.Error("webhook: failed to add review comment to bead",
				"bead", bead.ID, "error", err)
			continue
		}

		// Set mr_has_review_comments flag.
		fields := map[string]string{
			"mr_has_review_comments": "true",
		}
		if err := daemon.UpdateBeadFields(ctx, bead.ID, fields); err != nil {
			logger.Error("webhook: failed to set review comment flag on bead",
				"bead", bead.ID, "error", err)
		} else {
			logger.Info("webhook: added review comment to bead",
				"bead", bead.ID, "author", author, "mr_url", mr.URL)
		}
	}
}

// containsApprover checks if username is in the comma-separated approvers list.
func containsApprover(approvers, username string) bool {
	for _, a := range strings.Split(approvers, ",") {
		if strings.TrimSpace(a) == username {
			return true
		}
	}
	return false
}

// removeApprover removes username from the comma-separated approvers list.
func removeApprover(approvers, username string) string {
	var result []string
	for _, a := range strings.Split(approvers, ",") {
		a = strings.TrimSpace(a)
		if a != "" && a != username {
			result = append(result, a)
		}
	}
	return strings.Join(result, ",")
}
