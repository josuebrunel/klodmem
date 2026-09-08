// Package mcpserver exposes the memory index to Claude Code as an MCP tool
// over stdio.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/josuebrunel/klodmem/internal/store"
)

// SearchInput is the search_memory tool's input schema.
type SearchInput struct {
	Query   string `json:"query" jsonschema:"the search terms to look for across all indexed memory files"`
	Project string `json:"project,omitempty" jsonschema:"optional project slug to restrict results to"`
	Type    string `json:"type,omitempty" jsonschema:"optional memory type filter: user, feedback, project, or reference"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of results to return (default 10)"`
}

// HistorySearchInput is the search_history tool's input schema.
type HistorySearchInput struct {
	Query   string `json:"query" jsonschema:"the search terms to look for across indexed conversation history"`
	Project string `json:"project,omitempty" jsonschema:"optional project slug to restrict results to"`
	Role    string `json:"role,omitempty" jsonschema:"optional role filter: user or assistant"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of results to return (default 10)"`
}

// New builds an MCP server exposing search_memory and search_history, both
// backed by s. version is reported to clients as the server implementation
// version; an empty string falls back to "dev".
func New(s *store.Store, log *slog.Logger, version string) *mcp.Server {
	if log == nil {
		log = slog.Default()
	}
	if version == "" {
		version = "dev"
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "klodmem", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memory",
		Description: "Full-text search over Claude Code's auto-memory files across all projects, matching body content and frontmatter (not just the MEMORY.md index lines).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
		return handleSearchMemory(ctx, s, log, in)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_history",
		Description: "Full-text search over past Claude Code conversation transcripts (user and assistant turns) across all projects. Complements search_memory: this searches raw conversation history, not curated memory.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HistorySearchInput) (*mcp.CallToolResult, any, error) {
		return handleSearchHistory(ctx, s, log, in)
	})

	return server
}

func handleSearchMemory(ctx context.Context, s *store.Store, log *slog.Logger, in SearchInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Query) == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "query must not be empty"}},
			IsError: true,
		}, nil, nil
	}

	results, err := s.Search(ctx, store.SearchQuery{
		Query:   in.Query,
		Project: in.Project,
		Type:    in.Type,
		Limit:   in.Limit,
	})
	if err != nil {
		log.Error("mcpserver: search failed", "query", in.Query, "error", err)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("search failed: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	if len(results) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "no matching memories found"}},
		}, nil, nil
	}

	text := formatResults(results)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, nil, nil
}

func formatResults(results []store.SearchResult) string {
	out := ""
	for i, r := range results {
		if i > 0 {
			out += "\n\n"
		}
		out += fmt.Sprintf("[%s/%s] %s (%s)\n%s\nfile: %s",
			r.Project, r.Type, r.Name, r.Description, r.Snippet, r.Path)
	}
	return out
}

func handleSearchHistory(ctx context.Context, s *store.Store, log *slog.Logger, in HistorySearchInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Query) == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "query must not be empty"}},
			IsError: true,
		}, nil, nil
	}

	results, err := s.SearchHistory(ctx, store.HistorySearchQuery{
		Query:   in.Query,
		Project: in.Project,
		Role:    in.Role,
		Limit:   in.Limit,
	})
	if err != nil {
		log.Error("mcpserver: history search failed", "query", in.Query, "error", err)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("search failed: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	if len(results) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "no matching conversation history found"}},
		}, nil, nil
	}

	text := formatHistoryResults(results)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, nil, nil
}

func formatHistoryResults(results []store.HistorySearchResult) string {
	out := ""
	for i, r := range results {
		if i > 0 {
			out += "\n\n"
		}
		out += fmt.Sprintf("[%s/%s] %s\n%s\nsession: %s\nfile: %s",
			r.Project, r.Role, r.Timestamp, r.Snippet, r.SessionID, r.Path)
	}
	return out
}
