// Command klodmem indexes Claude Code's auto-memory markdown files into a
// SQLite full-text index and serves search over MCP (stdio).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/josuebrunel/klodmem/internal/config"
	"github.com/josuebrunel/klodmem/internal/indexer"
	"github.com/josuebrunel/klodmem/internal/mcpserver"
	"github.com/josuebrunel/klodmem/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		slog.Error("klodmem: fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ingest := flag.Bool("ingest", false, "index memory files once and exit, without starting the MCP server")
	flag.Parse()

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
	if err := idx.FullScan(ctx); err != nil {
		return err
	}
	if *ingest {
		log.Info("klodmem: ingest complete")
		return nil
	}

	go func() {
		if err := idx.Watch(ctx); err != nil {
			log.Error("klodmem: watch stopped", "error", err)
		}
	}()

	server := mcpserver.New(s, log)
	return server.Run(ctx, &mcp.StdioTransport{})
}

func parseLevel(level string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return slog.LevelInfo
	}
	return l
}
