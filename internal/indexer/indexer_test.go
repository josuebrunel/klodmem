package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/josuebrunel/klodmem/internal/store"
)

const sampleMemory = `---
name: testing-style
description: prefers table-driven tests
metadata:
  type: feedback
---

Use table-driven tests. **Why:** matches existing suite.
`

func writeMemoryFile(t *testing.T, root, project, filename, content string) string {
	t.Helper()
	dir := filepath.Join(root, project, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	return path
}

func newTestIndexer(t *testing.T) (*Indexer, *store.Store, string, context.Context) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "klodmem.db"))
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(root, s, nil), s, root, ctx
}

func TestFullScanIndexesFiles(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	writeMemoryFile(t, root, "proj-a", "testing_style.md", sampleMemory)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	results, err := s.Search(ctx, store.SearchQuery{Query: "table-driven"})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() = %d results, want 1", len(results))
	}
	if results[0].Project != "proj-a" {
		t.Fatalf("Search()[0].Project = %s, want proj-a", results[0].Project)
	}
}

func TestFullScanSkipsMemoryIndexFile(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	writeMemoryFile(t, root, "proj-a", "MEMORY.md", "- [testing style](testing_style.md) — hook\n")

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	paths, err := s.AllPaths(ctx)
	if err != nil {
		t.Fatalf("AllPaths() error: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("AllPaths() = %v, want empty (MEMORY.md should be excluded)", paths)
	}
}

func TestFullScanPrunesDeletedFiles(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	path := writeMemoryFile(t, root, "proj-a", "testing_style.md", sampleMemory)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	if _, ok, err := s.FileMTime(ctx, path); err != nil || !ok {
		t.Fatalf("expected file indexed before deletion, ok=%v err=%v", ok, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after delete) error: %v", err)
	}

	if _, ok, err := s.FileMTime(ctx, path); err != nil || ok {
		t.Fatalf("expected file pruned after deletion, ok=%v err=%v", ok, err)
	}
}

func TestFullScanSkipsUnchangedFiles(t *testing.T) {
	idx, _, root, ctx := newTestIndexer(t)
	writeMemoryFile(t, root, "proj-a", "testing_style.md", sampleMemory)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	// Second scan with no changes on disk should be a no-op (exercised for
	// coverage of the mtime-skip path; correctness is that it doesn't error).
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (second pass) error: %v", err)
	}
}

// TestResetMemoryThenFullScanRebuildsTheIndex covers the memory side of the
// reason -reset exists: it drops the file records, and the next scan puts them
// back from the markdown, which klodmem only ever reads.
func TestResetMemoryThenFullScanRebuildsTheIndex(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	writeMemoryFile(t, root, "proj-a", "testing_style.md", sampleMemory)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	if stats, err := s.Stats(ctx); err != nil || stats.MemoryFiles != 1 {
		t.Fatalf("Stats().MemoryFiles before the reset = %d (err=%v), want 1", stats.MemoryFiles, err)
	}

	if _, err := s.ResetMemory(ctx); err != nil {
		t.Fatalf("ResetMemory() error: %v", err)
	}
	if stats, err := s.Stats(ctx); err != nil || stats.MemoryFiles != 0 {
		t.Fatalf("Stats().MemoryFiles after the reset = %d (err=%v), want 0", stats.MemoryFiles, err)
	}

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after reset) error: %v", err)
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.MemoryFiles != 1 {
		t.Fatalf("Stats().MemoryFiles after reset and re-index = %d, want 1", stats.MemoryFiles)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestWatchIndexesNewFileInExistingProject(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	writeMemoryFile(t, root, "proj-a", "existing.md", sampleMemory)
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- idx.Watch(watchCtx) }()

	// Give the watcher a moment to register its watches before writing.
	time.Sleep(50 * time.Millisecond)
	const secondMemory = `---
name: new-one
description: a second memory
metadata:
  type: project
---

Body of the second memory.
`
	writeMemoryFile(t, root, "proj-a", "new_one.md", secondMemory)

	waitFor(t, 3*time.Second, func() bool {
		results, err := s.Search(ctx, store.SearchQuery{Query: "second"})
		return err == nil && len(results) == 1
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch() did not return after context cancellation")
	}
}

// TestSetupWatcherCatchesEventsBeforeWatchLoopStarts is a regression test
// for the startup race between an initial FullScan and Watch registration:
// it proves that once SetupWatcher has registered a watch, a change that
// happens before WatchLoop starts consuming events isn't lost — the OS
// queues it and WatchLoop picks it up as soon as it starts running.
func TestSetupWatcherCatchesEventsBeforeWatchLoopStarts(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	writeMemoryFile(t, root, "proj-a", "existing.md", sampleMemory)

	watcher, watched, err := idx.SetupWatcher(ctx)
	if err != nil {
		t.Fatalf("SetupWatcher() error: %v", err)
	}

	// Simulate a change happening while a caller is busy elsewhere (e.g. a
	// slow initial FullScan) before WatchLoop has started running.
	const secondMemory = `---
name: new-one
description: a second memory
metadata:
  type: project
---

Body of the second memory.
`
	writeMemoryFile(t, root, "proj-a", "new_one.md", secondMemory)
	time.Sleep(100 * time.Millisecond)

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- idx.WatchLoop(watchCtx, watcher, watched) }()

	waitFor(t, 3*time.Second, func() bool {
		results, err := s.Search(ctx, store.SearchQuery{Query: "second"})
		return err == nil && len(results) == 1
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchLoop() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchLoop() did not return after context cancellation")
	}
}

func TestWatchDiscoversNewProjectDirectory(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	// No projects exist yet at Watch() start time.

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- idx.Watch(watchCtx) }()

	time.Sleep(50 * time.Millisecond)
	writeMemoryFile(t, root, "brand-new-proj", "note.md", `---
name: note
description: created after watch started
metadata:
  type: reference
---

Freshly discovered content.
`)

	waitFor(t, 3*time.Second, func() bool {
		results, err := s.Search(ctx, store.SearchQuery{Query: "freshly"})
		return err == nil && len(results) == 1
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch() did not return after context cancellation")
	}
}
