// Package config loads klodmem's runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/josuebrunel/gopkg/xenv"
)

// Config holds klodmem's runtime settings.
type Config struct {
	MemoryRoot   string `env:"KLODMEM_MEMORY_ROOT"   default:"~/.claude/projects"`
	DBPath       string `env:"KLODMEM_DB_PATH"       default:"~/.claude/klodmem/klodmem.db"`
	LogLevel     string `env:"KLODMEM_LOG_LEVEL"     default:"info"`
	IndexHistory bool   `env:"KLODMEM_INDEX_HISTORY" default:"false"`
}

// Load reads Config from the environment and expands leading "~" in path fields.
func Load() (Config, error) {
	var cfg Config
	if err := xenv.Load(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: load: %w", err)
	}

	memoryRoot, err := expandHome(cfg.MemoryRoot)
	if err != nil {
		return Config{}, fmt.Errorf("config: expand memory root: %w", err)
	}
	cfg.MemoryRoot = memoryRoot

	dbPath, err := expandHome(cfg.DBPath)
	if err != nil {
		return Config{}, fmt.Errorf("config: expand db path: %w", err)
	}
	cfg.DBPath = dbPath

	return cfg, nil
}

func expandHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, path[1:]), nil
}
