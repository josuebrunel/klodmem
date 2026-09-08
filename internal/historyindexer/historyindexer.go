// Package historyindexer scans and watches Claude Code's session transcript
// files (top-level "<project>/<session-id>.jsonl"), incrementally indexing
// newly-appended lines into a Store's conversation-history table. Subagent
// transcripts (nested under "<project>/<session-id>/subagents/") are
// deliberately out of scope for now.
package historyindexer

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
	"github.com/josuebrunel/klodmem/internal/store"
	"github.com/josuebrunel/klodmem/internal/transcript"
)

const debounceWindow = 500 * time.Millisecond

// Indexer keeps a Store's conversation-history table in sync with session
// transcript files under root.
type Indexer struct {
	root  string
	store *store.Store
	log   *slog.Logger
}

// New creates an Indexer that scans/watches root (a directory shaped like
// "~/.claude/projects", containing "<project>/<session-id>.jsonl" files).
func New(root string, s *store.Store, log *slog.Logger) *Indexer {
	if log == nil {
		log = slog.Default()
	}
	return &Indexer{root: root, store: s, log: log}
}

// FullScan incrementally parses every "<project>/<session-id>.jsonl" file
// under root (only newly-appended bytes since the last scan) and removes
// indexed history for transcript files that no longer exist on disk.
func (idx *Indexer) FullScan(ctx context.Context) error {
	seen := make(map[string]struct{})

	paths, err := idx.transcriptPaths()
	if err != nil {
		return err
	}

	total := 0
	for _, path := range paths {
		seen[path] = struct{}{}
		n, err := idx.ingestPath(ctx, path)
		if err != nil {
			idx.log.Warn("historyindexer: ingest failed", "path", path, "error", err)
			continue
		}
		total += n
	}

	if err := idx.pruneMissing(ctx, seen); err != nil {
		return err
	}

	idx.log.Info("historyindexer: full scan complete", "files", len(seen), "messages", total)
	return nil
}

// ingestPath reads any bytes appended to path since the last recorded offset,
// parses complete lines, and stores newly-found messages. It returns the
// number of messages indexed. If path no longer exists, any indexed history
// for it is removed instead.
func (idx *Indexer) ingestPath(ctx context.Context, path string) (int, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, idx.store.DeleteTranscript(ctx, path)
	}
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}

	offset, _, err := idx.store.TranscriptOffset(ctx, path)
	if err != nil {
		return 0, err
	}
	if offset > info.Size() {
		// File was truncated or replaced (e.g. a rare rewrite) — start over.
		offset = 0
	}
	if offset == info.Size() {
		return 0, nil // nothing new
	}

	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek %s: %w", path, err)
	}

	project := filepath.Base(filepath.Dir(path))
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")

	var msgs []transcript.Message
	consumed := int64(0)
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return 0, fmt.Errorf("read %s: %w", path, readErr)
		}
		if readErr == io.EOF {
			// A trailing line with no newline yet means the writer hasn't
			// finished it — leave it for the next scan.
			break
		}
		consumed += int64(len(line))

		if trimmed := strings.TrimSpace(line); trimmed != "" {
			msg, ok, err := transcript.Parse(project, []byte(trimmed))
			if err != nil {
				idx.log.Warn("historyindexer: skipping malformed line", "path", path, "error", err)
				continue
			}
			if ok {
				msgs = append(msgs, msg)
			}
		}
	}

	if consumed == 0 {
		return 0, nil
	}

	newOffset := offset + consumed
	if err := idx.store.InsertHistoryMessages(ctx, path, project, sessionID, newOffset, info.ModTime().UnixNano(), msgs); err != nil {
		return 0, err
	}
	return len(msgs), nil
}

// pruneMissing deletes indexed history for transcript files that were
// indexed in a previous scan but are no longer on disk.
func (idx *Indexer) pruneMissing(ctx context.Context, seen map[string]struct{}) error {
	indexed, err := idx.store.AllTranscriptPaths(ctx)
	if err != nil {
		return err
	}
	for _, path := range indexed {
		if _, ok := seen[path]; ok {
			continue
		}
		if err := idx.store.DeleteTranscript(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

// transcriptPaths globs "<root>/*/*.jsonl" — top-level session transcripts
// only, one level under root, excluding nested subagent transcripts.
func (idx *Indexer) transcriptPaths() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(idx.root, "*", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("glob transcript files under %s: %w", idx.root, err)
	}
	return matches, nil
}

// projectDirs globs the direct project-directory children of root.
func (idx *Indexer) projectDirs() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(idx.root, "*"))
	if err != nil {
		return nil, fmt.Errorf("glob project dirs under %s: %w", idx.root, err)
	}
	var dirs []string
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() {
			dirs = append(dirs, m)
		}
	}
	return dirs, nil
}

// SetupWatcher creates the fsnotify watcher and registers watches on root
// and its existing project directories, without consuming any events yet.
// It's split out from WatchLoop so a caller can register watches *before*
// running an initial FullScan: the OS queues events for an already-
// registered watch (independent of whether anything is reading
// watcher.Events yet), so running SetupWatcher first closes the race where a
// transcript appended to during the scan would otherwise go unnoticed until
// the next restart.
func (idx *Indexer) SetupWatcher(ctx context.Context) (*fsnotify.Watcher, map[string]struct{}, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, fmt.Errorf("historyindexer: create watcher: %w", err)
	}

	if err := watcher.Add(idx.root); err != nil {
		_ = watcher.Close()
		return nil, nil, fmt.Errorf("historyindexer: watch root %s: %w", idx.root, err)
	}

	dirs, err := idx.projectDirs()
	if err != nil {
		_ = watcher.Close()
		return nil, nil, err
	}
	watched := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if err := watcher.Add(dir); err != nil {
			idx.log.Warn("historyindexer: watch project dir failed", "dir", dir, "error", err)
			continue
		}
		watched[dir] = struct{}{}
	}

	return watcher, watched, nil
}

// WatchLoop runs until ctx is canceled, incrementally re-ingesting
// transcript files as they're appended to, and picking up newly-created
// project directories under root. watcher and watched come from a prior call
// to SetupWatcher; WatchLoop closes watcher before returning.
func (idx *Indexer) WatchLoop(ctx context.Context, watcher *fsnotify.Watcher, watched map[string]struct{}) error {
	defer func() { _ = watcher.Close() }()

	db := debounce.New(debounceWindow, func(path string) {
		if _, err := idx.ingestPath(ctx, path); err != nil {
			idx.log.Warn("historyindexer: reindex on change failed", "path", path, "error", err)
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
			idx.log.Warn("historyindexer: watcher error", "error", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			idx.handleEvent(ctx, watcher, watched, event, db)
		}
	}
}

// Watch runs until ctx is canceled, incrementally re-ingesting transcript
// files as they're appended to, and picking up newly-created project
// directories under root. It's SetupWatcher followed by WatchLoop; callers
// that want to avoid missing changes during an initial FullScan should call
// those two steps separately instead (see SetupWatcher).
func (idx *Indexer) Watch(ctx context.Context) error {
	watcher, watched, err := idx.SetupWatcher(ctx)
	if err != nil {
		return err
	}
	return idx.WatchLoop(ctx, watcher, watched)
}

func (idx *Indexer) handleEvent(ctx context.Context, watcher *fsnotify.Watcher, watched map[string]struct{}, event fsnotify.Event, db *debounce.Debouncer) {
	if event.Op&fsnotify.Create != 0 {
		idx.maybeWatchNewProjectDir(ctx, watcher, watched, event.Name)
	}

	if !strings.HasSuffix(event.Name, ".jsonl") {
		return
	}
	if _, ok := watched[filepath.Dir(event.Name)]; !ok {
		return // event for a nested file (e.g. a subagent transcript) we don't track
	}
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
		db.Trigger(event.Name)
	}
}

// maybeWatchNewProjectDir adds a watch on name if it's a newly created
// direct child of root, then does a one-off catch-up scan of any .jsonl
// files already inside it — a project dir and its first session file can be
// created faster than this loop processes events.
func (idx *Indexer) maybeWatchNewProjectDir(ctx context.Context, watcher *fsnotify.Watcher, watched map[string]struct{}, name string) {
	info, err := os.Stat(name)
	if err != nil || !info.IsDir() {
		return
	}
	if filepath.Dir(name) != idx.root {
		return
	}
	if _, ok := watched[name]; ok {
		return
	}

	if err := watcher.Add(name); err != nil {
		idx.log.Warn("historyindexer: watch new project dir failed", "dir", name, "error", err)
		return
	}
	watched[name] = struct{}{}
	idx.log.Info("historyindexer: watching new project directory", "dir", name)

	matches, err := filepath.Glob(filepath.Join(name, "*.jsonl"))
	if err != nil {
		return
	}
	for _, path := range matches {
		if _, err := idx.ingestPath(ctx, path); err != nil {
			idx.log.Warn("historyindexer: catch-up ingest failed", "path", path, "error", err)
		}
	}
}
