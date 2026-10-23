// Package planfile parses Claude Code's Plan Mode markdown files
// (~/.claude/plans/*.md) into searchable records. Unlike auto-memory files,
// plans have no YAML frontmatter, so the whole document is the content.
package planfile

import (
	"path/filepath"
	"strings"
)

// maxDescriptionRunes bounds the derived description.
const maxDescriptionRunes = 160

// Plan is a single parsed Plan Mode file.
type Plan struct {
	Path        string
	Name        string
	Description string
	Content     string
	MTime       int64
}

// Parse turns a plan file's raw bytes into a Plan. The name and description
// are derived from the first markdown heading, falling back to the filename
// and first non-empty line respectively. Parse never fails: a plan is plain
// markdown, so any content is valid.
func Parse(path string, raw []byte, mtime int64) Plan {
	content := strings.TrimSpace(string(raw))

	name, description := heading(content)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if description == "" {
		description = firstLine(content)
	}

	return Plan{
		Path:        path,
		Name:        name,
		Description: truncate(description, maxDescriptionRunes),
		Content:     content,
		MTime:       mtime,
	}
}

// heading returns the text of the first level-one heading in content and that
// same text as the description.
func heading(content string) (name, description string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			text := strings.TrimSpace(strings.TrimPrefix(line, "# "))
			return text, text
		}
	}
	return "", ""
}

// firstLine returns the first non-empty line of content.
func firstLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
