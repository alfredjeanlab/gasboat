package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SlackNotifier implements Notifier by posting decision beads to Slack
// and handling interactive responses (button clicks and thread replies).
type SlackNotifier struct {
	botToken      string
	signingSecret string
	channel       string
	daemon        BeadClient
	logger        *slog.Logger

	mu       sync.Mutex
	messages map[string]string // bead ID → Slack message ts
}

// NewSlackNotifier creates a new Slack notifier.
func NewSlackNotifier(botToken, signingSecret, channel string, daemon BeadClient, logger *slog.Logger) *SlackNotifier {
	return &SlackNotifier{
		botToken:      botToken,
		signingSecret: signingSecret,
		channel:       channel,
		daemon:        daemon,
		logger:        logger,
		messages:      make(map[string]string),
	}
}

// NotifyDecision posts a Block Kit message to Slack for a new decision bead.
func (s *SlackNotifier) NotifyDecision(ctx context.Context, bead BeadEvent) error {
	question := bead.Fields["question"]
	optionsRaw := bead.Fields["options"]
	agent := bead.Assignee

	// Parse options JSON array.
	var options []string
	if err := json.Unmarshal([]byte(optionsRaw), &options); err != nil {
		// Try as a single string fallback.
		options = []string{optionsRaw}
	}

	// Build Block Kit blocks.
	blocks := []map[string]interface{}{
		{
			"type": "header",
			"text": map[string]string{
				"type": "plain_text",
				"text": "Decision Needed",
			},
		},
		{
			"type": "section",
			"text": map[string]string{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*%s*", question),
			},
		},
	}

	// Add context block with agent info.
	if agent != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "context",
			"elements": []map[string]string{
				{
					"type": "mrkdwn",
					"text": fmt.Sprintf("Agent: `%s` | Bead: `%s`", agent, bead.ID),
				},
			},
		})
	}

	// Add action buttons for each option.
	if len(options) > 0 {
		var buttons []map[string]interface{}
		for i, opt := range options {
			style := "primary"
			if i > 0 {
				style = ""
			}
			btn := map[string]interface{}{
				"type": "button",
				"text": map[string]string{
					"type": "plain_text",
					"text": opt,
				},
				"value":     opt,
				"action_id": fmt.Sprintf("decision_%s_%d", bead.ID, i),
			}
			if style != "" {
				btn["style"] = style
			}
			buttons = append(buttons, btn)
		}
		blocks = append(blocks, map[string]interface{}{
			"type":     "actions",
			"block_id": "decision_" + bead.ID,
			"elements": buttons,
		})
	}

	payload := map[string]interface{}{
		"channel": s.channel,
		"text":    fmt.Sprintf("Decision needed: %s", question),
		"blocks":  blocks,
	}

	ts, err := s.postSlackMessage(ctx, payload)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.messages[bead.ID] = ts
	s.mu.Unlock()

	s.logger.Info("posted decision to Slack",
		"bead", bead.ID, "channel", s.channel, "ts", ts)
	return nil
}

// UpdateDecision edits the Slack message to show the resolved state.
func (s *SlackNotifier) UpdateDecision(ctx context.Context, beadID, chosen string) error {
	s.mu.Lock()
	ts, ok := s.messages[beadID]
	s.mu.Unlock()

	if !ok {
		s.logger.Debug("no Slack message found for resolved decision", "bead", beadID)
		return nil
	}

	blocks := []map[string]interface{}{
		{
			"type": "section",
			"text": map[string]string{
				"type": "mrkdwn",
				"text": fmt.Sprintf("~Decision needed~ — *Resolved*: %s", chosen),
			},
		},
	}

	payload := map[string]interface{}{
		"channel": s.channel,
		"ts":      ts,
		"text":    fmt.Sprintf("Decision resolved: %s", chosen),
		"blocks":  blocks,
	}

	if err := s.updateSlackMessage(ctx, payload); err != nil {
		return err
	}

	s.mu.Lock()
	delete(s.messages, beadID)
	s.mu.Unlock()

	return nil
}

// NotifyEscalation posts a highlighted notification for an escalated decision.
func (s *SlackNotifier) NotifyEscalation(ctx context.Context, bead BeadEvent) error {
	question := bead.Fields["question"]
	text := fmt.Sprintf(":rotating_light: ESCALATED: %s — %s", bead.ID, question)

	blocks := []map[string]interface{}{
		{
			"type": "section",
			"text": map[string]string{
				"type": "mrkdwn",
				"text": fmt.Sprintf(":rotating_light: *ESCALATED: %s*\n%s", bead.ID, question),
			},
		},
	}

	payload := map[string]interface{}{
		"channel": s.channel,
		"text":    text,
		"blocks":  blocks,
	}

	_, err := s.postSlackMessage(ctx, payload)
	return err
}

// DismissDecision updates the Slack message for an expired/dismissed decision.
func (s *SlackNotifier) DismissDecision(_ context.Context, beadID string) error {
	s.mu.Lock()
	ts, ok := s.messages[beadID]
	delete(s.messages, beadID)
	s.mu.Unlock()

	if !ok {
		return nil
	}

	payload := map[string]interface{}{
		"channel": s.channel,
		"ts":      ts,
		"text":    "Decision expired",
		"blocks": []map[string]interface{}{
			{
				"type": "section",
				"text": map[string]string{
					"type": "mrkdwn",
					"text": fmt.Sprintf("~Decision needed~ — *Expired* (`%s`)", beadID),
				},
			},
		},
	}
	return s.updateSlackMessage(context.Background(), payload)
}

// Handler returns an http.Handler for Slack interaction webhooks.
func (s *SlackNotifier) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/slack/interactions", s.HandleInteraction)
	return mux
}

// HandleInteraction processes Slack interactive payloads (button clicks and dialog submissions).
func (s *SlackNotifier) HandleInteraction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read and verify the request body.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if s.signingSecret != "" {
		if !s.verifySlackSignature(r, body) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Parse form values from the raw body (body was already consumed by ReadAll).
	formValues, err := url.ParseQuery(string(body))
	if err != nil {
		s.logger.Debug("failed to parse Slack form body", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	payloadStr := formValues.Get("payload")
	if payloadStr == "" {
		s.logger.Debug("missing payload in Slack interaction")
		http.Error(w, "missing payload", http.StatusBadRequest)
		return
	}

	var interaction slackInteraction
	if err := json.Unmarshal([]byte(payloadStr), &interaction); err != nil {
		s.logger.Debug("failed to parse Slack interaction", "error", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	switch interaction.Type {
	case "block_actions":
		s.handleBlockAction(r.Context(), interaction)
	default:
		s.logger.Debug("unhandled Slack interaction type", "type", interaction.Type)
	}

	w.WriteHeader(http.StatusOK)
}

type slackInteraction struct {
	Type    string `json:"type"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Message struct {
		TS string `json:"ts"`
	} `json:"message"`
	Actions []struct {
		ActionID string `json:"action_id"`
		BlockID  string `json:"block_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

func (s *SlackNotifier) handleBlockAction(ctx context.Context, interaction slackInteraction) {
	for _, action := range interaction.Actions {
		// Extract bead ID from block_id (format: "decision_{beadID}").
		beadID := strings.TrimPrefix(action.BlockID, "decision_")
		if beadID == action.BlockID {
			continue // Not a decision action
		}

		chosen := action.Value
		rationale := fmt.Sprintf("Chosen by @%s via Slack", interaction.User.Username)

		s.logger.Info("Slack decision action",
			"bead", beadID, "chosen", chosen, "user", interaction.User.Username)

		// Close the decision bead with the chosen value.
		fields := map[string]string{
			"chosen":    chosen,
			"rationale": rationale,
		}
		if err := s.daemon.CloseBead(ctx, beadID, fields); err != nil {
			s.logger.Error("failed to close decision bead from Slack",
				"bead", beadID, "error", err)
		}
	}
}

// verifySlackSignature verifies the Slack request signature using HMAC-SHA256.
func (s *SlackNotifier) verifySlackSignature(r *http.Request, body []byte) bool {
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")

	if timestamp == "" || signature == "" {
		return false
	}

	// Reject requests older than 5 minutes to prevent replay attacks.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if abs(time.Now().Unix()-ts) > 300 {
		return false
	}

	baseString := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(s.signingSecret))
	mac.Write([]byte(baseString))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// postSlackMessage sends a chat.postMessage API call and returns the message ts.
func (s *SlackNotifier) postSlackMessage(ctx context.Context, payload map[string]interface{}) (string, error) {
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal Slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://slack.com/api/chat.postMessage", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create Slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.botToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Slack API request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		TS    string `json:"ts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode Slack response: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("Slack API error: %s", result.Error)
	}
	return result.TS, nil
}

// updateSlackMessage sends a chat.update API call to edit an existing message.
func (s *SlackNotifier) updateSlackMessage(ctx context.Context, payload map[string]interface{}) error {
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://slack.com/api/chat.update", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create Slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.botToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Slack API request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode Slack response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("Slack API error: %s", result.Error)
	}
	return nil
}
