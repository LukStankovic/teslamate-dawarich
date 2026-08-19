package syncer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/dawarich"
	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeSource struct {
	positions []teslamate.Position
	err       error

	mu      sync.Mutex
	queries []teslamate.Query
}

func (f *fakeSource) Positions(_ context.Context, query teslamate.Query) ([]teslamate.Position, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}

	var page []teslamate.Position
	for _, position := range f.positions {
		afterPage := position.Date.After(query.After) ||
			(position.Date.Equal(query.After) && position.ID > query.AfterID)
		if !afterPage {
			continue
		}
		page = append(page, position)
		if len(page) == query.Limit {
			break
		}
	}
	return page, nil
}

func (f *fakeSource) firstQuery() teslamate.Query {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries[0]
}

func (f *fakeSource) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

type fakeSink struct {
	err error

	mu      sync.Mutex
	batches [][]dawarich.Point
}

func (f *fakeSink) SendPoints(_ context.Context, points []dawarich.Point) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, points)
	return nil
}

func (f *fakeSink) batchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

type memoryCursor struct {
	mu        sync.Mutex
	at        time.Time
	saveCount int
}

func (m *memoryCursor) Load() (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at, nil
}

func (m *memoryCursor) Save(at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.at = at
	m.saveCount++
	return nil
}

func (m *memoryCursor) state() (time.Time, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at, m.saveCount
}

func positionsEverySecond(count int, start time.Time) []teslamate.Position {
	out := make([]teslamate.Position, 0, count)
	for i := range count {
		out = append(out, teslamate.Position{
			ID:        int64(i + 1),
			CarName:   "Model Y",
			Date:      start.Add(time.Duration(i) * time.Second),
			Latitude:  50 + float64(i)/10000,
			Longitude: 14 + float64(i)/10000,
		})
	}
	return out
}

func newTestSyncer(source Source, sink Sink, cursor Cursor, opts Options) *Syncer {
	if opts.BatchSize == 0 {
		opts.BatchSize = 2
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Hour
	}
	return New(source, sink, cursor, opts, discardLogger())
}

func TestSyncOncePagesUntilDrained(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{positions: positionsEverySecond(5, start)}
	sink := &fakeSink{}
	cursor := &memoryCursor{at: start.Add(-time.Second)}

	synced, err := newTestSyncer(source, sink, cursor, Options{BatchSize: 2}).SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if synced != 5 {
		t.Errorf("synced = %d, want 5", synced)
	}
	if sink.batchCount() != 3 {
		t.Errorf("batches = %d, want 3", sink.batchCount())
	}
	if at, _ := cursor.state(); !at.Equal(start.Add(4 * time.Second)) {
		t.Errorf("cursor = %v, want %v", at, start.Add(4*time.Second))
	}
}

func TestSyncOnceQueriesBehindTheCursorByTheOverlapWindow(t *testing.T) {
	t.Parallel()

	cursorAt := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{}
	syncer := newTestSyncer(source, &fakeSink{}, &memoryCursor{at: cursorAt}, Options{OverlapWindow: 5 * time.Minute})

	if _, err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if source.queryCount() != 1 {
		t.Fatalf("queries = %d, want 1", source.queryCount())
	}
	if want := cursorAt.Add(-5 * time.Minute); !source.firstQuery().After.Equal(want) {
		t.Errorf("query after = %v, want %v", source.firstQuery().After, want)
	}
}

func TestSyncOnceWithoutCursorStartsAtInitialLookback(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{}
	syncer := newTestSyncer(source, &fakeSink{}, &memoryCursor{}, Options{InitialLookback: 24 * time.Hour})
	syncer.now = func() time.Time { return now }

	if _, err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if want := now.Add(-24 * time.Hour); !source.firstQuery().After.Equal(want) {
		t.Errorf("query after = %v, want %v", source.firstQuery().After, want)
	}
}

func TestSyncOnceKeepsCursorWhenDawarichRejectsTheBatch(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{positions: positionsEverySecond(2, start)}
	cursor := &memoryCursor{at: start.Add(-time.Second)}
	syncer := newTestSyncer(source, &fakeSink{err: errors.New("dawarich down")}, cursor, Options{})

	if _, err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("SyncOnce succeeded, want the sink error")
	}
	if _, saves := cursor.state(); saves != 0 {
		t.Errorf("cursor saved %d times, want 0", saves)
	}
}

func TestSyncOnceDrainsPositionsSharingOneTimestamp(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	positions := make([]teslamate.Position, 0, 5)
	for i := range 5 {
		positions = append(positions, teslamate.Position{ID: int64(i + 1), Date: at, CarName: "Model Y"})
	}
	source := &fakeSource{positions: positions}
	syncer := newTestSyncer(source, &fakeSink{}, &memoryCursor{at: at}, Options{
		BatchSize:     2,
		OverlapWindow: time.Minute,
	})

	done := make(chan int, 1)
	go func() {
		synced, err := syncer.SyncOnce(context.Background())
		if err != nil {
			t.Errorf("SyncOnce: %v", err)
		}
		done <- synced
	}()

	select {
	case synced := <-done:
		if synced != len(positions) {
			t.Errorf("synced = %d, want %d", synced, len(positions))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SyncOnce did not return")
	}
}

func TestSyncOncePagesByIDWithinOneTimestamp(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{positions: []teslamate.Position{
		{ID: 1, Date: at, CarName: "Model Y"},
		{ID: 2, Date: at, CarName: "Model Y"},
	}}
	syncer := newTestSyncer(source, &fakeSink{}, &memoryCursor{at: at}, Options{BatchSize: 1})

	if _, err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	source.mu.Lock()
	queries := append([]teslamate.Query(nil), source.queries...)
	source.mu.Unlock()

	if len(queries) < 2 {
		t.Fatalf("queries = %d, want at least 2", len(queries))
	}
	if queries[1].AfterID != 1 {
		t.Errorf("second query AfterID = %d, want 1", queries[1].AfterID)
	}
}

func TestRunSyncsOnNudge(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{positions: positionsEverySecond(1, start)}
	sink := &fakeSink{}
	cursor := &memoryCursor{at: start.Add(-time.Second)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nudge := make(chan struct{}, 1)
	nudge <- struct{}{}

	syncer := newTestSyncer(source, sink, cursor, Options{BatchSize: 10, PollInterval: time.Hour})
	go func() { _ = syncer.Run(ctx, nudge) }()

	deadline := time.After(2 * time.Second)
	for {
		if at, _ := cursor.state(); sink.batchCount() > 0 && at.Equal(start) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Run did not sync: batches = %d", sink.batchCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestSyncOnceLogsProgressDuringALongPass(t *testing.T) {
	t.Parallel()

	var logged strings.Builder
	start := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{positions: positionsEverySecond(progressLogStep*2, start)}
	syncer := New(source, &fakeSink{}, &memoryCursor{at: start.Add(-time.Second)}, Options{
		BatchSize:    progressLogStep / 2,
		PollInterval: time.Hour,
	}, slog.New(slog.NewTextHandler(&logged, nil)))

	if _, err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if !strings.Contains(logged.String(), "sync in progress") {
		t.Errorf("log = %q, want a progress line", logged.String())
	}
}

func TestSyncOnceDoesNotResendTheOverlapWindow(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{positions: positionsEverySecond(3, start)}
	sink := &fakeSink{}
	cursor := &memoryCursor{at: start.Add(-time.Second)}
	syncer := newTestSyncer(source, sink, cursor, Options{BatchSize: 10, OverlapWindow: 5 * time.Minute})

	first, err := syncer.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	if first != 3 {
		t.Fatalf("first pass synced %d, want 3", first)
	}

	second, err := syncer.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	if second != 0 {
		t.Errorf("second pass synced %d, want 0: the overlap window must not be re-sent", second)
	}
	if sink.batchCount() != 1 {
		t.Errorf("batches = %d, want 1: Dawarich announces every accepted batch as a new location", sink.batchCount())
	}
}

func TestSyncOnceSendsPositionsThatCommittedLateInsideTheOverlap(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{positions: positionsEverySecond(3, start)}
	sink := &fakeSink{}
	cursor := &memoryCursor{at: start.Add(-time.Second)}
	syncer := newTestSyncer(source, sink, cursor, Options{BatchSize: 10, OverlapWindow: 5 * time.Minute})

	if _, err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}

	lateRow := teslamate.Position{ID: 99, CarName: "Model Y", Date: start.Add(time.Second)}
	source.positions = append(source.positions, lateRow)
	sortPositionsByDate(source.positions)

	synced, err := syncer.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	if synced != 1 {
		t.Errorf("synced = %d, want 1: a row that became visible late must still be sent", synced)
	}
}

func sortPositionsByDate(positions []teslamate.Position) {
	slices.SortFunc(positions, func(a, b teslamate.Position) int {
		if a.Date.Equal(b.Date) {
			return int(a.ID - b.ID)
		}
		return a.Date.Compare(b.Date)
	})
}
