package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir() error: %v", err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "tilde with subpath", path: "~/.claude/projects", want: filepath.Join(home, ".claude/projects")},
		{name: "bare tilde", path: "~", want: home},
		{name: "absolute path unchanged", path: "/var/lib/klodmem.db", want: "/var/lib/klodmem.db"},
		{name: "empty path unchanged", path: "", want: ""},
		{name: "relative path without tilde unchanged", path: "relative/path", want: "relative/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandHome(tt.path)
			if err != nil {
				t.Fatalf("expandHome(%q) error: %v", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("expandHome(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// unsetEnv clears key for the duration of the test and restores whatever
// value (or absence) it had beforehand.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%q) error: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
		}
	})
}

func TestLoadDefaults(t *testing.T) {
	unsetEnv(t, "KLODMEM_MEMORY_ROOT")
	unsetEnv(t, "KLODMEM_DB_PATH")
	unsetEnv(t, "KLODMEM_LOG_LEVEL")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if want := filepath.Join(home, ".claude/projects"); cfg.MemoryRoot != want {
		t.Fatalf("Load().MemoryRoot = %q, want %q", cfg.MemoryRoot, want)
	}
	if want := filepath.Join(home, ".claude/klodmem/klodmem.db"); cfg.DBPath != want {
		t.Fatalf("Load().DBPath = %q, want %q", cfg.DBPath, want)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("Load().LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir() error: %v", err)
	}

	t.Setenv("KLODMEM_MEMORY_ROOT", "~/custom/projects")
	t.Setenv("KLODMEM_DB_PATH", "/tmp/custom/klodmem.db")
	t.Setenv("KLODMEM_LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if want := filepath.Join(home, "custom/projects"); cfg.MemoryRoot != want {
		t.Fatalf("Load().MemoryRoot = %q, want %q (tilde-expanded)", cfg.MemoryRoot, want)
	}
	if cfg.DBPath != "/tmp/custom/klodmem.db" {
		t.Fatalf("Load().DBPath = %q, want %q (absolute path passed through)", cfg.DBPath, "/tmp/custom/klodmem.db")
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("Load().LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}
