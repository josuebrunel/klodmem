package historyindexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

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

// TestFullScanAfterTranscriptRewriteDoesNotDuplicateMessages is a regression
// test for the rewrite path: a transcript rewritten shorter (Claude Code
// rewrites session files when it compacts them) is re-read from byte 0, and
// that used to re-index every message in the file on top of the rows from the
// previous pass.
func TestFullScanAfterTranscriptRewriteDoesNotDuplicateMessages(t *testing.T) {
	idx, s, root, ctx := newTestIndexer(t)
	kept := userLine("s1", "the deploy runs through the cli")
	gone := assistantLine("s1", "this answer is about to be compacted away")
	path := writeTranscript(t, root, "proj-a", "s1", kept+gone)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	// Rewrite the transcript shorter, dropping the last message.
	if err := os.WriteFile(path, []byte(kept), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after rewrite) error: %v", err)
	}

	results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "deploy"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchHistory() = %d results, want 1 (the kept message must be indexed once)", len(results))
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.HistoryMessages != 1 {
		t.Fatalf("Stats().HistoryMessages = %d, want 1 (the dropped message must be gone, the kept one not duplicated)", stats.HistoryMessages)
	}
}

// TestRescanAfterLostOffsetDoesNotDuplicateMessages is a regression test for
// the offset race: two klodmem processes can both read a transcript's offset
// before either commits, and both then index the same span. Clearing the
// stored offset (what the second, stale reader effectively sees) must not
// duplicate messages that are already indexed.
func TestRescanAfterLostOffsetDoesNotDuplicateMessages(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "klodmem.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	idx := New(root, s, nil)

	path := writeTranscript(t, root, "proj-a", "s1",
		userLine("s1", "first question about caching")+assistantLine("s1", "use an lru cache keyed by the request path"))
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(ctx, `DELETE FROM transcript_files WHERE path = ?`, path); err != nil {
		t.Fatalf("clearing the stored offset: %v", err)
	}

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after losing the offset) error: %v", err)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.HistoryMessages != 2 {
		t.Fatalf("Stats().HistoryMessages = %d, want 2 (re-reading an indexed span must not duplicate it)", stats.HistoryMessages)
	}
}

// TestConcurrentFullScansIndexEachMessageOnce covers the same race from the
// other side: two klodmem processes (each with its own connection) scanning
// the same transcript at the same time must leave exactly one row per message,
// whichever of them wins.
func TestConcurrentFullScansIndexEachMessageOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "klodmem.db")

	open := func() (*store.Store, *Indexer) {
		s, err := store.Open(ctx, dbPath)
		if err != nil {
			t.Fatalf("store.Open() error: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s, New(root, s, nil)
	}
	s1, idx1 := open()
	_, idx2 := open()
	t.Cleanup(func() { _ = idx2.store.Close() })

	writeTranscript(t, root, "proj-a", "s1",
		userLine("s1", "first question about caching")+
			assistantLine("s1", "use an lru cache keyed by the request path")+
			userLine("s1", "second question about caching"))

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, idx := range []*Indexer{idx1, idx2} {
		wg.Add(1)
		go func(idx *Indexer) {
			defer wg.Done()
			errs <- idx.FullScan(ctx)
		}(idx)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent FullScan() error: %v", err)
		}
	}

	stats, err := s1.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.HistoryMessages != 3 {
		t.Fatalf("Stats().HistoryMessages = %d, want 3 (one per message, not one per process)", stats.HistoryMessages)
	}
}

// TestResetHistoryThenFullScanClearsPreexistingDuplicates covers the upgrade
// path -reset exists for: an index written by a version older than the
// duplicate fix holds the same message several times, and a reset plus a scan
// has to leave exactly one row per message.
func TestResetHistoryThenFullScanClearsPreexistingDuplicates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "klodmem.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	idx := New(root, s, nil)

	path := writeTranscript(t, root, "proj-a", "s1",
		userLine("s1", "first question about caching")+assistantLine("s1", "use an lru cache keyed by the request path"))
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	// Write the first message a second time, straight into the FTS table the
	// way a pre-fix binary did, bypassing the identity table.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO history_fts (path, session_id, timestamp, project, role, text)
		VALUES (?, ?, ?, ?, ?, ?)
	`, path, "s1", "t", "proj-a", "user", "first question about caching"); err != nil {
		t.Fatalf("seeding a duplicate row: %v", err)
	}

	before, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if before.HistoryMessages != 3 {
		t.Fatalf("Stats().HistoryMessages = %d before the reset, want 3 (two messages plus the seeded duplicate)", before.HistoryMessages)
	}

	if _, err := s.ResetHistory(ctx); err != nil {
		t.Fatalf("ResetHistory() error: %v", err)
	}
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after reset) error: %v", err)
	}

	after, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if after.HistoryMessages != 2 {
		t.Fatalf("Stats().HistoryMessages = %d after reset and re-index, want 2", after.HistoryMessages)
	}
	results, err := s.SearchHistory(ctx, store.HistorySearchQuery{Query: "caching"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("SearchHistory() after reset and re-index = %d results, want 2", len(results))
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
