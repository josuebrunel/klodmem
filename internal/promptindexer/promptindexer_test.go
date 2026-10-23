package promptindexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/josuebrunel/klodmem/internal/store"
)

func promptLine(sessionID, text string) string {
	return `{"display":"` + text + `","pastedContents":{},"timestamp":1784946759607,"project":"/home/u/workspace/app","sessionId":"` + sessionID + `"}` + "\n"
}

func newTestIndexer(t *testing.T) (*Indexer, *store.Store, string, context.Context) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "klodmem.db"))
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(path, s, nil), s, path, ctx
}

func writeHistory(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
}

func appendHistory(t *testing.T, path, content string) {
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

func TestFullScanIndexesPrompts(t *testing.T) {
	idx, s, path, ctx := newTestIndexer(t)
	writeHistory(t, path, promptLine("s1", "why does the build fail"))

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	results, err := s.SearchPrompts(ctx, store.PromptSearchQuery{Query: "build"})
	if err != nil {
		t.Fatalf("SearchPrompts() error: %v", err)
	}
	if len(results) != 1 || results[0].Project != "-home-u-workspace-app" || results[0].SessionID != "s1" {
		t.Fatalf("SearchPrompts() = %+v, want one hit from s1", results)
	}
}

func TestFullScanIsIncrementalNotDuplicated(t *testing.T) {
	idx, s, path, ctx := newTestIndexer(t)
	writeHistory(t, path, promptLine("s1", "first question about caching"))

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	appendHistory(t, path, promptLine("s1", "second question about caching"))
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (second pass) error: %v", err)
	}

	results, err := s.SearchPrompts(ctx, store.PromptSearchQuery{Query: "caching"})
	if err != nil {
		t.Fatalf("SearchPrompts() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("SearchPrompts() = %d results, want 2", len(results))
	}

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (no changes) error: %v", err)
	}
	results, _ = s.SearchPrompts(ctx, store.PromptSearchQuery{Query: "caching"})
	if len(results) != 2 {
		t.Fatalf("SearchPrompts() after no-op scan = %d results, want 2", len(results))
	}
}

func TestFullScanLeavesUnterminatedLineForNextScan(t *testing.T) {
	idx, s, path, ctx := newTestIndexer(t)
	writeHistory(t, path, promptLine("s1", "complete prompt"))
	appendHistory(t, path, `{"display":"partial prom`)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	results, err := s.SearchPrompts(ctx, store.PromptSearchQuery{Query: "complete"})
	if err != nil {
		t.Fatalf("SearchPrompts() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchPrompts() = %d results, want 1 (partial line must not be indexed)", len(results))
	}

	appendHistory(t, path, "pt\"}\n")
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after completing line) error: %v", err)
	}
	results, _ = s.SearchPrompts(ctx, store.PromptSearchQuery{Query: "partial"})
	if len(results) != 1 {
		t.Fatalf("SearchPrompts() after completing line = %d results, want 1", len(results))
	}
}

func TestFullScanPrunesDeletedHistory(t *testing.T) {
	idx, s, path, ctx := newTestIndexer(t)
	writeHistory(t, path, promptLine("s1", "ephemeral prompt content"))

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	if _, ok, err := s.PromptOffset(ctx, path); err != nil || !ok {
		t.Fatalf("expected history indexed before deletion, ok=%v err=%v", ok, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after delete) error: %v", err)
	}
	results, err := s.SearchPrompts(ctx, store.PromptSearchQuery{Query: "ephemeral"})
	if err != nil {
		t.Fatalf("SearchPrompts() error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("SearchPrompts() after prune = %d results, want 0", len(results))
	}
}

func TestFullScanAfterRewriteDoesNotDuplicate(t *testing.T) {
	idx, s, path, ctx := newTestIndexer(t)
	kept := promptLine("s1", "the deploy runs through the cli")
	gone := promptLine("s1", "this prompt is about to be compacted away")
	writeHistory(t, path, kept+gone)

	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}
	writeHistory(t, path, kept)
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() (after rewrite) error: %v", err)
	}

	results, err := s.SearchPrompts(ctx, store.PromptSearchQuery{Query: "deploy"})
	if err != nil {
		t.Fatalf("SearchPrompts() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchPrompts() = %d results, want 1", len(results))
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.PromptMessages != 1 {
		t.Fatalf("Stats().PromptMessages = %d, want 1", stats.PromptMessages)
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
	idx, s, path, ctx := newTestIndexer(t)
	writeHistory(t, path, promptLine("s1", "initial prompt"))
	if err := idx.FullScan(ctx); err != nil {
		t.Fatalf("FullScan() error: %v", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- idx.Watch(watchCtx) }()

	time.Sleep(50 * time.Millisecond)
	appendHistory(t, path, promptLine("s1", "appended while watching"))

	waitFor(t, 3*time.Second, func() bool {
		results, err := s.SearchPrompts(ctx, store.PromptSearchQuery{Query: "appended"})
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
