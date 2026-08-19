package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBookmarkRoundTrip(t *testing.T) {
	t.Parallel()

	bookmark := NewBookmark(t.TempDir())
	saved := Checkpoint{
		LastPositionAt: time.Date(2026, time.August, 19, 17, 42, 3, 0, time.UTC),
		SentPositionID: []int64{7, 8, 9},
	}

	if err := bookmark.Save(saved); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := bookmark.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.LastPositionAt.Equal(saved.LastPositionAt) {
		t.Errorf("LastPositionAt = %v, want %v", loaded.LastPositionAt, saved.LastPositionAt)
	}
	if len(loaded.SentPositionID) != len(saved.SentPositionID) {
		t.Fatalf("SentPositionID = %v, want %v", loaded.SentPositionID, saved.SentPositionID)
	}
	for i, id := range saved.SentPositionID {
		if loaded.SentPositionID[i] != id {
			t.Errorf("SentPositionID[%d] = %d, want %d", i, loaded.SentPositionID[i], id)
		}
	}
}

func TestBookmarkMissingFileIsEmpty(t *testing.T) {
	t.Parallel()

	loaded, err := NewBookmark(t.TempDir()).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.LastPositionAt.IsZero() || loaded.SentPositionID != nil {
		t.Errorf("Load = %+v, want the zero checkpoint", loaded)
	}
}

func TestBookmarkCorruptFileErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, bookmarkFilename), []byte("{not json"), fileMode); err != nil {
		t.Fatal(err)
	}

	if _, err := NewBookmark(dir).Load(); err == nil {
		t.Fatal("Load succeeded, want an error for a corrupt bookmark")
	}
}

func TestBookmarkSaveLeavesNoTempFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := NewBookmark(dir).Save(Checkpoint{LastPositionAt: time.Unix(1, 0)}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != bookmarkFilename {
		t.Errorf("directory contains %v, want only %s", entries, bookmarkFilename)
	}
}
