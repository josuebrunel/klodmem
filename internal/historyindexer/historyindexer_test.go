package historyindexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/josuebrunel/klodmem/internal/store"
)

func userLine(sessionID, text string) string {
	return `{"type":"user","sessionId":"` + sessionID + `","timestamp":"t","message":{"role":"user","content":"` + text + `"}}` + "\n"
}

func assistantLine(sessionID, text string) string {
	return `{"type":"assistant","sessionId":"` + sessionID + `","timestamp":"t","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
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

func writeTranscript(t *testing.T, root, project, sessionID, content string) string {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	return path
}

func appendTranscript(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile() error: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("WriteString() error: %v", err)
	}
}

func TestFullScanIndexesMessages(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	writeTranscript(t, root, "proj-a", "s1", userLine("s1", "why does the build fail")+assistantLine("s1", "missing dependency in go.mod"))

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "dependency"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 || results[0].Project != "proj-a" || results[0].SessionID != "s1" {
		t.Fatalf("SearchHistory() = %+v, want one hit from proj-a/s1", results)
	}
}

func TestFullScanIsIncrementalNotDuplicated(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	path := writeTranscript(t, root, "proj-a", "s1", userLine("s1", "first question about caching"))

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	appendTranscript(t, path, userLine("s1", "second question about caching"))
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (second pass) error: %v", err)
	}

	results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "caching"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("SearchHistory() = %d results, want 2 (no duplicates, both spans indexed)", len(results))
	}

	// A third scan with no new bytes must not error or duplicate anything.
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (third pass, no changes) error: %v", err)
	}
	results, err = s.SearchHistory(ctx, store.HistorySearchQuery{Query: "caching"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("SearchHistory() after no-op scan = %d results, want 2", len(results))
	}
}

func TestFullScanLeavesUnterminatedLineForNextScan(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	completeLine := userLine("s1", "complete message")
	path := writeTranscript(t, root, "proj-a", "s1", completeLine)
	// Append a line with no trailing newline yet, simulating a write in progress.
	appendTranscript(t, path, `{"type":"user","sessionId":"s1","message":{"role":"user","content":"partial mess`)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "complete"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchHistory() = %d results, want 1 (partial line must not be indexed yet)", len(results))
	}

	// Finish the line; the next scan should pick up exactly that one message.
	appendTranscript(t, path, "age content\"}}\n")
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after completing line) error: %v", err)
	}
	results, err = s.SearchHistory(ctx, store.HistorySearchQuery{Query: "partial"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchHistory() after completing line = %d results, want 1", len(results))
	}
}

func TestFullScanPrunesDeletedTranscripts(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	path := writeTranscript(t, root, "proj-a", "s1", userLine("s1", "ephemeral session content"))

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	if _, ok, err := s.TranscriptOffset(ctx, path); err != nil || !ok {
		t.Fatalf("expected transcript indexed before deletion, ok=%v err=%v", ok, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after delete) error: %v", err)
	}

	if _, ok, err := s.TranscriptOffset(ctx, path); err != nil || ok {
		t.Fatalf("expected transcript pruned after deletion, ok=%v err=%v", ok, err)
	}
	results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "ephemeral"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("SearchHistory() after prune = %d results, want 0", len(results))
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

func TestWatchIndexesAppendedLines(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	path := writeTranscript(t, root, "proj-a", "s1", userLine("s1", "initial line"))
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- idx.Watch(watchCtx) }()

	time.Sleep(50 * time.Millisecond)
	appendTranscript(t, path, userLine("s1", "appended while watching"))

	waitFor(t, 3*time.Second, func() bool {
		results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "appended"})
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
// it proves that once SetupWatcher has registered a watch, a transcript
// append that happens before WatchLoop starts consuming events isn't lost —
// the OS queues it and WatchLoop picks it up as soon as it starts running.
func TestSetupWatcherCatchesEventsBeforeWatchLoopStarts(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	path := writeTranscript(t, root, "proj-a", "s1", userLine("s1", "initial line"))

	watcher, watched, err := idx.SetupWatcher(ctx)
	if err != nil {
		t.Fatalf("SetupWatcher() error: %v", err)
	}

	// Simulate an append happening while a caller is busy elsewhere (e.g. a
	// slow initial FullScan) before WatchLoop has started running.
	appendTranscript(t, path, assistantLine("s1", "missing dependency in go.mod"))
	time.Sleep(100 * time.Millisecond)

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- idx.WatchLoop(watchCtx, watcher, watched) }()

	waitFor(t, 3*time.Second, func() bool {
		results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "dependency"})
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

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- idx.Watch(watchCtx) }()

	time.Sleep(50 * time.Millisecond)
	writeTranscript(t, root, "brand-new-proj", "s9", userLine("s9", "hello from a brand new project"))

	waitFor(t, 3*time.Second, func() bool {
		results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "brand"})
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
