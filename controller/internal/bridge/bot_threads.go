package bridge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/slack-go/slack"
)

// SlackThreadAPI serves the Slack thread read/reply API endpoints.
// These endpoints allow agents to read thread messages and post replies
// via the gb CLI, without needing direct Slack API access.
type SlackThreadAPI struct {
	api    *slack.Client
	logger *slog.Logger
}

// NewSlackThreadAPI creates a new Slack thread API handler.
func NewSlackThreadAPI(api *slack.Client, logger *slog.Logger) *SlackThreadAPI {
	return &SlackThreadAPI{api: api, logger: logger}
}

// RegisterRoutes registers thread API routes on the given mux.
func (a *SlackThreadAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/slack/threads", a.handleGetThreadMessages)
	mux.HandleFunc("/api/slack/threads/reply", a.handlePostReply)
}

// ThreadMessage represents a single message in a Slack thread.
type ThreadMessage struct {
	Author    string `json:"author"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	IsBot     bool   `json:"is_bot"`
}

// handleGetThreadMessages handles GET /api/slack/threads?channel={channel}&ts={ts}&limit={n}.
func (a *SlackThreadAPI) handleGetThreadMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	channel := r.URL.Query().Get("channel")
	ts := r.URL.Query().Get("ts")
	if channel == "" || ts == "" {
		http.Error(w, `{"error":"channel and ts query parameters are required"}`, http.StatusBadRequest)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	// Fetch thread replies from Slack.
	msgs, _, _, err := a.api.GetConversationReplies(&slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: ts,
		Limit:     limit,
	})
	if err != nil {
		a.logger.Error("failed to get thread replies", "channel", channel, "ts", ts, "error", err)
		http.Error(w, `{"error":"failed to fetch thread messages"}`, http.StatusInternalServerError)
		return
	}

	// Convert to our response format, filtering out bot messages.
	result := make([]ThreadMessage, 0, len(msgs))
	for _, msg := range msgs {
		isBot := msg.BotID != "" || msg.SubType == "bot_message"
		if isBot {
			continue // omit bot messages to keep context clean for agents
		}
		author := msg.User
		if author == "" {
			author = msg.Username
		}
		result = append(result, ThreadMessage{
			Author:    author,
			Text:      msg.Text,
			Timestamp: msg.Timestamp,
			IsBot:     false,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// replyRequest is the request body for POST /api/slack/threads/reply.
type replyRequest struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
	Text     string `json:"text"`
}

// replyResponse is the response body for POST /api/slack/threads/reply.
type replyResponse struct {
	OK        bool   `json:"ok"`
	Timestamp string `json:"timestamp"`
}

// handlePostReply handles POST /api/slack/threads/reply.
func (a *SlackThreadAPI) handlePostReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req replyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Channel == "" || req.ThreadTS == "" || req.Text == "" {
		http.Error(w, `{"error":"channel, thread_ts, and text are required"}`, http.StatusBadRequest)
		return
	}

	_, ts, err := a.api.PostMessage(req.Channel,
		slack.MsgOptionText(req.Text, false),
		slack.MsgOptionTS(req.ThreadTS),
	)
	if err != nil {
		a.logger.Error("failed to post thread reply",
			"channel", req.Channel, "thread_ts", req.ThreadTS, "error", err)
		http.Error(w, `{"error":"failed to post reply"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(replyResponse{OK: true, Timestamp: ts})
}
