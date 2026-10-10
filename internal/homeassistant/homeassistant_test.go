package homeassistant

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHistoryReadsTheEntityOverTheDrive(t *testing.T) {
	t.Parallel()

	var path, query, auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query, auth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `[[
			{"entity_id":"binary_sensor.in_tesla","state":"off","last_changed":"2026-10-08T08:00:00+00:00"},
			{"state":"on","last_changed":"2026-10-08T08:01:30.25+00:00"}
		]]`)
	}))
	defer server.Close()

	from := time.Date(2026, time.October, 8, 8, 0, 0, 0, time.UTC)
	changes, err := New(server.URL, "token").History(context.Background(), "binary_sensor.in_tesla", from, from.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	want := []StateChange{
		{State: "off", Since: from},
		{State: "on", Since: from.Add(90*time.Second + 250*time.Millisecond)},
	}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
	for i := range want {
		if changes[i].State != want[i].State || !changes[i].Since.Equal(want[i].Since) {
			t.Errorf("change %d = %+v, want %+v", i, changes[i], want[i])
		}
	}
	if path != "/api/history/period/2026-10-08T08:00:00Z" {
		t.Errorf("path = %q, want the drive start", path)
	}
	if !strings.Contains(query, "end_time=2026-10-08T08%3A10%3A00Z") || !strings.Contains(query, "filter_entity_id=binary_sensor.in_tesla") {
		t.Errorf("query = %q, want the entity and the drive end", query)
	}
	if auth != "Bearer token" {
		t.Errorf("authorization = %q, want the bearer token", auth)
	}
}

func TestHistoryReportsRejectedToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := New(server.URL, "wrong").History(context.Background(), "binary_sensor.in_tesla", time.Now(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want the 401", err)
	}
}
