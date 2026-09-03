// Package memoryfile parses Claude Code's auto-memory markdown files
// (YAML frontmatter + body) into structured records.
package memoryfile

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Memory is a single parsed auto-memory record.
type Memory struct {
	Path        string
	Project     string
	Name        string
	Type        string
	Description string
	Content     string
	MTime       int64
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Type string `yaml:"type"`
	} `yaml:"metadata"`
}

const delimiter = "---"

// Parse splits raw into YAML frontmatter and body, deriving Project from the
// parent "<project>/memory/<file>.md" path, and MTime from the given value.
func Parse(path string, raw []byte, mtime int64) (Memory, error) {
	fmText, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return Memory{}, fmt.Errorf("memoryfile: parse %s: %w", path, err)
	}

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return Memory{}, fmt.Errorf("memoryfile: parse %s: unmarshal frontmatter: %w", path, err)
	}

	return Memory{
		Path:        path,
		Project:     projectFromPath(path),
		Name:        fm.Name,
		Type:        fm.Metadata.Type,
		Description: fm.Description,
		Content:     strings.TrimSpace(body),
		MTime:       mtime,
	}, nil
}

// splitFrontmatter separates a leading "---\n...\n---" YAML block from the
// remaining body text.
func splitFrontmatter(raw string) (fm string, body string, err error) {
	trimmed := strings.TrimLeft(raw, "\n")
	if !strings.HasPrefix(trimmed, delimiter) {
		return "", "", fmt.Errorf("missing frontmatter delimiter")
	}

	rest := trimmed[len(delimiter):]
	idx := strings.Index(rest, delimiter)
	if idx == -1 {
		return "", "", fmt.Errorf("unterminated frontmatter block")
	}

	return rest[:idx], rest[idx+len(delimiter):], nil
}

// projectFromPath derives the project slug from a path shaped like
// ".../projects/<project>/memory/<file>.md".
func projectFromPath(path string) string {
	dir := filepath.Dir(path)       // .../projects/<project>/memory
	projectDir := filepath.Dir(dir) // .../projects/<project>
	return filepath.Base(projectDir)
}
