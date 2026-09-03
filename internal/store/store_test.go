package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/josuebrunel/klodmem/internal/memoryfile"
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
