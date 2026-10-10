package dawarich

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSendPointsPayload(t *testing.T) {
	t.Parallel()

	var body []byte
	var path, auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	speed := 25.0
	client := New(server.URL, "secret", discardLogger())
	err := client.SendPoints(context.Background(), []Point{{
		Latitude:       50.0755,
		Longitude:      14.4378,
		Timestamp:      time.Date(2026, time.August, 18, 17, 42, 3, 0, time.UTC),
		AltitudeMeters: ptr(int32(214)),
		SpeedMS:        &speed,
		BatteryPercent: ptr(int32(72)),
		Charging:       ptr(true),
		DeviceID:       "tesla-Model Y",
	}})
	if err != nil {
		t.Fatalf("SendPoints: %v", err)
	}

	if want := "/api/v1/overland/batches"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if want := "Bearer secret"; auth != want {
		t.Errorf("authorization = %q, want %q", auth, want)
	}

	var got struct {
		Locations []struct {
			Type     string `json:"type"`
			Geometry struct {
				Type        string     `json:"type"`
				Coordinates [2]float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties struct {
				Timestamp    string   `json:"timestamp"`
				DeviceID     string   `json:"device_id"`
				Altitude     *int32   `json:"altitude"`
				Speed        *float64 `json:"speed"`
				BatteryLevel *float64 `json:"battery_level"`
				BatteryState string   `json:"battery_state"`
			} `json:"properties"`
		} `json:"locations"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal request body: %v\n%s", err, body)
	}
	if len(got.Locations) != 1 {
		t.Fatalf("locations = %d, want 1", len(got.Locations))
	}

	location := got.Locations[0]
	if location.Type != "Feature" || location.Geometry.Type != "Point" {
		t.Errorf("geojson types = %q/%q, want Feature/Point", location.Type, location.Geometry.Type)
	}
	if location.Geometry.Coordinates != [2]float64{14.4378, 50.0755} {
		t.Errorf("coordinates = %v, want [longitude latitude]", location.Geometry.Coordinates)
	}
	if want := "2026-08-18T17:42:03Z"; location.Properties.Timestamp != want {
		t.Errorf("timestamp = %q, want %q", location.Properties.Timestamp, want)
	}
	if location.Properties.BatteryLevel == nil || *location.Properties.BatteryLevel != 0.72 {
		t.Errorf("battery_level = %v, want 0.72", location.Properties.BatteryLevel)
	}
	if location.Properties.BatteryState != "charging" {
		t.Errorf("battery_state = %q, want charging", location.Properties.BatteryState)
	}
}

func TestSendPointsOmitsUnknownFields(t *testing.T) {
	t.Parallel()

	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := New(server.URL, "secret", discardLogger())
	if err := client.SendPoints(context.Background(), []Point{{Latitude: 1, Longitude: 2, Timestamp: time.Unix(0, 0)}}); err != nil {
		t.Fatalf("SendPoints: %v", err)
	}

	var got struct {
		Locations []struct {
			Properties map[string]any `json:"properties"`
		} `json:"locations"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	for _, field := range []string{"speed", "altitude", "battery_level", "battery_state", "course"} {
		if _, present := got.Locations[0].Properties[field]; present {
			t.Errorf("property %q present, want omitted when unknown", field)
		}
	}
}

func TestSendPointsRetriesServerErrors(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := New(server.URL, "secret", discardLogger(), WithRetry(5, time.Millisecond))
	if err := client.SendPoints(context.Background(), []Point{{Latitude: 1, Longitude: 2, Timestamp: time.Unix(0, 0)}}); err != nil {
		t.Fatalf("SendPoints: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestSendPointsDoesNotRetryRejectedKey(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := New(server.URL, "wrong", discardLogger(), WithRetry(5, time.Millisecond))
	if err := client.SendPoints(context.Background(), []Point{{Latitude: 1, Longitude: 2, Timestamp: time.Unix(0, 0)}}); err == nil {
		t.Fatal("SendPoints succeeded, want an error")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1: a rejected key cannot be fixed by retrying", got)
	}
}

func TestSendPointsEmptyBatchSkipsRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request sent for an empty batch")
	}))
	defer server.Close()

	if err := New(server.URL, "secret", discardLogger()).SendPoints(context.Background(), nil); err != nil {
		t.Fatalf("SendPoints: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestSendPointsSetsForwardedProto(t *testing.T) {
	t.Parallel()

	var forwarded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = r.Header.Get("X-Forwarded-Proto")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := New(server.URL, "secret", discardLogger(), WithForwardedProto("https"))
	if err := client.SendPoints(context.Background(), []Point{{Latitude: 1, Longitude: 2, Timestamp: time.Unix(0, 0)}}); err != nil {
		t.Fatalf("SendPoints: %v", err)
	}
	if forwarded != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https", forwarded)
	}
}

func TestSendPointsDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Redirect(w, r, "https://elsewhere.example.com"+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer server.Close()

	client := New(server.URL, "secret", discardLogger(), WithRetry(5, time.Millisecond))
	err := client.SendPoints(context.Background(), []Point{{Latitude: 1, Longitude: 2, Timestamp: time.Unix(0, 0)}})
	if err == nil {
		t.Fatal("SendPoints succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "DAWARICH_FORWARDED_PROTO") {
		t.Errorf("error = %v, want it to name the fix", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

func TestPointsReadsEveryPageOfTheWindow(t *testing.T) {
	t.Parallel()

	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Path != "/api/v1/points" || r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("X-Total-Pages", "2")
		if r.URL.Query().Get("page") == "1" {
			_, _ = io.WriteString(w, `[{"latitude":"50.0755","longitude":"14.4378","timestamp":1791446400,"tracker_id":"iphone"}]`)
			return
		}
		_, _ = io.WriteString(w, `[{"latitude":"49.19","longitude":"16.6","timestamp":1791446460,"tracker_id":null}]`)
	}))
	defer server.Close()

	from := time.Unix(1791446400, 0)
	points, err := New(server.URL, "secret", discardLogger()).Points(context.Background(), from, from.Add(time.Minute))
	if err != nil {
		t.Fatalf("Points: %v", err)
	}

	want := []TrackedPoint{
		{Latitude: 50.0755, Longitude: 14.4378, Timestamp: time.Unix(1791446400, 0).UTC(), TrackerID: "iphone"},
		{Latitude: 49.19, Longitude: 16.6, Timestamp: time.Unix(1791446460, 0).UTC()},
	}
	if len(points) != len(want) {
		t.Fatalf("points = %+v, want %+v", points, want)
	}
	for i := range want {
		if points[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, points[i], want[i])
		}
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "start_at=1791446400") ||
		!strings.Contains(queries[0], "end_at=1791446460") || !strings.Contains(queries[0], "slim=true") {
		t.Errorf("queries = %q, want two slim pages bounded by the window", queries)
	}
}

func TestPointsKeepsPagingWithoutTotalPagesHeader(t *testing.T) {
	t.Parallel()

	fullPage := "[" + strings.TrimSuffix(strings.Repeat(`{"latitude":"50","longitude":"14","timestamp":1791446400,"tracker_id":"iphone"},`, pointsPerPage), ",") + "]"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("page") == "1" {
			_, _ = io.WriteString(w, fullPage)
			return
		}
		_, _ = io.WriteString(w, `[{"latitude":"50","longitude":"14","timestamp":1791446460,"tracker_id":"iphone"}]`)
	}))
	defer server.Close()

	points, err := New(server.URL, "secret", discardLogger()).Points(context.Background(), time.Now(), time.Now())
	if err != nil {
		t.Fatalf("Points: %v", err)
	}
	if len(points) != pointsPerPage+1 || requests.Load() != 2 {
		t.Errorf("points = %d over %d requests, want %d over 2", len(points), requests.Load(), pointsPerPage+1)
	}
}

func TestPointsReportsRejectedRead(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := New(server.URL, "wrong", discardLogger()).Points(context.Background(), time.Now(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want the 401", err)
	}
}
