// Package homeassistant reads entity history from a Home Assistant instance.
package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type StateChange struct {
	State string
	Since time.Time
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

const (
	defaultTimeout = 30 * time.Second
	errorBodyLimit = 512
)

func New(baseURL, token string) *Client {
	return &Client{baseURL: baseURL, token: token, http: &http.Client{Timeout: defaultTimeout}}
}

func (c *Client) History(ctx context.Context, entityID string, from, to time.Time) ([]StateChange, error) {
	endpoint, err := url.JoinPath(c.baseURL, "api", "history", "period", from.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("build endpoint URL: %w", err)
	}
	query := url.Values{
		"filter_entity_id": {entityID},
		"end_time":         {to.UTC().Format(time.RFC3339)},
		"minimal_response": {""},
		"no_attributes":    {""},
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, errorBodyLimit))
		return nil, fmt.Errorf("home assistant returned %s: %s", response.Status, snippet)
	}

	var entities [][]historyRow
	if err := json.NewDecoder(response.Body).Decode(&entities); err != nil {
		return nil, fmt.Errorf("decode history: %w", err)
	}
	if len(entities) == 0 {
		return nil, nil
	}

	changes := make([]StateChange, 0, len(entities[0]))
	for _, row := range entities[0] {
		changes = append(changes, StateChange{State: row.State, Since: row.LastChanged})
	}
	return changes, nil
}

type historyRow struct {
	State       string    `json:"state"`
	LastChanged time.Time `json:"last_changed"`
}
