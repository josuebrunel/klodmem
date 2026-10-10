package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josuebrunel/klodmem/internal/memoryfile"
	"github.com/josuebrunel/klodmem/internal/transcript"
)

func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "klodmem.db")
	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, ctx
}

func TestUpsertAndFileMTime(t *testing.T) {
	s, ctx := openTestStore(t)

	m := memoryfile.Memory{
		Path:        "/proj/memory/testing_style.md",
		Project:     "proj",
		Name:        "testing-style",
		Type:        "feedback",
		Description: "prefers table-driven tests",
		Content:     "Use table-driven tests for everything.",
		MTime:       100,
	}

	if err := s.UpsertFile(ctx, m); err != nil {
		t.Fatalf("UpsertFile() error: %v", err)
	}

	mtime, ok, err := s.FileMTime(ctx, m.Path)
	if err != nil {
		t.Fatalf("FileMTime() error: %v", err)
	}
	if !ok {
		t.Fatalf("FileMTime() ok = false, want true")
	}
	if mtime != m.MTime {
		t.Fatalf("FileMTime() = %d, want %d", mtime, m.MTime)
	}

	// Re-upsert with a new mtime should replace, not duplicate.
	m.MTime = 200
	m.Content = "Updated content about table-driven tests."
	if err := s.UpsertFile(ctx, m); err != nil {
		t.Fatalf("UpsertFile() (update) error: %v", err)
	}
	mtime, _, err = s.FileMTime(ctx, m.Path)
	if err != nil {
		t.Fatalf("FileMTime() error: %v", err)
	}
	if mtime != 200 {
		t.Fatalf("FileMTime() after update = %d, want 200", mtime)
	}
}

func TestAllPaths(t *testing.T) {
	s, ctx := openTestStore(t)

	paths, err := s.AllPaths(ctx)
	if err != nil {
		t.Fatalf("AllPaths() error: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("AllPaths() on empty store = %v, want empty", paths)
	}

	want := []string{"/proj-a/memory/one.md", "/proj-b/memory/two.md"}
	for _, p := range want {
		if err := s.UpsertFile(ctx, memoryfile.Memory{
			Path: p, Project: "proj", Name: "n", Type: "project", Description: "d", Content: "c", MTime: 1,
		}); err != nil {
			t.Fatalf("UpsertFile(%s) error: %v", p, err)
		}
	}

	paths, err = s.AllPaths(ctx)
	if err != nil {
		t.Fatalf("AllPaths() error: %v", err)
	}
	if !sameElements(paths, want) {
		t.Fatalf("AllPaths() = %v, want %v", paths, want)
	}
}

func TestAllTranscriptPaths(t *testing.T) {
	s, ctx := openTestStore(t)

	paths, err := s.AllTranscriptPaths(ctx)
	if err != nil {
		t.Fatalf("AllTranscriptPaths() error: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("AllTranscriptPaths() on empty store = %v, want empty", paths)
	}

	want := []string{"/proj-a/s1.jsonl", "/proj-b/s2.jsonl"}
	msgs := []transcript.Message{{Project: "proj-a", SessionID: "s1", Role: "user", Text: "hi", Timestamp: "t1"}}
	if err := s.InsertHistoryMessages(ctx, want[0], "proj-a", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}
	if err := s.InsertHistoryMessages(ctx, want[1], "proj-b", "s2", 100, 1, nil); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	paths, err = s.AllTranscriptPaths(ctx)
	if err != nil {
		t.Fatalf("AllTranscriptPaths() error: %v", err)
	}
	if !sameElements(paths, want) {
		t.Fatalf("AllTranscriptPaths() = %v, want %v", paths, want)
	}
}

func TestResetMemoryClearsOnlyTheMemoryIndex(t *testing.T) {
	s, ctx := openTestStore(t)

	if err := s.UpsertFile(ctx, memoryfile.Memory{
		Path: "/proj/memory/style.md", Project: "proj", Name: "style", Type: "feedback",
		Description: "prefers table-driven tests", Content: "Use table-driven tests.", MTime: 100,
	}); err != nil {
		t.Fatalf("UpsertFile() error: %v", err)
	}
	msgs := []transcript.Message{{Project: "proj", SessionID: "s1", Role: "user", Text: "why is the build failing", Timestamp: "t1"}}
	if err := s.InsertHistoryMessages(ctx, "/proj/s1.jsonl", "proj", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	reports, err := s.ResetMemory(ctx)
	if err != nil {
		t.Fatalf("ResetMemory() error: %v", err)
	}
	if len(reports) != 2 || reports[0].Table != "files" || reports[0].Rows != 1 {
		t.Fatalf("ResetMemory() reports = %+v, want one cleared row in files and one in memory_fts", reports)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.MemoryFiles != 0 {
		t.Fatalf("Stats().MemoryFiles = %d after reset, want 0", stats.MemoryFiles)
	}
	if stats.HistoryMessages != 1 {
		t.Fatalf("Stats().HistoryMessages = %d after a memory-only reset, want 1 (history must be untouched)", stats.HistoryMessages)
	}

	// The memory index has to be usable again, or a reset would be a one-way trip.
	if err := s.UpsertFile(ctx, memoryfile.Memory{
		Path: "/proj/memory/style.md", Project: "proj", Name: "style", Type: "feedback",
		Description: "prefers table-driven tests", Content: "Use table-driven tests.", MTime: 100,
	}); err != nil {
		t.Fatalf("UpsertFile() (after reset) error: %v", err)
	}
	results, err := s.Search(ctx, SearchQuery{Query: "table-driven"})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() after reset and re-index = %d results, want 1", len(results))
	}
}

func TestResetHistoryClearsOnlyTheHistoryIndex(t *testing.T) {
	s, ctx := openTestStore(t)

	if err := s.UpsertFile(ctx, memoryfile.Memory{
		Path: "/proj/memory/style.md", Project: "proj", Name: "style", Type: "feedback",
		Description: "prefers table-driven tests", Content: "Use table-driven tests.", MTime: 100,
	}); err != nil {
		t.Fatalf("UpsertFile() error: %v", err)
	}
	msgs := []transcript.Message{{Project: "proj", SessionID: "s1", Role: "user", Text: "why is the build failing", Timestamp: "t1"}}
	if err := s.InsertHistoryMessages(ctx, "/proj/s1.jsonl", "proj", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	reports, err := s.ResetHistory(ctx)
	if err != nil {
		t.Fatalf("ResetHistory() error: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("ResetHistory() reports = %+v, want transcript_files, history_fts and history_messages", reports)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.HistoryMessages != 0 {
		t.Fatalf("Stats().HistoryMessages = %d after reset, want 0", stats.HistoryMessages)
	}
	if stats.MemoryFiles != 1 {
		t.Fatalf("Stats().MemoryFiles = %d after a history-only reset, want 1 (memory must be untouched)", stats.MemoryFiles)
	}

	// The offset is gone, so the file counts as unindexed...
	if _, ok, err := s.TranscriptOffset(ctx, "/proj/s1.jsonl"); err != nil || ok {
		t.Fatalf("TranscriptOffset() after reset = (ok=%v, err=%v), want the file to look unindexed", ok, err)
	}
	// ...and the messages have to be indexable again. If history_messages
	// survived the reset, the insert below would be skipped as a duplicate and
	// the re-index would silently index nothing.
	if err := s.InsertHistoryMessages(ctx, "/proj/s1.jsonl", "proj", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() (after reset) error: %v", err)
	}
	results, err := s.SearchHistory(ctx, HistorySearchQuery{Query: "failing"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchHistory() after reset and re-index = %d results, want 1", len(results))
	}
}

func TestResetOnEmptyStoreClearsNothing(t *testing.T) {
	s, ctx := openTestStore(t)

	for name, reset := range map[string]func(context.Context) ([]ResetReport, error){
		"memory":  s.ResetMemory,
		"history": s.ResetHistory,
	} {
		reports, err := reset(ctx)
		if err != nil {
			t.Fatalf("Reset(%s) on an empty store error: %v", name, err)
		}
		for _, r := range reports {
			if r.Rows != 0 {
				t.Fatalf("Reset(%s) cleared %d rows from an empty %s, want 0", name, r.Rows, r.Table)
			}
		}
	}
}

// TestResetWithAnotherConnectionOpen covers the documented reason to clear
// rows instead of deleting the database file: another klodmem process (one
// runs per MCP client) has the same database open.
func TestResetWithAnotherConnectionOpen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "klodmem.db")

	first, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() (second connection) error: %v", err)
	}
	defer func() { _ = second.Close() }()

	msgs := []transcript.Message{{Project: "proj", SessionID: "s1", Role: "user", Text: "why is the build failing", Timestamp: "t1"}}
	if err := first.InsertHistoryMessages(ctx, "/proj/s1.jsonl", "proj", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	if _, err := second.ResetHistory(ctx); err != nil {
		t.Fatalf("ResetHistory() with another connection open error: %v", err)
	}
	if err := second.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum() with another connection open error: %v", err)
	}

	// The first connection must still work, and see the cleared index.
	if _, ok, err := first.TranscriptOffset(ctx, "/proj/s1.jsonl"); err != nil || ok {
		t.Fatalf("TranscriptOffset() after a reset from the other connection = (ok=%v, err=%v), want unindexed", ok, err)
	}
	if err := first.InsertHistoryMessages(ctx, "/proj/s1.jsonl", "proj", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() (after reset from the other connection) error: %v", err)
	}
	stats, err := first.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.HistoryMessages != 1 {
		t.Fatalf("Stats().HistoryMessages = %d, want 1", stats.HistoryMessages)
	}
}

func sameElements(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]bool, len(want))
	for _, w := range want {
		seen[w] = true
	}
	for _, g := range got {
		if !seen[g] {
			return false
		}
	}
	return true
}

func TestFileMTimeMissing(t *testing.T) {
	s, ctx := openTestStore(t)

	_, ok, err := s.FileMTime(ctx, "/does/not/exist.md")
	if err != nil {
		t.Fatalf("FileMTime() error: %v", err)
	}
	if ok {
		t.Fatalf("FileMTime() ok = true, want false for missing file")
	}
}

func TestDeleteFile(t *testing.T) {
	s, ctx := openTestStore(t)

	m := memoryfile.Memory{
		Path: "/proj/memory/one.md", Project: "proj", Name: "one",
		Type: "project", Description: "desc", Content: "body text", MTime: 1,
	}
	if err := s.UpsertFile(ctx, m); err != nil {
		t.Fatalf("UpsertFile() error: %v", err)
	}

	if err := s.DeleteFile(ctx, m.Path); err != nil {
		t.Fatalf("DeleteFile() error: %v", err)
	}

	_, ok, err := s.FileMTime(ctx, m.Path)
	if err != nil {
		t.Fatalf("FileMTime() error: %v", err)
	}
	if ok {
		t.Fatalf("FileMTime() ok = true after delete, want false")
	}

	results, err := s.Search(ctx, SearchQuery{Query: "body"})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search() after delete = %d results, want 0", len(results))
	}
}

func TestSearch(t *testing.T) {
	s, ctx := openTestStore(t)

	memories := []memoryfile.Memory{
		{
			Path: "/proj-a/memory/db_choice.md", Project: "proj-a", Name: "db-choice",
			Type: "project", Description: "using postgres", Content: "We chose postgres for durability.",
			MTime: 1,
		},
		{
			Path: "/proj-b/memory/testing_style.md", Project: "proj-b", Name: "testing-style",
			Type: "feedback", Description: "table-driven tests", Content: "Always write table-driven tests.",
			MTime: 1,
		},
	}
	for _, m := range memories {
		if err := s.UpsertFile(ctx, m); err != nil {
			t.Fatalf("UpsertFile(%s) error: %v", m.Path, err)
		}
	}

	tests := []struct {
		name    string
		query   SearchQuery
		wantLen int
		wantHit string
	}{
		{
			name:    "match by body content not in description",
			query:   SearchQuery{Query: "durability"},
			wantLen: 1,
			wantHit: "/proj-a/memory/db_choice.md",
		},
		{
			name:    "filter by project",
			query:   SearchQuery{Query: "tests", Project: "proj-b"},
			wantLen: 1,
			wantHit: "/proj-b/memory/testing_style.md",
		},
		{
			name:    "filter by type excludes non-matching",
			query:   SearchQuery{Query: "tests", Type: "project"},
			wantLen: 0,
		},
		{
			name:    "no match",
			query:   SearchQuery{Query: "nonexistentterm"},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := s.Search(ctx, tt.query)
			if err != nil {
				t.Fatalf("Search() error: %v", err)
			}
			if len(results) != tt.wantLen {
				t.Fatalf("Search() = %d results, want %d (%+v)", len(results), tt.wantLen, results)
			}
			if tt.wantHit != "" && results[0].Path != tt.wantHit {
				t.Fatalf("Search()[0].Path = %s, want %s", results[0].Path, tt.wantHit)
			}
		})
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s, ctx := openTestStore(t)

	for _, q := range []string{"", "   ", "\t\n"} {
		if _, err := s.Search(ctx, SearchQuery{Query: q}); err == nil {
			t.Fatalf("Search(%q) error = nil, want an error for a blank query", q)
		}
	}
}

func TestTranscriptOffsetMissing(t *testing.T) {
	s, ctx := openTestStore(t)

	_, ok, err := s.TranscriptOffset(ctx, "/proj/s1.jsonl")
	if err != nil {
		t.Fatalf("TranscriptOffset() error: %v", err)
	}
	if ok {
		t.Fatalf("TranscriptOffset() ok = true, want false for unindexed file")
	}
}

func TestInsertHistoryMessagesAndSearch(t *testing.T) {
	s, ctx := openTestStore(t)

	msgs := []transcript.Message{
		{Project: "proj-a", SessionID: "s1", Role: "user", Text: "why is the build failing on CI", Timestamp: "t1"},
		{Project: "proj-a", SessionID: "s1", Role: "assistant", Text: "the build fails because of a missing dependency", Timestamp: "t2"},
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 1234, 999, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	offset, ok, err := s.TranscriptOffset(ctx, "/proj-a/s1.jsonl")
	if err != nil {
		t.Fatalf("TranscriptOffset() error: %v", err)
	}
	if !ok || offset != 1234 {
		t.Fatalf("TranscriptOffset() = (%d, %v), want (1234, true)", offset, ok)
	}

	results, err := s.SearchHistory(ctx, HistorySearchQuery{Query: "dependency"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 || results[0].Role != "assistant" {
		t.Fatalf("SearchHistory() = %+v, want one assistant hit", results)
	}
	if !strings.Contains(results[0].Snippet, "dependency") {
		t.Fatalf("SearchHistory()[0].Snippet = %q, want it to contain the matched text (not another column)", results[0].Snippet)
	}

	// Re-inserting at a later offset (simulating incremental tailing) must
	// update the offset, not create a duplicate transcript_files row.
	moreMsgs := []transcript.Message{
		{Project: "proj-a", SessionID: "s1", Role: "user", Text: "thanks, that fixed it", Timestamp: "t3"},
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 5000, 1000, moreMsgs); err != nil {
		t.Fatalf("InsertHistoryMessages() (second span) error: %v", err)
	}
	offset, _, err = s.TranscriptOffset(ctx, "/proj-a/s1.jsonl")
	if err != nil {
		t.Fatalf("TranscriptOffset() error: %v", err)
	}
	if offset != 5000 {
		t.Fatalf("TranscriptOffset() after second span = %d, want 5000", offset)
	}

	results, err = s.SearchHistory(ctx, HistorySearchQuery{Query: "fixed"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchHistory() after second span = %d results, want 1", len(results))
	}
}

func TestInsertHistoryMessagesIsIdempotent(t *testing.T) {
	s, ctx := openTestStore(t)

	msgs := []transcript.Message{
		{Project: "proj-a", SessionID: "s1", Role: "user", Text: "why is the build failing on CI", Timestamp: "t1"},
		{Project: "proj-a", SessionID: "s1", Role: "assistant", Text: "the build fails because of a missing dependency", Timestamp: "t2"},
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 1234, 999, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	// The same span parsed again — a rewritten transcript re-read from 0, or a
	// second klodmem process that read the same offset before this one
	// committed — must not index these messages a second time.
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 4321, 1000, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() (same span again) error: %v", err)
	}

	results, err := s.SearchHistory(ctx, HistorySearchQuery{Query: "dependency"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchHistory() = %d results, want 1 (a duplicate insert must be a no-op)", len(results))
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.HistoryMessages != len(msgs) {
		t.Fatalf("Stats().HistoryMessages = %d, want %d", stats.HistoryMessages, len(msgs))
	}
}

func TestDeletedTranscriptCanBeIndexedAgain(t *testing.T) {
	s, ctx := openTestStore(t)

	msgs := []transcript.Message{{Project: "proj-a", SessionID: "s1", Role: "user", Text: "hello world", Timestamp: "t1"}}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}
	if err := s.DeleteTranscript(ctx, "/proj-a/s1.jsonl"); err != nil {
		t.Fatalf("DeleteTranscript() error: %v", err)
	}

	// Deleting has to clear the message keys, not just the FTS rows: a
	// transcript that comes back (or is re-indexed after a rewrite) must be
	// indexable again.
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() (after delete) error: %v", err)
	}

	results, err := s.SearchHistory(ctx, HistorySearchQuery{Query: "hello"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchHistory() after re-index = %d results, want 1", len(results))
	}
}

func TestSearchHistoryFilters(t *testing.T) {
	s, ctx := openTestStore(t)

	msgs := []transcript.Message{
		{Project: "proj-a", SessionID: "s1", Role: "user", Text: "explain the retry logic", Timestamp: "t1"},
		{Project: "proj-b", SessionID: "s2", Role: "assistant", Text: "here is the retry logic explanation", Timestamp: "t2"},
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 100, 1, msgs[:1]); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-b/s2.jsonl", "proj-b", "s2", 100, 1, msgs[1:]); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	results, err := s.SearchHistory(ctx, HistorySearchQuery{Query: "retry", Project: "proj-b"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 || results[0].SessionID != "s2" {
		t.Fatalf("SearchHistory() project filter = %+v, want one hit from s2", results)
	}

	results, err = s.SearchHistory(ctx, HistorySearchQuery{Query: "retry", Role: "user"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 1 || results[0].SessionID != "s1" {
		t.Fatalf("SearchHistory() role filter = %+v, want one hit from s1", results)
	}
}

func TestSearchHistoryEmptyQuery(t *testing.T) {
	s, ctx := openTestStore(t)

	for _, q := range []string{"", "   ", "\t\n"} {
		if _, err := s.SearchHistory(ctx, HistorySearchQuery{Query: q}); err == nil {
			t.Fatalf("SearchHistory(%q) error = nil, want an error for a blank query", q)
		}
	}
}

func TestDeleteTranscript(t *testing.T) {
	s, ctx := openTestStore(t)

	msgs := []transcript.Message{{Project: "proj-a", SessionID: "s1", Role: "user", Text: "hello world", Timestamp: "t1"}}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	if err := s.DeleteTranscript(ctx, "/proj-a/s1.jsonl"); err != nil {
		t.Fatalf("DeleteTranscript() error: %v", err)
	}

	_, ok, err := s.TranscriptOffset(ctx, "/proj-a/s1.jsonl")
	if err != nil {
		t.Fatalf("TranscriptOffset() error: %v", err)
	}
	if ok {
		t.Fatalf("TranscriptOffset() ok = true after delete, want false")
	}

	results, err := s.SearchHistory(ctx, HistorySearchQuery{Query: "hello"})
	if err != nil {
		t.Fatalf("SearchHistory() error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("SearchHistory() after delete = %d results, want 0", len(results))
	}
}

func TestStats(t *testing.T) {
	s, ctx := openTestStore(t)

	if err := s.UpsertFile(ctx, memoryfile.Memory{
		Path: "/proj-a/memory/one.md", Project: "proj-a", Name: "one",
		Type: "project", Description: "d", Content: "c", MTime: 1,
	}); err != nil {
		t.Fatalf("UpsertFile() error: %v", err)
	}

	projA := []transcript.Message{
		{Project: "proj-a", SessionID: "s1", Role: "user", Text: "q1", Timestamp: "2026-08-01T00:00:00Z"},
		{Project: "proj-a", SessionID: "s1", Role: "assistant", Text: "a1", Timestamp: "2026-08-02T00:00:00Z"},
		{Project: "proj-a", SessionID: "s2", Role: "assistant", Text: "a2", Timestamp: "2026-08-03T00:00:00Z"},
	}
	projB := []transcript.Message{
		{Project: "proj-b", SessionID: "s3", Role: "user", Text: "q2", Timestamp: "2026-08-04T00:00:00Z"},
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 100, 1, projA[:2]); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s2.jsonl", "proj-a", "s2", 100, 1, projA[2:]); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-b/s3.jsonl", "proj-b", "s3", 100, 1, projB); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}

	if stats.MemoryFiles != 1 {
		t.Fatalf("Stats().MemoryFiles = %d, want 1", stats.MemoryFiles)
	}
	if stats.HistoryMessages != 4 {
		t.Fatalf("Stats().HistoryMessages = %d, want 4", stats.HistoryMessages)
	}
	if stats.HistorySessions != 3 {
		t.Fatalf("Stats().HistorySessions = %d, want 3", stats.HistorySessions)
	}
	if stats.HistoryProjects != 2 {
		t.Fatalf("Stats().HistoryProjects = %d, want 2", stats.HistoryProjects)
	}
	if stats.RoleCounts["user"] != 2 || stats.RoleCounts["assistant"] != 2 {
		t.Fatalf("Stats().RoleCounts = %+v, want user:2 assistant:2", stats.RoleCounts)
	}
	if len(stats.ByProject) != 2 || stats.ByProject[0].Project != "proj-a" || stats.ByProject[0].Messages != 3 || stats.ByProject[0].Sessions != 2 {
		t.Fatalf("Stats().ByProject[0] = %+v, want proj-a with 3 messages/2 sessions first", stats.ByProject)
	}
	if stats.ByProject[1].Project != "proj-b" || stats.ByProject[1].Messages != 1 {
		t.Fatalf("Stats().ByProject[1] = %+v, want proj-b with 1 message", stats.ByProject[1])
	}
	if stats.EarliestTime != "2026-08-01T00:00:00Z" || stats.LatestTime != "2026-08-04T00:00:00Z" {
		t.Fatalf("Stats() time range = (%s, %s), want (2026-08-01T00:00:00Z, 2026-08-04T00:00:00Z)", stats.EarliestTime, stats.LatestTime)
	}
}

func TestStatsEmptyStore(t *testing.T) {
	s, ctx := openTestStore(t)

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if stats.MemoryFiles != 0 || stats.HistoryMessages != 0 || stats.HistorySessions != 0 || stats.HistoryProjects != 0 {
		t.Fatalf("Stats() on empty store = %+v, want all zero", stats)
	}
	if stats.EarliestTime != "" || stats.LatestTime != "" {
		t.Fatalf("Stats() on empty store time range = (%q, %q), want empty strings", stats.EarliestTime, stats.LatestTime)
	}
}
