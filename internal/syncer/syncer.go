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

const progressLogStep = 10_000

type Syncer struct {
	source Source
	sink   Sink
	cursor Cursor
	opts   Options
	logger *slog.Logger

	sentInOverlap map[int64]time.Time

	now func() time.Time
}

func New(source Source, sink Sink, cursor Cursor, opts Options, logger *slog.Logger) *Syncer {
	return &Syncer{
		source:        source,
		sink:          sink,
		cursor:        cursor,
		opts:          opts,
		logger:        logger,
		sentInOverlap: map[int64]time.Time{},
		now:           time.Now,
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

	page := teslamate.Query{
		After:      cursor.Add(-s.opts.OverlapWindow),
		CarIDs:     s.opts.CarIDs,
		DrivesOnly: s.opts.DrivesOnly,
		Limit:      s.opts.BatchSize,
	}

	sent, loggedAt := 0, 0
	for {
		positions, err := s.source.Positions(ctx, page)
		if err != nil {
			return sent, err
		}
		if len(positions) == 0 {
			return sent, nil
		}

		unsent := s.unsentPositions(positions)
		points := make([]dawarich.Point, 0, len(unsent))
		for _, position := range unsent {
			points = append(points, toPoint(position, s.opts.TrackerPrefix))
		}
		if len(points) > 0 {
			if err := s.sink.SendPoints(ctx, points); err != nil {
				return sent, fmt.Errorf("send %d points: %w", len(points), err)
			}
			s.rememberSent(unsent)
			sent += len(points)
		}

		last := positions[len(positions)-1]
		if err := s.cursor.Save(last.Date); err != nil {
			return sent, fmt.Errorf("save cursor: %w", err)
		}
		if len(points) > 0 {
			s.logger.Debug("batch synced", "points", len(points), "through", last.Date)
		}
		if sent-loggedAt >= progressLogStep {
			loggedAt = sent
			s.logger.Info("sync in progress", "points", sent, "through", last.Date)
		}

		if len(positions) < s.opts.BatchSize {
			return sent, nil
		}
		page.After, page.AfterID = last.Date, last.ID
	}
}

func (s *Syncer) unsentPositions(positions []teslamate.Position) []teslamate.Position {
	unsent := make([]teslamate.Position, 0, len(positions))
	for _, position := range positions {
		if _, alreadySent := s.sentInOverlap[position.ID]; !alreadySent {
			unsent = append(unsent, position)
		}
	}
	return unsent
}

func (s *Syncer) rememberSent(positions []teslamate.Position) {
	var newest time.Time
	for _, position := range positions {
		s.sentInOverlap[position.ID] = position.Date
		if position.Date.After(newest) {
			newest = position.Date
		}
	}
	if newest.IsZero() {
		return
	}

	forgetBefore := newest.Add(-2 * s.opts.OverlapWindow)
	for id, at := range s.sentInOverlap {
		if at.Before(forgetBefore) {
			delete(s.sentInOverlap, id)
		}
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
