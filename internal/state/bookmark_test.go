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
	at := time.Date(2026, time.August, 18, 17, 42, 3, 0, time.UTC)

	if err := bookmark.Save(at); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := bookmark.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Equal(at) {
		t.Errorf("Load = %v, want %v", got, at)
	}
}

func TestBookmarkMissingFileIsZero(t *testing.T) {
	t.Parallel()

	got, err := NewBookmark(t.TempDir()).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("Load = %v, want the zero time", got)
	}
}

func TestBookmarkCorruptFileErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bookmark.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewBookmark(dir).Load(); err == nil {
		t.Fatal("Load succeeded, want an error for a corrupt bookmark")
	}
}

func TestBookmarkSaveLeavesNoTempFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := NewBookmark(dir).Save(time.Now()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "bookmark.json" {
		t.Errorf("directory contains %v, want only bookmark.json", entries)
	}
}
