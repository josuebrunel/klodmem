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
