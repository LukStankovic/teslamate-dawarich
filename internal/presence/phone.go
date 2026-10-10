package presence

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/dawarich"
	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

type Routes interface {
	DriveRoute(ctx context.Context, id int64) ([]teslamate.RoutePoint, error)
}

type PhonePoints interface {
	Points(ctx context.Context, from, to time.Time) ([]dawarich.TrackedPoint, error)
}

const (
	routeTimeTolerance = time.Minute
	presentShare       = 0.5
)

type PhoneSignal struct {
	routes       Routes
	points       PhonePoints
	trackerID    string
	radiusMeters float64
	uploadWait   time.Duration

	now func() time.Time
}

func NewPhoneSignal(routes Routes, points PhonePoints, trackerID string, radiusMeters float64, uploadWait time.Duration) *PhoneSignal {
	return &PhoneSignal{
		routes:       routes,
		points:       points,
		trackerID:    trackerID,
		radiusMeters: radiusMeters,
		uploadWait:   uploadWait,
		now:          time.Now,
	}
}

func (s *PhoneSignal) Name() string { return "dawarich " + s.trackerID }

func (s *PhoneSignal) Witness(ctx context.Context, drive teslamate.Drive) (Verdict, bool, error) {
	phone, err := s.ownPoints(ctx, drive.StartedAt, *drive.EndedAt)
	if err != nil {
		return Pending, false, fmt.Errorf("read phone points for drive %d: %w", drive.ID, err)
	}
	if len(phone) == 0 {
		return s.awaitUpload(ctx, drive)
	}

	route, err := s.routes.DriveRoute(ctx, drive.ID)
	if err != nil {
		return Pending, false, err
	}
	if float64(countAlongRoute(phone, route, s.radiusMeters)) >= presentShare*float64(len(phone)) {
		return Present, true, nil
	}
	return Absent, true, nil
}

func (s *PhoneSignal) awaitUpload(ctx context.Context, drive teslamate.Drive) (Verdict, bool, error) {
	now := s.now()
	if !now.Before(drive.EndedAt.Add(s.uploadWait)) {
		return Pending, false, nil
	}

	later, err := s.ownPoints(ctx, *drive.EndedAt, now)
	if err != nil {
		return Pending, false, fmt.Errorf("read phone points after drive %d: %w", drive.ID, err)
	}
	if len(later) > 0 {
		return Pending, false, nil
	}
	return Pending, true, nil
}

func (s *PhoneSignal) ownPoints(ctx context.Context, from, to time.Time) ([]dawarich.TrackedPoint, error) {
	points, err := s.points.Points(ctx, from, to)
	if err != nil {
		return nil, err
	}
	own := make([]dawarich.TrackedPoint, 0, len(points))
	for _, point := range points {
		if point.TrackerID == s.trackerID {
			own = append(own, point)
		}
	}
	return own, nil
}

func countAlongRoute(phone []dawarich.TrackedPoint, route []teslamate.RoutePoint, radiusMeters float64) int {
	matched := 0
	for _, point := range phone {
		if nearRoute(point, route, radiusMeters) {
			matched++
		}
	}
	return matched
}

func nearRoute(point dawarich.TrackedPoint, route []teslamate.RoutePoint, radiusMeters float64) bool {
	earliest, latest := point.Timestamp.Add(-routeTimeTolerance), point.Timestamp.Add(routeTimeTolerance)
	first := sort.Search(len(route), func(i int) bool { return !route[i].At.Before(earliest) })
	for _, car := range route[first:] {
		if car.At.After(latest) {
			return false
		}
		if distanceMeters(point.Latitude, point.Longitude, car.Latitude, car.Longitude) <= radiusMeters {
			return true
		}
	}
	return false
}

const earthRadiusMeters = 6_371_000

func distanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	toRadians := func(degrees float64) float64 { return degrees * math.Pi / 180 }
	dLat, dLon := toRadians(lat2-lat1), toRadians(lon2-lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRadians(lat1))*math.Cos(toRadians(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusMeters * math.Asin(math.Sqrt(a))
}
