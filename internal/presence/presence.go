// Package presence decides whether the owner was in the car during a drive.
package presence

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

type Verdict int

const (
	Pending Verdict = iota
	Present
	Absent
)

func (v Verdict) String() string {
	switch v {
	case Present:
		return "present"
	case Absent:
		return "absent"
	default:
		return "pending"
	}
}

type Drives interface {
	Drive(ctx context.Context, id int64) (teslamate.Drive, error)
}

type Signal interface {
	Name() string
	Witness(ctx context.Context, drive teslamate.Drive) (verdict Verdict, known bool, err error)
}

type Options struct {
	SettleDelay     time.Duration
	SyncWhenUnknown bool
}

const recheckInterval = 5 * time.Minute

type Checker struct {
	drives  Drives
	signals []Signal
	opts    Options
	logger  *slog.Logger

	decided   map[int64]Verdict
	recheckAt map[int64]time.Time

	now func() time.Time
}

func NewChecker(drives Drives, signals []Signal, opts Options, logger *slog.Logger) *Checker {
	return &Checker{
		drives:    drives,
		signals:   signals,
		opts:      opts,
		logger:    logger,
		decided:   map[int64]Verdict{},
		recheckAt: map[int64]time.Time{},
		now:       time.Now,
	}
}

func (c *Checker) Verdict(ctx context.Context, driveID int64) (Verdict, error) {
	if verdict, ok := c.decided[driveID]; ok {
		return verdict, nil
	}
	if at, ok := c.recheckAt[driveID]; ok && c.now().Before(at) {
		return Pending, nil
	}

	drive, err := c.drives.Drive(ctx, driveID)
	if err != nil {
		return Pending, err
	}
	if drive.EndedAt == nil || c.now().Before(drive.EndedAt.Add(c.opts.SettleDelay)) {
		return Pending, nil
	}

	verdict, decidedBy, err := c.witness(ctx, drive)
	if err != nil {
		return Pending, err
	}
	if verdict == Pending {
		c.logger.Debug("drive waits for presence source", "drive_id", driveID, "waiting_on", decidedBy)
		c.recheckAt[driveID] = c.now().Add(recheckInterval)
		return Pending, nil
	}
	delete(c.recheckAt, driveID)

	c.logger.Info("drive checked for presence",
		"drive_id", driveID, "started_at", drive.StartedAt, "verdict", verdict, "decided_by", decidedBy)
	c.decided[driveID] = verdict
	return verdict, nil
}

func (c *Checker) witness(ctx context.Context, drive teslamate.Drive) (Verdict, string, error) {
	var failures []error
	for _, signal := range c.signals {
		verdict, known, err := signal.Witness(ctx, drive)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if known {
			return verdict, signal.Name(), nil
		}
	}

	if len(failures) > 0 {
		return Pending, "", errors.Join(failures...)
	}
	if c.opts.SyncWhenUnknown {
		return Present, "fallback", nil
	}
	return Absent, "fallback", nil
}
