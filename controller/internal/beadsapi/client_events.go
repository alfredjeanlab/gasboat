package beadsapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SSEEvent represents a parsed Server-Sent Event.
type SSEEvent struct {
	Event string          // event type (e.g., "bead.closed")
	Data  json.RawMessage // raw JSON payload
}

// EventStream opens an SSE connection to the daemon and returns a channel of
// events. The channel is closed when the context is canceled or the stream ends.
// topics is the NATS-style topic filter (e.g., "beads.bead.>", "decisions.>").
func (c *Client) EventStream(ctx context.Context, topics string) (<-chan SSEEvent, error) {
	q := url.Values{}
	if topics != "" {
		q.Set("topics", topics)
	}
	path := "/v1/events/stream"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	// Use the dedicated SSE client (no timeout) for long-lived streams.
	resp, err := c.sseClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to SSE stream: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("SSE stream returned HTTP %d", resp.StatusCode)
	}

	ch := make(chan SSEEvent, 16)
	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		var eventType string
		var dataLines []string

		for scanner.Scan() {
			line := scanner.Text()

			if line == "" {
				// Empty line = end of event.
				if len(dataLines) > 0 {
					data := strings.Join(dataLines, "\n")
					select {
					case ch <- SSEEvent{Event: eventType, Data: json.RawMessage(data)}:
					case <-ctx.Done():
						return
					}
				}
				eventType = ""
				dataLines = nil
				continue
			}

			if strings.HasPrefix(line, "event:") {
				eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data:"))
			}
		}
	}()

	return ch, nil
}

// BaseURL returns the client's base URL (useful for constructing SSE URLs externally).
func (c *Client) BaseURL() string {
	return c.baseURL
}
