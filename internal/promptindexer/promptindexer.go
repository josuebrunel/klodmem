// Package promptindexer scans and watches Claude Code's global prompt history
// file (~/.claude/history.jsonl), incrementally indexing newly-appended lines
// into a Store's prompt-history table.
package promptindexer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/josuebrunel/klodmem/internal/debounce"
	"github.com/josuebrunel/klodmem/internal/promptfile"
	"github.com/josuebrunel/klodmem/internal/store"
)

const debounceWindow = 500 * time.Millisecond

// Indexer keeps a Store's prompt-history table in sync with one history file.
type Indexer struct {
	path  string
	store *store.Store
	log   *slog.Logger
}

// New creates an Indexer that scans/watches path (a file shaped like
// "~/.claude/history.jsonl").
func New(path string, s *store.Store, log *slog.Logger) *Indexer {
	if log == nil {
		log = slog.Default()
	}
	return &Indexer{path: path, store: s, log: log}
}

// FullScan incrementally parses the history file (only newly-appended bytes
// since the last scan) and removes indexed prompts if the file no longer
// exists.
func (idx *Indexer) FullScan(ctx context.Context) error {
	n, err := idx.ingestPath(ctx)
	if err != nil {
		return err
	}
	idx.log.Info("promptindexer: scan complete", "prompts", n)
	return nil
}

// ingestPath reads any bytes appended to the history file since the last
// recorded offset, parses complete lines, and stores newly-found prompts. It
// returns the number of prompts indexed.
func (idx *Indexer) ingestPath(ctx context.Context) (int, error) {
	info, err := os.Stat(idx.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, idx.store.DeletePromptFile(ctx, idx.path)
	}
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", idx.path, err)
	}

	offset, _, err := idx.store.PromptOffset(ctx, idx.path)
	if err != nil {
		return 0, err
	}
	if offset > info.Size() {
		// File was truncated or rewritten — drop what's indexed and re-read.
		if err := idx.store.DeletePromptFile(ctx, idx.path); err != nil {
			return 0, err
		}
		offset = 0
	}
	if offset == info.Size() {
		return 0, nil // nothing new
	}

	f, err := os.Open(idx.path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", idx.path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek %s: %w", idx.path, err)
	}

	var prompts []promptfile.Prompt
	consumed := int64(0)
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return 0, fmt.Errorf("read %s: %w", idx.path, readErr)
		}
		if readErr == io.EOF {
			// Leave a trailing line without a newline for the next scan.
			break
		}
		consumed += int64(len(line))

		if trimmed := strings.TrimSpace(line); trimmed != "" {
			p, ok, err := promptfile.Parse(idx.path, []byte(trimmed))
			if err != nil {
				idx.log.Warn("promptindexer: skipping malformed line", "path", idx.path, "error", err)
				continue
			}
			if ok {
				prompts = append(prompts, p)
			}
		}
	}

	if consumed == 0 {
		return 0, nil
	}

	newOffset := offset + consumed
	if err := idx.store.InsertPromptMessages(ctx, idx.path, newOffset, info.ModTime().UnixNano(), prompts); err != nil {
		return 0, err
	}
	return len(prompts), nil
}

// SetupWatcher creates the fsnotify watcher and watches the history file's
// parent directory, without consuming any events yet (see
// indexer.SetupWatcher for why).
func (idx *Indexer) SetupWatcher(ctx context.Context) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("promptindexer: create watcher: %w", err)
	}

	if err := watcher.Add(filepath.Dir(idx.path)); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("promptindexer: watch %s: %w", filepath.Dir(idx.path), err)
	}

	return watcher, nil
}

// WatchLoop runs until ctx is canceled, incrementally re-ingesting the history
// file as it's appended to. watcher comes from a prior call to SetupWatcher;
// WatchLoop closes it before returning.
func (idx *Indexer) WatchLoop(ctx context.Context, watcher *fsnotify.Watcher) error {
	defer func() { _ = watcher.Close() }()

	db := debounce.New(debounceWindow, func(string) {
		if _, err := idx.ingestPath(ctx); err != nil {
			idx.log.Warn("promptindexer: reindex on change failed", "path", idx.path, "error", err)
		}
	})
	defer db.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			idx.log.Warn("promptindexer: watcher error", "error", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Name != idx.path {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				db.Trigger(idx.path)
			}
		}
	}
}

// Watch runs until ctx is canceled, incrementally re-ingesting the history
// file as it changes. It's SetupWatcher followed by WatchLoop.
func (idx *Indexer) Watch(ctx context.Context) error {
	watcher, err := idx.SetupWatcher(ctx)
	if err != nil {
		return err
	}
	return idx.WatchLoop(ctx, watcher)
}
