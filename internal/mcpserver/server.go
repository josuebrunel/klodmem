// Package mcpserver exposes the memory, history, prompt, and plan indexes to
// Claude Code as MCP tools over stdio, and records per-tool usage for
// usage_stats.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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

// PromptSearchInput is the search_prompts tool's input schema.
type PromptSearchInput struct {
	Query   string `json:"query" jsonschema:"the search terms to look for across indexed prompts"`
	Project string `json:"project,omitempty" jsonschema:"optional project slug to restrict results to"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of results to return (default 10)"`
}

// PlanSearchInput is the search_plans tool's input schema.
type PlanSearchInput struct {
	Query string `json:"query" jsonschema:"the search terms to look for across indexed Plan Mode plans"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results to return (default 10)"`
}

// UsageStatsInput is the usage_stats tool's input schema.
type UsageStatsInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of top queries to return (default 10)"`
}

// New builds an MCP server exposing the search tools and usage_stats, all
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

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_prompts",
		Description: "Full-text search over the prompts you have typed into Claude Code across every project (Claude Code's global prompt history), including pasted content. Returns the matching prompt, timestamp, project, and session id so the full conversation can be opened.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PromptSearchInput) (*mcp.CallToolResult, any, error) {
		return handleSearchPrompts(ctx, s, log, in)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_plans",
		Description: "Full-text search over Claude Code Plan Mode plans (~/.claude/plans/*.md), the planning documents written during plan mode across all projects.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PlanSearchInput) (*mcp.CallToolResult, any, error) {
		return handleSearchPlans(ctx, s, log, in)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "usage_stats",
		Description: "Aggregate statistics for this klodmem server's own tool usage: total calls, errors, per-tool counts, average latency, last use, and the most frequent queries.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in UsageStatsInput) (*mcp.CallToolResult, any, error) {
		return handleUsageStats(ctx, s, log, in)
	})

	return server
}

func handleSearchMemory(ctx context.Context, s *store.Store, log *slog.Logger, in SearchInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	out := searchMemory(ctx, s, log, in)
	recordToolCall(ctx, s, log, "search_memory", in.Query, in.Project, out, start)
	return out.result(), nil, nil
}

func searchMemory(ctx context.Context, s *store.Store, log *slog.Logger, in SearchInput) outcome {
	if strings.TrimSpace(in.Query) == "" {
		return outcome{text: "query must not be empty", isError: true}
	}

	results, err := s.Search(ctx, store.SearchQuery{
		Query:   in.Query,
		Project: in.Project,
		Type:    in.Type,
		Limit:   in.Limit,
	})
	if err != nil {
		log.Error("mcpserver: search failed", "query", in.Query, "error", err)
		return outcome{text: fmt.Sprintf("search failed: %v", err), isError: true}
	}

	if len(results) == 0 {
		return outcome{text: "no matching memories found"}
	}

	return outcome{text: formatResults(results), count: len(results)}
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
	start := time.Now()
	out := searchHistory(ctx, s, log, in)
	recordToolCall(ctx, s, log, "search_history", in.Query, in.Project, out, start)
	return out.result(), nil, nil
}

func searchHistory(ctx context.Context, s *store.Store, log *slog.Logger, in HistorySearchInput) outcome {
	if strings.TrimSpace(in.Query) == "" {
		return outcome{text: "query must not be empty", isError: true}
	}

	results, err := s.SearchHistory(ctx, store.HistorySearchQuery{
		Query:   in.Query,
		Project: in.Project,
		Role:    in.Role,
		Limit:   in.Limit,
	})
	if err != nil {
		log.Error("mcpserver: history search failed", "query", in.Query, "error", err)
		return outcome{text: fmt.Sprintf("search failed: %v", err), isError: true}
	}

	if len(results) == 0 {
		return outcome{text: "no matching conversation history found"}
	}

	return outcome{text: formatHistoryResults(results), count: len(results)}
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

func handleSearchPrompts(ctx context.Context, s *store.Store, log *slog.Logger, in PromptSearchInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	out := searchPrompts(ctx, s, log, in)
	recordToolCall(ctx, s, log, "search_prompts", in.Query, in.Project, out, start)
	return out.result(), nil, nil
}

func searchPrompts(ctx context.Context, s *store.Store, log *slog.Logger, in PromptSearchInput) outcome {
	if strings.TrimSpace(in.Query) == "" {
		return outcome{text: "query must not be empty", isError: true}
	}

	results, err := s.SearchPrompts(ctx, store.PromptSearchQuery{
		Query:   in.Query,
		Project: in.Project,
		Limit:   in.Limit,
	})
	if err != nil {
		log.Error("mcpserver: prompt search failed", "query", in.Query, "error", err)
		return outcome{text: fmt.Sprintf("search failed: %v", err), isError: true}
	}

	if len(results) == 0 {
		return outcome{text: "no matching prompts found"}
	}

	return outcome{text: formatPromptResults(results), count: len(results)}
}

func formatPromptResults(results []store.PromptSearchResult) string {
	out := ""
	for i, r := range results {
		if i > 0 {
			out += "\n\n"
		}
		out += fmt.Sprintf("[%s] %s\n%s\nsession: %s\nfile: %s",
			r.Project, r.Timestamp, r.Snippet, r.SessionID, r.Path)
	}
	return out
}

func handleSearchPlans(ctx context.Context, s *store.Store, log *slog.Logger, in PlanSearchInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	out := searchPlans(ctx, s, log, in)
	recordToolCall(ctx, s, log, "search_plans", in.Query, "", out, start)
	return out.result(), nil, nil
}

func searchPlans(ctx context.Context, s *store.Store, log *slog.Logger, in PlanSearchInput) outcome {
	if strings.TrimSpace(in.Query) == "" {
		return outcome{text: "query must not be empty", isError: true}
	}

	results, err := s.SearchPlans(ctx, store.PlanSearchQuery{
		Query: in.Query,
		Limit: in.Limit,
	})
	if err != nil {
		log.Error("mcpserver: plan search failed", "query", in.Query, "error", err)
		return outcome{text: fmt.Sprintf("search failed: %v", err), isError: true}
	}

	if len(results) == 0 {
		return outcome{text: "no matching plans found"}
	}

	return outcome{text: formatPlanResults(results), count: len(results)}
}

func formatPlanResults(results []store.PlanSearchResult) string {
	out := ""
	for i, r := range results {
		if i > 0 {
			out += "\n\n"
		}
		out += fmt.Sprintf("%s (%s)\n%s\nfile: %s",
			r.Name, r.Description, r.Snippet, r.Path)
	}
	return out
}

// outcome is a tool handler's result before it is wrapped for MCP. count is
// the number of matches found (0 for errors and empty results), recorded for
// usage statistics.
type outcome struct {
	text    string
	count   int
	isError bool
}

func (o outcome) result() *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: o.text}},
		IsError: o.isError,
	}
}

// recordToolCall persists one invocation for usage_stats. It is best effort:
// a logging failure must never fail the search the user asked for.
func recordToolCall(ctx context.Context, s *store.Store, log *slog.Logger, tool, query, project string, out outcome, start time.Time) {
	if err := s.RecordToolCall(ctx, store.ToolCall{
		Timestamp:   time.Now(),
		Tool:        tool,
		Query:       query,
		Project:     project,
		ResultCount: out.count,
		IsError:     out.isError,
		Duration:    time.Since(start),
	}); err != nil {
		log.Warn("mcpserver: record tool call failed", "tool", tool, "error", err)
	}
}

func handleUsageStats(ctx context.Context, s *store.Store, log *slog.Logger, in UsageStatsInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()

	stats, err := s.ToolCallStats(ctx, in.Limit)
	out := outcome{}
	switch {
	case err != nil:
		log.Error("mcpserver: usage stats failed", "error", err)
		out = outcome{text: fmt.Sprintf("stats failed: %v", err), isError: true}
	case stats.TotalCalls == 0:
		out = outcome{text: "no tool calls recorded yet"}
	default:
		out = outcome{text: formatUsageStats(stats), count: len(stats.ByTool)}
	}

	recordToolCall(ctx, s, log, "usage_stats", "", "", out, start)
	return out.result(), nil, nil
}

func formatUsageStats(stats store.ToolCallStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool calls: %d (errors: %d)\n", stats.TotalCalls, stats.Errors)
	if stats.FirstCall != "" {
		fmt.Fprintf(&b, "range: %s to %s\n", stats.FirstCall, stats.LastCall)
	}

	if len(stats.ByTool) > 0 {
		b.WriteString("\nby tool\n")
		for _, t := range stats.ByTool {
			fmt.Fprintf(&b, "  %-16s %6d calls  %4d errors  %5d ms avg  last %s\n",
				t.Tool, t.Calls, t.Errors, t.AvgDurationMs, t.LastUsed)
		}
	}

	if len(stats.TopQueries) > 0 {
		b.WriteString("\ntop queries\n")
		for _, q := range stats.TopQueries {
			fmt.Fprintf(&b, "  %6d  %s\n", q.Calls, q.Query)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}
