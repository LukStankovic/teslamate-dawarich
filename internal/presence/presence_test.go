package presence

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

var (
	driveStart  = time.Date(2026, time.October, 8, 8, 0, 0, 0, time.UTC)
	driveEnd    = driveStart.Add(10 * time.Minute)
	afterSettle = driveEnd.Add(time.Hour)
)

type fakeDrives struct {
	drive teslamate.Drive
	route []teslamate.RoutePoint
}

func (f *fakeDrives) Drive(context.Context, int64) (teslamate.Drive, error) { return f.drive, nil }

func (f *fakeDrives) DriveRoute(context.Context, int64) ([]teslamate.RoutePoint, error) {
	return f.route, nil
}

type fakeSignal struct {
	verdict Verdict
	known   bool
	err     error
	calls   int
}

func (f *fakeSignal) Name() string { return "fake" }

func (f *fakeSignal) Witness(context.Context, teslamate.Drive) (Verdict, bool, error) {
	f.calls++
	return f.verdict, f.known, f.err
}

func finishedDrive() teslamate.Drive {
	end := driveEnd
	return teslamate.Drive{ID: 7, StartedAt: driveStart, EndedAt: &end}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestChecker(drive teslamate.Drive, opts Options, signals ...Signal) *Checker {
	checker := NewChecker(&fakeDrives{drive: drive}, signals, opts, discardLogger())
	checker.now = func() time.Time { return afterSettle }
	return checker
}

func TestFirstSignalThatKnowsDecides(t *testing.T) {
	t.Parallel()

	silent := &fakeSignal{}
	knowing := &fakeSignal{verdict: Absent, known: true}
	never := &fakeSignal{verdict: Present, known: true}
	checker := newTestChecker(finishedDrive(), Options{}, silent, knowing, never)

	verdict, err := checker.Verdict(context.Background(), 7)
	if err != nil {
		t.Fatalf("Verdict: %v", err)
	}
	if verdict != Absent {
		t.Errorf("verdict = %v, want absent from the second signal", verdict)
	}
	if silent.calls != 1 || never.calls != 0 {
		t.Errorf("signal calls = %d/%d, want 1/0", silent.calls, never.calls)
	}
}

func TestDriveNoSignalKnowsAboutFollowsTheFallback(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		syncWhenUnknown bool
		want            Verdict
	}{
		"skipped by default":    {false, Absent},
		"synced when asked for": {true, Present},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			checker := newTestChecker(finishedDrive(), Options{SyncWhenUnknown: tc.syncWhenUnknown}, &fakeSignal{})
			verdict, err := checker.Verdict(context.Background(), 7)
			if err != nil {
				t.Fatalf("Verdict: %v", err)
			}
			if verdict != tc.want {
				t.Errorf("verdict = %v, want %v", verdict, tc.want)
			}
		})
	}
}

func TestDriveStaysPendingUntilItEndsAndSettles(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		drive teslamate.Drive
		now   time.Time
	}{
		"still driving":    {teslamate.Drive{ID: 7, StartedAt: driveStart}, afterSettle},
		"phone may upload": {finishedDrive(), driveEnd.Add(5 * time.Minute)},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			signal := &fakeSignal{verdict: Present, known: true}
			checker := newTestChecker(tc.drive, Options{SettleDelay: 15 * time.Minute}, signal)
			checker.now = func() time.Time { return tc.now }

			verdict, err := checker.Verdict(context.Background(), 7)
			if err != nil {
				t.Fatalf("Verdict: %v", err)
			}
			if verdict != Pending || signal.calls != 0 {
				t.Errorf("verdict = %v after %d signal calls, want pending without asking", verdict, signal.calls)
			}
		})
	}
}

func TestVerdictIsDecidedOncePerDrive(t *testing.T) {
	t.Parallel()

	signal := &fakeSignal{verdict: Absent, known: true}
	checker := newTestChecker(finishedDrive(), Options{}, signal)

	for range 3 {
		if _, err := checker.Verdict(context.Background(), 7); err != nil {
			t.Fatalf("Verdict: %v", err)
		}
	}
	if signal.calls != 1 {
		t.Errorf("signal asked %d times, want 1", signal.calls)
	}
}

func TestFailingSignalFallsThroughToTheNextOne(t *testing.T) {
	t.Parallel()

	checker := newTestChecker(finishedDrive(), Options{},
		&fakeSignal{err: errors.New("home assistant down")}, &fakeSignal{verdict: Present, known: true})

	verdict, err := checker.Verdict(context.Background(), 7)
	if err != nil || verdict != Present {
		t.Errorf("Verdict = %v, %v, want present from the working signal", verdict, err)
	}
}

func TestFailingSignalLeavesTheDriveUndecided(t *testing.T) {
	t.Parallel()

	signal := &fakeSignal{err: errors.New("dawarich down")}
	checker := newTestChecker(finishedDrive(), Options{}, signal)

	if _, err := checker.Verdict(context.Background(), 7); err == nil {
		t.Fatal("Verdict succeeded, want the signal error")
	}
	signal.err, signal.verdict, signal.known = nil, Absent, true
	if verdict, err := checker.Verdict(context.Background(), 7); err != nil || verdict != Absent {
		t.Errorf("Verdict after recovery = %v, %v, want absent", verdict, err)
	}
}

func TestWaitingSignalHoldsTheDriveAndIsAskedAgainLater(t *testing.T) {
	t.Parallel()

	signal := &fakeSignal{verdict: Pending, known: true}
	checker := newTestChecker(finishedDrive(), Options{SyncWhenUnknown: true}, signal)

	now := afterSettle
	checker.now = func() time.Time { return now }
	for range 2 {
		if verdict, err := checker.Verdict(context.Background(), 7); err != nil || verdict != Pending {
			t.Fatalf("Verdict = %v, %v, want pending", verdict, err)
		}
	}
	if signal.calls != 1 {
		t.Errorf("signal asked %d times before the recheck, want 1", signal.calls)
	}

	now = now.Add(recheckInterval)
	signal.verdict = Absent
	if verdict, err := checker.Verdict(context.Background(), 7); err != nil || verdict != Absent {
		t.Errorf("Verdict after recheck = %v, %v, want absent", verdict, err)
	}
}
