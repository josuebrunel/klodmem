// Command klodmem indexes Claude Code's auto-memory markdown files into a
// SQLite full-text index and serves search over MCP (stdio).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/josuebrunel/klodmem/internal/config"
	"github.com/josuebrunel/klodmem/internal/historyindexer"
	"github.com/josuebrunel/klodmem/internal/indexer"
	"github.com/josuebrunel/klodmem/internal/mcpserver"
	"github.com/josuebrunel/klodmem/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is set at build time via -ldflags "-X main.version=...". It falls
// back to "dev" for `go run`/`go build` without that flag.
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("klodmem: fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ingestAll := flag.Bool("ingest", false, "index memory and history once and exit, without starting the MCP server")
	ingestMemory := flag.Bool("ingest.memory", false, "index memory files only, once, and exit")
	ingestHistory := flag.Bool("ingest.history", false, "index conversation history only, once, and exit")
	stat := flag.Bool("stat", false, "print index statistics and exit, without starting the MCP server")
	showVersion := flag.Bool("version", false, "print the klodmem version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("klodmem", version)
		return nil
	}

	doMemory := *ingestAll || *ingestMemory
	doHistory := *ingestAll || *ingestHistory
	oneShot := *ingestAll || *ingestMemory || *ingestHistory || *stat

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logLevel := parseLevel(cfg.LogLevel)
	// MCP over stdio uses stdout for protocol messages, so all logs must go
	// to stderr or they'll corrupt the stream Claude Code reads.
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(log)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	s, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	idx := indexer.New(cfg.MemoryRoot, s, log)
	histIdx := historyindexer.New(cfg.MemoryRoot, s, log)

	if oneShot {
		if doMemory {
			if err := idx.FullScan(ctx); err != nil {
				return err
			}
		}
		if doHistory {
			if err := histIdx.FullScan(ctx); err != nil {
				return err
			}
		}
		if *stat {
			return printStats(ctx, s, cfg.DBPath)
		}
		log.Info("klodmem: ingest complete")
		return nil
	}

	// Register filesystem watches before the initial full scans, not after:
	// the OS starts queuing events for an already-registered watch right
	// away, independent of whether we've started reading from it, so this
	// closes the window where a file changed during a scan (which can take a
	// few seconds on a large history) would otherwise go unnoticed until the
	// next restart.
	watcher, watched, err := idx.SetupWatcher(ctx)
	if err != nil {
		return err
	}
	histWatcher, histWatched, err := histIdx.SetupWatcher(ctx)
	if err != nil {
		return err
	}

	if err := idx.FullScan(ctx); err != nil {
		return err
	}
	if err := histIdx.FullScan(ctx); err != nil {
		return err
	}

	go func() {
		if err := idx.WatchLoop(ctx, watcher, watched); err != nil {
			log.Error("klodmem: watch stopped", "error", err)
		}
	}()
	go func() {
		if err := histIdx.WatchLoop(ctx, histWatcher, histWatched); err != nil {
			log.Error("klodmem: history watch stopped", "error", err)
		}
	}()

	server := mcpserver.New(s, log, version)
	return server.Run(ctx, &mcp.StdioTransport{})
}

func parseLevel(level string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return slog.LevelInfo
	}
	return l
}

// printStats writes a human-readable index summary to stdout. Unlike the
// rest of this binary's structured logging (which goes to stderr because
// stdout carries the MCP protocol), -stat never starts the MCP server, so
// stdout is free to use for the report itself.
func printStats(ctx context.Context, s *store.Store, dbPath string) error {
	stats, err := s.Stats(ctx)
	if err != nil {
		return err
	}

	dbSize := "unknown"
	if info, err := os.Stat(dbPath); err == nil {
		dbSize = humanBytes(info.Size())
	}

	fmt.Println("klodmem index stats")
	fmt.Printf("  db: %s (%s)\n\n", dbPath, dbSize)

	fmt.Println("memory")
	fmt.Printf("  files: %d\n\n", stats.MemoryFiles)

	fmt.Println("history")
	fmt.Printf("  messages: %d (assistant: %d, user: %d)\n",
		stats.HistoryMessages, stats.RoleCounts["assistant"], stats.RoleCounts["user"])
	fmt.Printf("  sessions: %d\n", stats.HistorySessions)
	fmt.Printf("  projects: %d\n", stats.HistoryProjects)
	if stats.EarliestTime != "" {
		fmt.Printf("  range: %s to %s\n", stats.EarliestTime, stats.LatestTime)
	}

	if len(stats.ByProject) > 0 {
		fmt.Println("\nby project")
		for _, p := range stats.ByProject {
			fmt.Printf("  %-60s %6d messages  %4d sessions\n", p.Project, p.Messages, p.Sessions)
		}
	}

	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
