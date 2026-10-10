// Package teslamate reads vehicle positions from TeslaMate's database.
package teslamate

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Position struct {
	ID        int64
	CarID     int32
	CarName   string
	Date      time.Time
	Latitude  float64
	Longitude float64

	ElevationMeters *int32
	SpeedKmh        *int32
	PowerKilowatts  *float64
	BatteryPercent  *int32
	DriveID         *int64
}

type Query struct {
	After      time.Time
	AfterID    int64
	CarIDs     []int32
	DrivesOnly bool
	Limit      int
}

type Store struct {
	pool *pgxpool.Pool
}

const maxConnections = 2

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse TeslaMate DSN: %w", err)
	}
	cfg.MaxConns = maxConnections
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to TeslaMate: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping TeslaMate: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

const positionsSQL = `
SELECT p.id,
       p.car_id,
       COALESCE(NULLIF(c.name, ''), 'car-' || p.car_id) AS car_name,
       p.date,
       p.latitude::float8,
       p.longitude::float8,
       p.elevation::int,
       p.speed::int,
       p.power::float8,
       p.battery_level::int,
       p.drive_id
FROM positions p
JOIN cars c ON c.id = p.car_id
WHERE (p.date > $1::timestamp OR (p.date = $1::timestamp AND p.id > $5))
  AND p.latitude IS NOT NULL
  AND p.longitude IS NOT NULL
  AND (cardinality($2::int[]) = 0 OR p.car_id = ANY($2::int[]))
  AND (NOT $3::bool OR p.drive_id IS NOT NULL)
ORDER BY p.date, p.id
LIMIT $4`

func (s *Store) Positions(ctx context.Context, query Query) ([]Position, error) {
	carIDs := query.CarIDs
	if carIDs == nil {
		carIDs = []int32{}
	}

	rows, err := s.pool.Query(ctx, positionsSQL,
		query.After.UTC(), carIDs, query.DrivesOnly, query.Limit, query.AfterID)
	if err != nil {
		return nil, fmt.Errorf("query positions: %w", err)
	}
	defer rows.Close()

	positions := make([]Position, 0, query.Limit)
	for rows.Next() {
		var p Position
		if err := rows.Scan(
			&p.ID, &p.CarID, &p.CarName, &p.Date,
			&p.Latitude, &p.Longitude,
			&p.ElevationMeters, &p.SpeedKmh, &p.PowerKilowatts, &p.BatteryPercent, &p.DriveID,
		); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		positions = append(positions, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read positions: %w", err)
	}
	return positions, nil
}

type Drive struct {
	ID        int64
	StartedAt time.Time
	EndedAt   *time.Time
}

type RoutePoint struct {
	At        time.Time
	Latitude  float64
	Longitude float64
}

func (s *Store) Drive(ctx context.Context, id int64) (Drive, error) {
	drive := Drive{ID: id}
	err := s.pool.QueryRow(ctx, `SELECT start_date, end_date FROM drives WHERE id = $1`, id).
		Scan(&drive.StartedAt, &drive.EndedAt)
	if err != nil {
		return Drive{}, fmt.Errorf("query drive %d: %w", id, err)
	}
	return drive, nil
}

const driveRouteSQL = `
SELECT date, latitude::float8, longitude::float8
FROM positions
WHERE drive_id = $1
  AND latitude IS NOT NULL
  AND longitude IS NOT NULL
ORDER BY date, id`

func (s *Store) DriveRoute(ctx context.Context, id int64) ([]RoutePoint, error) {
	rows, err := s.pool.Query(ctx, driveRouteSQL, id)
	if err != nil {
		return nil, fmt.Errorf("query route of drive %d: %w", id, err)
	}
	defer rows.Close()

	var route []RoutePoint
	for rows.Next() {
		var point RoutePoint
		if err := rows.Scan(&point.At, &point.Latitude, &point.Longitude); err != nil {
			return nil, fmt.Errorf("scan route point: %w", err)
		}
		route = append(route, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read route of drive %d: %w", id, err)
	}
	return route, nil
}
