// Package syncer copies TeslaMate positions into Dawarich.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/dawarich"
	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

type Source interface {
	Positions(ctx context.Context, query teslamate.Query) ([]teslamate.Position, error)
}

type Sink interface {
	SendPoints(ctx context.Context, points []dawarich.Point) error
}

type Cursor interface {
	Load() (time.Time, error)
	Save(at time.Time) error
}

type Options struct {
	PollInterval    time.Duration
	OverlapWindow   time.Duration
	InitialLookback time.Duration
	BatchSize       int
	DrivesOnly      bool
	CarIDs          []int32
	TrackerPrefix   string
}

type Syncer struct {
	source Source
	sink   Sink
	cursor Cursor
	opts   Options
	logger *slog.Logger

	now func() time.Time
}

func New(source Source, sink Sink, cursor Cursor, opts Options, logger *slog.Logger) *Syncer {
	return &Syncer{
		source: source,
		sink:   sink,
		cursor: cursor,
		opts:   opts,
		logger: logger,
		now:    time.Now,
	}
}

func (s *Syncer) Run(ctx context.Context, nudge <-chan struct{}) error {
	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()

	for {
		switch synced, err := s.SyncOnce(ctx); {
		case err != nil && ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			s.logger.Error("sync pass failed", "error", err)
		case synced > 0:
			s.logger.Info("sync pass complete", "points", synced)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-nudge:
			ticker.Reset(s.opts.PollInterval)
		}
	}
}

func (s *Syncer) SyncOnce(ctx context.Context) (int, error) {
	cursor, err := s.startOfPass()
	if err != nil {
		return 0, err
	}

	sent := 0
	for {
		positions, err := s.source.Positions(ctx, teslamate.Query{
			After:      cursor.Add(-s.opts.OverlapWindow),
			CarIDs:     s.opts.CarIDs,
			DrivesOnly: s.opts.DrivesOnly,
			Limit:      s.opts.BatchSize,
		})
		if err != nil {
			return sent, err
		}
		if len(positions) == 0 {
			return sent, nil
		}

		points := make([]dawarich.Point, 0, len(positions))
		for _, position := range positions {
			points = append(points, toPoint(position, s.opts.TrackerPrefix))
		}
		if err := s.sink.SendPoints(ctx, points); err != nil {
			return sent, fmt.Errorf("send %d points: %w", len(points), err)
		}
		sent += len(points)

		newest := positions[len(positions)-1].Date
		if err := s.cursor.Save(newest); err != nil {
			return sent, fmt.Errorf("save cursor: %w", err)
		}
		s.logger.Debug("batch synced", "points", len(points), "through", newest)

		pageWasPartial := len(positions) < s.opts.BatchSize
		if pageWasPartial || !newest.After(cursor) {
			return sent, nil
		}
		cursor = newest
	}
}

func (s *Syncer) startOfPass() (time.Time, error) {
	stored, err := s.cursor.Load()
	if err != nil {
		return time.Time{}, fmt.Errorf("load cursor: %w", err)
	}
	if !stored.IsZero() {
		return stored, nil
	}
	return s.now().Add(-s.opts.InitialLookback), nil
}
