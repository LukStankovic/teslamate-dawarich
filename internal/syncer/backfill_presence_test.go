package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/dawarich"
	"github.com/LukStankovic/teslamate-dawarich/internal/homeassistant"
	"github.com/LukStankovic/teslamate-dawarich/internal/presence"
	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

const (
	phoneTracker = "iphone"
	carTracker   = "car-model-y"
	inCarEntity  = "binary_sensor.in_tesla"
)

type recordedDrives struct {
	drives map[int64]teslamate.Drive
	routes map[int64][]teslamate.RoutePoint
}

func (r *recordedDrives) Drive(_ context.Context, id int64) (teslamate.Drive, error) {
	drive, ok := r.drives[id]
	if !ok {
		return teslamate.Drive{}, fmt.Errorf("no drive %d", id)
	}
	return drive, nil
}

func (r *recordedDrives) DriveRoute(_ context.Context, id int64) ([]teslamate.RoutePoint, error) {
	return r.routes[id], nil
}

type trackedPoint struct {
	Latitude  string `json:"latitude"`
	Longitude string `json:"longitude"`
	Timestamp int64  `json:"timestamp"`
	TrackerID string `json:"tracker_id"`
}

func trackedAt(trackerID string, latitude, longitude float64, at time.Time) trackedPoint {
	return trackedPoint{
		Latitude:  strconv.FormatFloat(latitude, 'f', -1, 64),
		Longitude: strconv.FormatFloat(longitude, 'f', -1, 64),
		Timestamp: at.Unix(),
		TrackerID: trackerID,
	}
}

func dawarichServing(t *testing.T, points []trackedPoint) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/points" {
			t.Errorf("dawarich asked for %s", r.URL.Path)
		}
		from, _ := strconv.ParseInt(r.URL.Query().Get("start_at"), 10, 64)
		to, _ := strconv.ParseInt(r.URL.Query().Get("end_at"), 10, 64)
		inRange := []trackedPoint{}
		for _, point := range points {
			if point.Timestamp >= from && point.Timestamp <= to {
				inRange = append(inRange, point)
			}
		}
		w.Header().Set("X-Total-Pages", "1")
		_ = json.NewEncoder(w).Encode(inRange)
	}))
	t.Cleanup(server.Close)
	return server
}

func homeAssistantRetaining(t *testing.T, retainedSince time.Time, onFrom, onUntil time.Time) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, err := time.Parse(time.RFC3339, strings.TrimPrefix(r.URL.Path, "/api/history/period/"))
		if err != nil {
			t.Errorf("history start: %v", err)
		}
		if from.Before(retainedSince) {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = fmt.Fprintf(w, `[[{"state":"off","last_changed":%q},{"state":"on","last_changed":%q},{"state":"off","last_changed":%q}]]`,
			from.Format(time.RFC3339), onFrom.Format(time.RFC3339), onUntil.Format(time.RFC3339))
	}))
	t.Cleanup(server.Close)
	return server
}

type backfillHistory struct {
	drives    *recordedDrives
	positions []teslamate.Position
	phone     []trackedPoint
	driveOf   map[string]int64
}

func (h *backfillHistory) addDrive(name string, start time.Time) teslamate.Drive {
	id := int64(len(h.driveOf) + 1)
	h.driveOf[name] = id
	end := start.Add(10 * time.Minute)
	drive := teslamate.Drive{ID: id, StartedAt: start, EndedAt: &end}
	h.drives.drives[id] = drive

	for minute := range 11 {
		at := start.Add(time.Duration(minute) * time.Minute)
		latitude, longitude := 50.0, 14.0+float64(minute)*0.01
		h.drives.routes[id] = append(h.drives.routes[id], teslamate.RoutePoint{At: at, Latitude: latitude, Longitude: longitude})
		h.positions = append(h.positions, teslamate.Position{
			ID: int64(len(h.positions) + 1), CarName: "Model Y", Date: at,
			Latitude: latitude, Longitude: longitude, DriveID: &id,
		})
	}
	h.positions = append(h.positions, teslamate.Position{
		ID: int64(len(h.positions) + 1), CarName: "Model Y", Date: end.Add(time.Hour), Latitude: 50, Longitude: 14.1,
	})
	return drive
}

func (h *backfillHistory) phoneAlong(drive teslamate.Drive) {
	for _, car := range h.drives.routes[drive.ID] {
		h.phone = append(h.phone, trackedAt(phoneTracker, car.Latitude+0.0005, car.Longitude, car.At))
	}
}

func (h *backfillHistory) phoneAtHome(drive teslamate.Drive) {
	for _, car := range h.drives.routes[drive.ID] {
		h.phone = append(h.phone, trackedAt(phoneTracker, 49.2, 16.6, car.At))
	}
}

func (h *backfillHistory) sentPerDrive(sink *fakeSink) map[string]int {
	sent := map[string]int{}
	for _, batch := range sink.batches {
		for _, point := range batch {
			name := "parked"
			for candidate, id := range h.driveOf {
				drive := h.drives.drives[id]
				if !point.Timestamp.Before(drive.StartedAt) && !point.Timestamp.After(*drive.EndedAt) {
					name = candidate
				}
			}
			sent[name]++
		}
	}
	return sent
}

func TestBackfillScreensDrivesOlderThanHomeAssistantHistoryByThePhone(t *testing.T) {
	t.Parallel()

	for _, syncUnknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("sync_unknown=%v", syncUnknown), func(t *testing.T) {
			t.Parallel()
			backfillHistoricalDrives(t, syncUnknown)
		})
	}
}

func backfillHistoricalDrives(t *testing.T, syncUnknown bool) {
	longAgo := time.Date(2024, time.March, 4, 8, 0, 0, 0, time.UTC)
	history := &backfillHistory{
		drives:  &recordedDrives{drives: map[int64]teslamate.Drive{}, routes: map[int64][]teslamate.RoutePoint{}},
		driveOf: map[string]int64{},
	}

	riddenAlong := history.addDrive("phone rode along", longAgo)
	history.phoneAlong(riddenAlong)
	leftHome := history.addDrive("phone stayed home", longAgo.Add(24*time.Hour))
	history.phoneAtHome(leftHome)
	silent := history.addDrive("phone silent", longAgo.Add(48*time.Hour))
	history.phone = append(history.phone,
		trackedAt(phoneTracker, 49.2, 16.6, silent.StartedAt.Add(5*time.Hour)),
		trackedAt(carTracker, 50.0, 14.05, silent.StartedAt.Add(5*time.Minute)))
	recent := history.addDrive("home assistant saw it", time.Now().Add(-3*24*time.Hour).Truncate(time.Second))

	homeAssistant := homeAssistantRetaining(t, time.Now().Add(-10*24*time.Hour), recent.StartedAt, *recent.EndedAt)
	dawarichAPI := dawarichServing(t, history.phone)

	checker := presence.NewChecker(history.drives, []presence.Signal{
		presence.NewEntitySignal(homeassistant.New(homeAssistant.URL, "token"), inCarEntity),
		presence.NewPhoneSignal(history.drives, dawarich.New(dawarichAPI.URL, "key", discardLogger()), phoneTracker, 500, 6*time.Hour),
	}, presence.Options{SettleDelay: 15 * time.Minute, SyncWhenUnknown: syncUnknown}, discardLogger())

	sink := &fakeSink{}
	cursor := cursorAt(time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC))
	_, err := newTestSyncer(&fakeSource{positions: history.positions}, sink, cursor,
		Options{BatchSize: 5, OverlapWindow: time.Minute, Presence: checker}).SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	want := map[string]int{"phone rode along": 11, "home assistant saw it": 11, "parked": 4}
	if syncUnknown {
		want["phone silent"] = 11
	}
	sent := history.sentPerDrive(sink)
	for _, name := range []string{"phone rode along", "phone stayed home", "phone silent", "home assistant saw it", "parked"} {
		if sent[name] != want[name] {
			t.Errorf("%s: sent %d points, want %d", name, sent[name], want[name])
		}
	}

	if at, _ := cursor.state(); !at.Equal(history.positions[len(history.positions)-1].Date) {
		t.Errorf("cursor = %v, want past the last position at %v", at, history.positions[len(history.positions)-1].Date)
	}
}
