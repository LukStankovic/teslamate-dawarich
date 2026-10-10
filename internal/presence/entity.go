package presence

import (
	"context"
	"fmt"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/homeassistant"
	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

type EntityHistory interface {
	History(ctx context.Context, entityID string, from, to time.Time) ([]homeassistant.StateChange, error)
}

type EntitySignal struct {
	history  EntityHistory
	entityID string
}

func NewEntitySignal(history EntityHistory, entityID string) *EntitySignal {
	return &EntitySignal{history: history, entityID: entityID}
}

func (s *EntitySignal) Name() string { return "home assistant " + s.entityID }

func (s *EntitySignal) Witness(ctx context.Context, drive teslamate.Drive) (Verdict, bool, error) {
	changes, err := s.history.History(ctx, s.entityID, drive.StartedAt, *drive.EndedAt)
	if err != nil {
		return Pending, false, fmt.Errorf("read %s history for drive %d: %w", s.entityID, drive.ID, err)
	}

	on, known := timeOn(changes, drive.StartedAt, *drive.EndedAt)
	if known.Seconds() < presentShare*drive.EndedAt.Sub(drive.StartedAt).Seconds() {
		return Pending, false, nil
	}
	if on.Seconds() >= presentShare*known.Seconds() {
		return Present, true, nil
	}
	return Absent, true, nil
}

func timeOn(changes []homeassistant.StateChange, from, to time.Time) (on, known time.Duration) {
	for i, change := range changes {
		spanStart := later(change.Since, from)
		spanEnd := to
		if i+1 < len(changes) {
			spanEnd = earlier(changes[i+1].Since, to)
		}
		if !spanEnd.After(spanStart) {
			continue
		}

		if change.State == "unavailable" || change.State == "unknown" {
			continue
		}
		span := spanEnd.Sub(spanStart)
		known += span
		if change.State == "on" {
			on += span
		}
	}
	return on, known
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
