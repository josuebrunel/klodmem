package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/josuebrunel/klodmem/internal/memoryfile"
	"github.com/josuebrunel/klodmem/internal/store"
	"github.com/josuebrunel/klodmem/internal/transcript"
)

func openTestStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "klodmem.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, ctx
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func contentText(res *mcp.CallToolResult) string {
	var out strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out.WriteString(tc.Text)
		}
	}
	return out.String()
}

func TestHandleSearchMemory(t *testing.T) {
	s, ctx := openTestStore(t)
	log := testLogger()

	if err := s.UpsertFile(ctx, memoryfile.Memory{
		Path: "/proj-a/memory/db_choice.md", Project: "proj-a", Name: "db-choice",
		Type: "project", Description: "using postgres", Content: "We chose postgres for durability.",
		MTime: 1,
	}); err != nil {
		t.Fatalf("UpsertFile() error: %v", err)
	}

	tests := []struct {
		name        string
		in          SearchInput
		wantErr     bool
		wantContain []string
	}{
		{
			name:        "empty query",
			in:          SearchInput{Query: ""},
			wantErr:     true,
			wantContain: []string{"query must not be empty"},
		},
		{
			name:        "whitespace-only query",
			in:          SearchInput{Query: "   \t\n"},
			wantErr:     true,
			wantContain: []string{"query must not be empty"},
		},
		{
			name:        "no results",
			in:          SearchInput{Query: "nonexistentterm"},
			wantErr:     false,
			wantContain: []string{"no matching memories found"},
		},
		{
			name:    "real hit",
			in:      SearchInput{Query: "durability"},
			wantErr: false,
			wantContain: []string{
				"proj-a", "project", "db-choice", "using postgres",
				"durability", "/proj-a/memory/db_choice.md",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _, err := handleSearchMemory(ctx, s, log, tt.in)
			if err != nil {
				t.Fatalf("handleSearchMemory() error: %v", err)
			}
			if res.IsError != tt.wantErr {
				t.Fatalf("handleSearchMemory() IsError = %v, want %v", res.IsError, tt.wantErr)
			}
			text := contentText(res)
			for _, want := range tt.wantContain {
				if !strings.Contains(text, want) {
					t.Fatalf("handleSearchMemory() text = %q, want it to contain %q", text, want)
				}
			}
		})
	}
}

func TestHandleSearchHistory(t *testing.T) {
	s, ctx := openTestStore(t)
	log := testLogger()

	msgs := []transcript.Message{
		{Project: "proj-a", SessionID: "s1", Role: "assistant", Text: "the build fails because of a missing dependency", Timestamp: "2026-08-01T00:00:00Z"},
	}
	if err := s.InsertHistoryMessages(ctx, "/proj-a/s1.jsonl", "proj-a", "s1", 100, 1, msgs); err != nil {
		t.Fatalf("InsertHistoryMessages() error: %v", err)
	}

	tests := []struct {
		name        string
		in          HistorySearchInput
		wantErr     bool
		wantContain []string
	}{
		{
			name:        "empty query",
			in:          HistorySearchInput{Query: ""},
			wantErr:     true,
			wantContain: []string{"query must not be empty"},
		},
		{
			name:        "whitespace-only query",
			in:          HistorySearchInput{Query: "  "},
			wantErr:     true,
			wantContain: []string{"query must not be empty"},
		},
		{
			name:        "no results",
			in:          HistorySearchInput{Query: "nonexistentterm"},
			wantErr:     false,
			wantContain: []string{"no matching conversation history found"},
		},
		{
			name:    "real hit",
			in:      HistorySearchInput{Query: "dependency"},
			wantErr: false,
			wantContain: []string{
				"proj-a", "assistant", "2026-08-01T00:00:00Z",
				"dependency", "s1", "/proj-a/s1.jsonl",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _, err := handleSearchHistory(ctx, s, log, tt.in)
			if err != nil {
				t.Fatalf("handleSearchHistory() error: %v", err)
			}
			if res.IsError != tt.wantErr {
				t.Fatalf("handleSearchHistory() IsError = %v, want %v", res.IsError, tt.wantErr)
			}
			text := contentText(res)
			for _, want := range tt.wantContain {
				if !strings.Contains(text, want) {
					t.Fatalf("handleSearchHistory() text = %q, want it to contain %q", text, want)
				}
			}
		})
	}
}

func TestFormatResults(t *testing.T) {
	if got := formatResults(nil); got != "" {
		t.Fatalf("formatResults(nil) = %q, want empty string", got)
	}

	one := []store.SearchResult{
		{Path: "/p/memory/a.md", Project: "p", Type: "project", Name: "a", Description: "d1", Snippet: "s1"},
	}
	got := formatResults(one)
	for _, want := range []string{"p/project", "a", "d1", "s1", "/p/memory/a.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatResults(one) = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("formatResults(one) = %q, want no separator for a single result", got)
	}

	two := append(one, store.SearchResult{
		Path: "/p/memory/b.md", Project: "p", Type: "feedback", Name: "b", Description: "d2", Snippet: "s2",
	})
	got = formatResults(two)
	if !strings.Contains(got, "\n\n") {
		t.Fatalf("formatResults(two) = %q, want a blank-line separator between results", got)
	}
}

func TestNew(t *testing.T) {
	s, ctx := openTestStore(t)

	if err := s.UpsertFile(ctx, memoryfile.Memory{
		Path: "/proj-a/memory/db_choice.md", Project: "proj-a", Name: "db-choice",
		Type: "project", Description: "using postgres", Content: "We chose postgres for durability.",
		MTime: 1,
	}); err != nil {
		t.Fatalf("UpsertFile() error: %v", err)
	}

	server := New(s, nil) // nil logger exercises the slog.Default() fallback

	t1, t2 := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server.Connect() error: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect() error: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	toolList, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error: %v", err)
	}
	names := make(map[string]bool, len(toolList.Tools))
	for _, tool := range toolList.Tools {
		names[tool.Name] = true
	}
	if !names["search_memory"] || !names["search_history"] {
		t.Fatalf("ListTools() = %v, want both search_memory and search_history registered", names)
	}

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_memory",
		Arguments: map[string]any{"query": "durability"},
	})
	if err != nil {
		t.Fatalf("CallTool(search_memory) error: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool(search_memory) IsError = true, want a successful hit")
	}
	if text := contentText(res); !strings.Contains(text, "db-choice") {
		t.Fatalf("CallTool(search_memory) text = %q, want it to contain %q", text, "db-choice")
	}

	res, err = clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_memory",
		Arguments: map[string]any{"query": ""},
	})
	if err != nil {
		t.Fatalf("CallTool(search_memory, empty query) error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(search_memory, empty query) IsError = false, want true")
	}
}

func TestFormatHistoryResults(t *testing.T) {
	if got := formatHistoryResults(nil); got != "" {
		t.Fatalf("formatHistoryResults(nil) = %q, want empty string", got)
	}

	one := []store.HistorySearchResult{
		{Path: "/p/s1.jsonl", SessionID: "s1", Timestamp: "t1", Project: "p", Role: "user", Snippet: "snip1"},
	}
	got := formatHistoryResults(one)
	for _, want := range []string{"p/user", "t1", "snip1", "s1", "/p/s1.jsonl"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatHistoryResults(one) = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("formatHistoryResults(one) = %q, want no separator for a single result", got)
	}

	two := append(one, store.HistorySearchResult{
		Path: "/p/s2.jsonl", SessionID: "s2", Timestamp: "t2", Project: "p", Role: "assistant", Snippet: "snip2",
	})
	got = formatHistoryResults(two)
	if !strings.Contains(got, "\n\n") {
		t.Fatalf("formatHistoryResults(two) = %q, want a blank-line separator between results", got)
	}
}
