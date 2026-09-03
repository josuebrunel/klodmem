// Package indexer scans and watches Claude Code's auto-memory directories,
// keeping a Store in sync with the markdown files on disk.
package indexer

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
	"github.com/josuebrunel/klodmem/internal/memoryfile"
	"github.com/josuebrunel/klodmem/internal/store"
)

const (
	memoryDirName  = "memory"
	indexFileName  = "MEMORY.md"
	debounceWindow = 200 * time.Millisecond
)

// Indexer keeps store.Store in sync with markdown memory files under root.
type Indexer struct {
	root  string
	store *store.Store
	log   *slog.Logger
}

// New creates an Indexer that scans/watches root (a directory shaped like
// "~/.claude/projects", containing "<project>/memory/*.md" subtrees).
func New(root string, s *store.Store, log *slog.Logger) *Indexer {
	if log == nil {
		log = slog.Default()
	}
	return &Indexer{root: root, store: s, log: log}
}

// FullScan walks every "<project>/memory/*.md" file under the root, upserting
// changed files and removing DB rows for files that no longer exist on disk.
func (idx *Indexer) FullScan(ctx context.Context) error {
	seen := make(map[string]struct{})

	memoryDirs, err := idx.memoryDirs()
	if err != nil {
		return err
	}

	for _, dir := range memoryDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			idx.log.Warn("indexer: read memory dir failed", "dir", dir, "error", err)
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !isMemoryFile(entry.Name()) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			seen[path] = struct{}{}
			if err := idx.indexFile(ctx, path); err != nil {
				idx.log.Warn("indexer: index file failed", "path", path, "error", err)
			}
		}
	}

	if err := idx.pruneMissing(ctx, seen); err != nil {
		return err
	}

	idx.log.Info("indexer: full scan complete", "files", len(seen))
	return nil
}

// indexFile upserts path into the store if it's new or its mtime changed.
func (idx *Indexer) indexFile(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return idx.store.DeleteFile(ctx, path)
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	mtime := info.ModTime().UnixNano()
	existing, ok, err := idx.store.FileMTime(ctx, path)
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

	m, err := memoryfile.Parse(path, raw, mtime)
	if err != nil {
		return err
	}

	return idx.store.UpsertFile(ctx, m)
}

// pruneMissing deletes indexed rows whose path wasn't found in seen, i.e.
// files that existed in a previous scan but are no longer on disk.
func (idx *Indexer) pruneMissing(ctx context.Context, seen map[string]struct{}) error {
	indexed, err := idx.store.AllPaths(ctx)
	if err != nil {
		return err
	}
	for _, path := range indexed {
		if _, ok := seen[path]; ok {
			continue
		}
		if err := idx.store.DeleteFile(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

// memoryDirs globs "<root>/*/memory" directories that exist.
func (idx *Indexer) memoryDirs() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(idx.root, "*", memoryDirName))
	if err != nil {
		return nil, fmt.Errorf("glob memory dirs under %s: %w", idx.root, err)
	}
	return matches, nil
}

func isMemoryFile(name string) bool {
	return strings.HasSuffix(name, ".md") && name != indexFileName
}

// Watch runs until ctx is canceled, keeping the store in sync as memory files
// are created, modified, or removed, and picking up newly-created project
// directories under root.
func (idx *Indexer) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("indexer: create watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	if err := watcher.Add(idx.root); err != nil {
		return fmt.Errorf("indexer: watch root %s: %w", idx.root, err)
	}

	memoryDirs, err := idx.memoryDirs()
	if err != nil {
		return err
	}
	for _, dir := range memoryDirs {
		if err := watcher.Add(dir); err != nil {
			idx.log.Warn("indexer: watch memory dir failed", "dir", dir, "error", err)
			continue
		}
	}
	watched := make(map[string]struct{}, len(memoryDirs))
	for _, dir := range memoryDirs {
		watched[dir] = struct{}{}
	}

	db := debounce.New(debounceWindow, func(path string) {
		if err := idx.indexFile(ctx, path); err != nil {
			idx.log.Warn("indexer: reindex on change failed", "path", path, "error", err)
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
			idx.log.Warn("indexer: watcher error", "error", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			idx.handleEvent(ctx, watcher, watched, event, db)
		}
	}
}

func (idx *Indexer) handleEvent(ctx context.Context, watcher *fsnotify.Watcher, watched map[string]struct{}, event fsnotify.Event, db *debounce.Debouncer) {
	if event.Op&fsnotify.Create != 0 {
		idx.maybeWatchNewDir(ctx, watcher, watched, event.Name)
	}

	if !isMemoryFile(filepath.Base(event.Name)) {
		return
	}
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
		db.Trigger(event.Name)
	}
}

// maybeWatchNewDir adds a watch on name if it's a newly created project
// directory (a direct child of root) or a "memory" directory inside one —
// the only two directory levels klodmem needs to track. Other subdirectories
// (e.g. per-session scratch dirs alongside memory/) are intentionally left
// unwatched, so the number of inotify watches stays bounded to O(projects)
// rather than growing with every session.
func (idx *Indexer) maybeWatchNewDir(ctx context.Context, watcher *fsnotify.Watcher, watched map[string]struct{}, name string) {
	info, err := os.Stat(name)
	if err != nil || !info.IsDir() {
		return
	}

	isProjectDir := filepath.Dir(name) == idx.root
	isMemoryDir := filepath.Base(name) == memoryDirName
	if !isProjectDir && !isMemoryDir {
		return
	}
	if _, ok := watched[name]; ok {
		return
	}

	if err := watcher.Add(name); err != nil {
		idx.log.Warn("indexer: watch new dir failed", "dir", name, "error", err)
		return
	}
	watched[name] = struct{}{}
	idx.log.Info("indexer: watching new directory", "dir", name)

	// A project dir and its memory/ subdirectory can both be created by a
	// single os.MkdirAll call faster than this loop processes events, so
	// the memory dir's own Create event can arrive before we're watching
	// its parent. Walk name's existing children now to catch anything
	// created in that window instead of waiting for the next full scan.
	entries, err := os.ReadDir(name)
	if err != nil {
		return
	}
	for _, entry := range entries {
		childPath := filepath.Join(name, entry.Name())
		if entry.IsDir() {
			idx.maybeWatchNewDir(ctx, watcher, watched, childPath)
			continue
		}
		if isMemoryDir && isMemoryFile(entry.Name()) {
			if err := idx.indexFile(ctx, childPath); err != nil {
				idx.log.Warn("indexer: catch-up index failed", "path", childPath, "error", err)
			}
		}
	}
}
