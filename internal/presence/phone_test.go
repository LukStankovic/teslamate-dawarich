package presence

import (
	"context"
	"testing"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/dawarich"
	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

type fakePhonePoints struct {
	points []dawarich.TrackedPoint
}

func (f *fakePhonePoints) Points(context.Context, time.Time, time.Time) ([]dawarich.TrackedPoint, error) {
	return f.points, nil
}

func routeHeadingEast() []teslamate.RoutePoint {
	route := make([]teslamate.RoutePoint, 0, 11)
	for minute := range 11 {
		route = append(route, teslamate.RoutePoint{
			At:        driveStart.Add(time.Duration(minute) * time.Minute),
			Latitude:  50.0,
			Longitude: 14.0 + float64(minute)*0.01,
		})
	}
	return route
}

func phoneAlong(route []teslamate.RoutePoint, trackerID string) []dawarich.TrackedPoint {
	points := make([]dawarich.TrackedPoint, 0, len(route))
	for _, car := range route {
		points = append(points, dawarich.TrackedPoint{
			Latitude: car.Latitude + 0.0005, Longitude: car.Longitude, Timestamp: car.At, TrackerID: trackerID,
		})
	}
	return points
}

func phoneAtHome(trackerID string) []dawarich.TrackedPoint {
	return []dawarich.TrackedPoint{
		{Latitude: 49.2, Longitude: 16.6, Timestamp: driveStart.Add(3 * time.Minute), TrackerID: trackerID},
		{Latitude: 49.2, Longitude: 16.6, Timestamp: driveStart.Add(7 * time.Minute), TrackerID: trackerID},
	}
}

func witnessPhone(t *testing.T, points []dawarich.TrackedPoint) (Verdict, bool) {
	t.Helper()
	signal := NewPhoneSignal(&fakeDrives{route: routeHeadingEast()}, &fakePhonePoints{points: points}, "iphone", 500)
	verdict, known, err := signal.Witness(context.Background(), finishedDrive())
	if err != nil {
		t.Fatalf("Witness: %v", err)
	}
	return verdict, known
}

func TestPhoneRidingAlongMakesTheOwnerPresent(t *testing.T) {
	t.Parallel()

	verdict, known := witnessPhone(t, phoneAlong(routeHeadingEast(), "iphone"))
	if !known || verdict != Present {
		t.Errorf("verdict = %v (known %v), want present", verdict, known)
	}
}

func TestPhoneLeftAtHomeMakesTheOwnerAbsent(t *testing.T) {
	t.Parallel()

	verdict, known := witnessPhone(t, phoneAtHome("iphone"))
	if !known || verdict != Absent {
		t.Errorf("verdict = %v (known %v), want absent", verdict, known)
	}
}

func TestOtherTrackersNeverSpeakForThePhone(t *testing.T) {
	t.Parallel()

	route := routeHeadingEast()
	points := append(phoneAlong(route, "tesla-Model Y"), phoneAlong(route, "")...)
	points = append(points, phoneAtHome("iphone")...)
	verdict, known := witnessPhone(t, points)
	if !known || verdict != Absent {
		t.Errorf("verdict = %v (known %v), want absent from the phone's own points", verdict, known)
	}
}

func TestSilentPhoneKnowsNothing(t *testing.T) {
	t.Parallel()

	if _, known := witnessPhone(t, nil); known {
		t.Error("known = true, want a phone without points to leave the decision to others")
	}
}

func TestDistanceMetersAcrossPrague(t *testing.T) {
	t.Parallel()

	got := distanceMeters(50.0870, 14.4208, 50.0755, 14.4378)
	if got < 1600 || got > 1800 {
		t.Errorf("distance = %.0f m, want about 1.7 km", got)
	}
}
