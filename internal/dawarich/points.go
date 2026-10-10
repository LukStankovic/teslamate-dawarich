package dawarich

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type TrackedPoint struct {
	Latitude  float64
	Longitude float64
	Timestamp time.Time
	TrackerID string
}

const pointsPerPage = 5000

func (c *Client) Points(ctx context.Context, from, to time.Time) ([]TrackedPoint, error) {
	endpoint, err := url.JoinPath(c.baseURL, "api", "v1", "points")
	if err != nil {
		return nil, fmt.Errorf("build endpoint URL: %w", err)
	}

	var points []TrackedPoint
	for page := 1; ; page++ {
		batch, totalPages, err := c.pointsPage(ctx, endpoint, from, to, page)
		if err != nil {
			return nil, err
		}
		points = append(points, batch...)
		if page >= totalPages {
			return points, nil
		}
	}
}

func (c *Client) pointsPage(ctx context.Context, endpoint string, from, to time.Time, page int) ([]TrackedPoint, int, error) {
	query := url.Values{
		"start_at": {strconv.FormatInt(from.Unix(), 10)},
		"end_at":   {strconv.FormatInt(unixCeil(to), 10)},
		"slim":     {"true"},
		"order":    {"asc"},
		"per_page": {strconv.Itoa(pointsPerPage)},
		"page":     {strconv.Itoa(page)},
	}

	request, err := c.newRequest(ctx, http.MethodGet, endpoint+"?"+query.Encode(), http.NoBody)
	if err != nil {
		return nil, 0, err
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("read points: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("dawarich returned %s reading points: %s", response.Status, errorSnippet(response.Body))
	}

	var rows []slimPoint
	if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
		return nil, 0, fmt.Errorf("decode points: %w", err)
	}

	points := make([]TrackedPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, TrackedPoint{
			Latitude:  row.Latitude,
			Longitude: row.Longitude,
			Timestamp: time.Unix(row.Timestamp, 0).UTC(),
			TrackerID: row.TrackerID,
		})
	}

	totalPages, err := strconv.Atoi(response.Header.Get("X-Total-Pages"))
	if err != nil {
		totalPages = page
		if len(points) == pointsPerPage {
			totalPages++
		}
	}
	return points, totalPages, nil
}

func unixCeil(at time.Time) int64 {
	if at.Nanosecond() > 0 {
		return at.Unix() + 1
	}
	return at.Unix()
}

type slimPoint struct {
	Latitude  float64 `json:"latitude,string"`
	Longitude float64 `json:"longitude,string"`
	Timestamp int64   `json:"timestamp"`
	TrackerID string  `json:"tracker_id"`
}
