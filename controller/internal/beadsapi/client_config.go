package beadsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ConfigEntry represents a config key/value from the daemon.
type ConfigEntry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// GetConfig fetches a config entry by key.
//
// Deprecated: KV config is superseded by label-based config beads.
// Use ListBeadsFiltered with type="config" instead.
func (c *Client) GetConfig(ctx context.Context, key string) (*ConfigEntry, error) {
	var entry ConfigEntry
	if err := c.doJSON(ctx, http.MethodGet, "/v1/configs/"+url.PathEscape(key), nil, &entry); err != nil {
		return nil, fmt.Errorf("getting config %s: %w", key, err)
	}
	return &entry, nil
}

// ListConfigs returns all config entries in the given namespace.
//
// Deprecated: KV config is superseded by label-based config beads.
// Use ListBeadsFiltered with type="config" instead.
func (c *Client) ListConfigs(ctx context.Context, namespace string) ([]ConfigEntry, error) {
	q := url.Values{}
	if namespace != "" {
		q.Set("namespace", namespace)
	}
	path := "/v1/configs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var resp struct {
		Configs []ConfigEntry `json:"configs"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("listing configs: %w", err)
	}
	return resp.Configs, nil
}

// DeleteConfig removes a config entry by key.
//
// Deprecated: KV config is superseded by label-based config beads.
// Use ListBeadsFiltered with type="config" instead.
func (c *Client) DeleteConfig(ctx context.Context, key string) error {
	if err := c.doJSON(ctx, http.MethodDelete, "/v1/configs/"+url.PathEscape(key), nil, nil); err != nil {
		return fmt.Errorf("deleting config %s: %w", key, err)
	}
	return nil
}
