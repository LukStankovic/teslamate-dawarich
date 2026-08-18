// Package dawarich submits location points to a Dawarich instance.
package dawarich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Point struct {
	Latitude       float64
	Longitude      float64
	Timestamp      time.Time
	AltitudeMeters *int32
	SpeedMS        *float64
	BatteryPercent *int32
	Charging       *bool
	CourseDegrees  *float64
	DeviceID       string
}

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	logger  *slog.Logger

	maxAttempts int
	baseBackoff time.Duration
}

type Option func(*Client)

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

func WithRetry(maxAttempts int, baseBackoff time.Duration) Option {
	return func(c *Client) {
		c.maxAttempts = maxAttempts
		c.baseBackoff = baseBackoff
	}
}

const (
	defaultTimeout     = 30 * time.Second
	defaultMaxAttempts = 5
	defaultBaseBackoff = time.Second
	maxBackoff         = 30 * time.Second
	errorBodyLimit     = 512
)

func New(baseURL, apiKey string, logger *slog.Logger, opts ...Option) *Client {
	client := &Client{
		baseURL:     baseURL,
		apiKey:      apiKey,
		http:        &http.Client{Timeout: defaultTimeout},
		logger:      logger,
		maxAttempts: defaultMaxAttempts,
		baseBackoff: defaultBaseBackoff,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

func (c *Client) SendPoints(ctx context.Context, points []Point) error {
	if len(points) == 0 {
		return nil
	}

	body, err := json.Marshal(batch{Locations: features(points)})
	if err != nil {
		return fmt.Errorf("encode batch: %w", err)
	}

	endpoint, err := url.JoinPath(c.baseURL, "api", "v1", "overland", "batches")
	if err != nil {
		return fmt.Errorf("build endpoint URL: %w", err)
	}

	backoff := c.baseBackoff
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		retryAfter, err := c.post(ctx, endpoint, body)
		if err == nil {
			return nil
		}
		lastErr = err

		var permanent permanentError
		if errors.As(err, &permanent) || attempt == c.maxAttempts {
			return err
		}

		wait := backoff
		if retryAfter > 0 {
			wait = retryAfter
		}
		c.logger.Warn("dawarich request failed, retrying",
			"attempt", attempt, "of", c.maxAttempts, "retry_in", wait, "error", err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	return lastErr
}

func (c *Client) post(ctx context.Context, endpoint string, body []byte) (retryAfter time.Duration, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, permanentError{fmt.Errorf("build request: %w", err)}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.apiKey)

	response, err := c.http.Do(request)
	if err != nil {
		return 0, fmt.Errorf("post points: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return 0, nil
	case response.StatusCode == http.StatusTooManyRequests, response.StatusCode >= 500:
		return parseRetryAfter(response.Header.Get("Retry-After")),
			fmt.Errorf("dawarich returned %s: %s", response.Status, errorSnippet(response.Body))
	default:
		return 0, permanentError{fmt.Errorf("dawarich returned %s: %s", response.Status, errorSnippet(response.Body))}
	}
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(header); err == nil {
		if until := time.Until(when); until > 0 {
			return until
		}
	}
	return 0
}

func errorSnippet(body io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(body, errorBodyLimit))
	if err != nil || len(data) == 0 {
		return "<no body>"
	}
	return string(data)
}
