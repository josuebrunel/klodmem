// Package planindexer scans and watches Claude Code's Plan Mode directory
// (~/.claude/plans), keeping a Store in sync with its markdown files.
package planindexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/josuebrunel/klodmem/internal/debounce"
	"github.com/josuebrunel/klodmem/internal/planfile"
	"github.com/josuebrunel/klodmem/internal/store"
)

const debounceWindow = 200 * time.Millisecond

// Indexer keeps a Store in sync with plan markdown files under root.
type Indexer struct {
	root  string
	store *store.Store
	log   *slog.Logger
}

// New creates an Indexer that scans/watches root (a directory shaped like
// "~/.claude/plans", containing "*.md" files).
func New(root string, s *store.Store, log *slog.Logger) *Indexer {
	if log == nil {
		log = slog.Default()
	}
	return &Indexer{root: root, store: s, log: log}
}

// FullScan walks every "*.md" file under root, upserting changed files and
// removing DB rows for files that no longer exist on disk.
func (idx *Indexer) FullScan(ctx context.Context) error {
	seen := make(map[string]struct{})

	paths, err := idx.planPaths()
	if err != nil {
		return err
	}

	for _, path := range paths {
		seen[path] = struct{}{}
		if err := idx.indexFile(ctx, path); err != nil {
			idx.log.Warn("planindexer: index file failed", "path", path, "error", err)
		}
	}

	if err := idx.pruneMissing(ctx, seen); err != nil {
		return err
	}

	idx.log.Info("planindexer: full scan complete", "files", len(seen))
	return nil
}

// indexFile upserts path into the store if it's new or its mtime changed.
func (idx *Indexer) indexFile(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return idx.store.DeletePlan(ctx, path)
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	mtime := info.ModTime().UnixNano()
	existing, ok, err := idx.store.PlanMTime(ctx, path)
	if err != nil {
		return err
	}
	if ok && existing == mtime {
		return nil // unchanged
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	return idx.store.UpsertPlan(ctx, planfile.Parse(path, raw, mtime))
}

// pruneMissing deletes indexed rows whose path wasn't found in seen.
func (idx *Indexer) pruneMissing(ctx context.Context, seen map[string]struct{}) error {
	indexed, err := idx.store.AllPlanPaths(ctx)
	if err != nil {
		return err
	}
	for _, path := range indexed {
		if _, ok := seen[path]; ok {
			continue
		}
		if err := idx.store.DeletePlan(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

// planPaths globs "<root>/*.md".
func (idx *Indexer) planPaths() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(idx.root, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob plan files under %s: %w", idx.root, err)
	}
	return matches, nil
}

// SetupWatcher creates the fsnotify watcher and registers a watch on root,
// without consuming any events yet (see indexer.SetupWatcher for why). A
// missing root is not an error: Claude Code creates ~/.claude/plans lazily, so
// WatchLoop retries adding the watch until the directory appears.
func (idx *Indexer) SetupWatcher(ctx context.Context) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("planindexer: create watcher: %w", err)
	}

	if err := watcher.Add(idx.root); err != nil {
		if _, statErr := os.Stat(idx.root); statErr == nil {
			_ = watcher.Close()
			return nil, fmt.Errorf("planindexer: watch root %s: %w", idx.root, err)
		}
		idx.log.Warn("planindexer: plans directory missing at startup, will retry", "dir", idx.root)
	}

	return watcher, nil
}

// WatchLoop runs until ctx is canceled, keeping the store in sync as plan
// files are created, modified, or removed. watcher comes from a prior call to
// SetupWatcher; WatchLoop closes it before returning. It also retries adding
// the root watch periodically, so a plans directory that does not exist yet at
// startup is picked up once Claude Code creates it.
func (idx *Indexer) WatchLoop(ctx context.Context, watcher *fsnotify.Watcher) error {
	defer func() { _ = watcher.Close() }()

	db := debounce.New(debounceWindow, func(path string) {
		if err := idx.indexFile(ctx, path); err != nil {
			idx.log.Warn("planindexer: reindex on change failed", "path", path, "error", err)
		}
	})
	defer db.Stop()

	// Retry adding the root watch until the plans directory exists. Add on an
	// already-watched path is a no-op, so this can run unconditionally.
	retry := time.NewTicker(5 * time.Second)
	defer retry.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-retry.C:
			if _, err := os.Stat(idx.root); err == nil {
				if err := watcher.Add(idx.root); err != nil {
					idx.log.Debug("planindexer: root watch retry failed", "dir", idx.root, "error", err)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			idx.log.Warn("planindexer: watcher error", "error", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			idx.handleEvent(event, db)
		}
	}
}

// Watch runs until ctx is canceled, keeping the store in sync. It's
// SetupWatcher followed by WatchLoop.
func (idx *Indexer) Watch(ctx context.Context) error {
	watcher, err := idx.SetupWatcher(ctx)
	if err != nil {
		return err
	}
	return idx.WatchLoop(ctx, watcher)
}

func (idx *Indexer) handleEvent(event fsnotify.Event, db *debounce.Debouncer) {
	if !strings.HasSuffix(event.Name, ".md") {
		return // ignore the root directory itself and anything else
	}
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
		db.Trigger(event.Name)
	}
}
