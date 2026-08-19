// Package state persists the sync cursor across restarts.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	bookmarkFilename = "bookmark.json"
	containerDataDir = "/data"
	fileMode         = 0o644
)

type Checkpoint struct {
	LastPositionAt time.Time `json:"last_position_at"`
	SentPositionID []int64   `json:"sent_position_ids,omitempty"`
}

type Bookmark struct {
	path string
}

func DefaultDir() string {
	if info, err := os.Stat(containerDataDir); err == nil && info.IsDir() {
		return containerDataDir
	}
	return "."
}

func NewBookmark(dir string) *Bookmark {
	return &Bookmark{path: filepath.Join(dir, bookmarkFilename)}
}

func (b *Bookmark) Path() string { return b.path }

func (b *Bookmark) Load() (Checkpoint, error) {
	data, err := os.ReadFile(b.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Checkpoint{}, nil
	}
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read bookmark: %w", err)
	}

	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("parse bookmark %s: %w", b.path, err)
	}
	return checkpoint, nil
}

func (b *Bookmark) Save(checkpoint Checkpoint) error {
	checkpoint.LastPositionAt = checkpoint.LastPositionAt.UTC()

	data, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode bookmark: %w", err)
	}

	partial := b.path + ".tmp"
	if err := os.WriteFile(partial, data, fileMode); err != nil {
		return fmt.Errorf("write bookmark: %w", err)
	}
	if err := os.Rename(partial, b.path); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("replace bookmark: %w", err)
	}
	return nil
}
