// Package promptfile parses Claude Code's global prompt history file
// (~/.claude/history.jsonl, one JSON object per line) into structured,
// searchable prompt records.
package promptfile

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// maxTextRunes bounds how much text a single prompt contributes to the index.
const maxTextRunes = 4000

// Prompt is one indexable prompt extracted from the global history file.
type Prompt struct {
	Path      string
	Project   string
	SessionID string
	Timestamp string
	Text      string
}

type rawLine struct {
	Display        string                    `json:"display"`
	PastedContents map[string]rawPastedEntry `json:"pastedContents"`
	Timestamp      int64                     `json:"timestamp"`
	Project        string                    `json:"project"`
	SessionID      string                    `json:"sessionId"`
}

type rawPastedEntry struct {
	Content string `json:"content"`
}

// Parse extracts a Prompt from one history.jsonl line. ok is false (with a nil
// error) for lines carrying no searchable text. path is the history file the
// line came from, recorded so results point back at the source.
func Parse(path string, line []byte) (p Prompt, ok bool, err error) {
	var raw rawLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return Prompt{}, false, err
	}

	text := promptText(raw)
	if text == "" {
		return Prompt{}, false, nil
	}

	return Prompt{
		Path:      path,
		Project:   ProjectSlug(raw.Project),
		SessionID: raw.SessionID,
		Timestamp: formatTimestamp(raw.Timestamp),
		Text:      truncate(text, maxTextRunes),
	}, true, nil
}

// promptText combines the typed prompt with any pasted content. The pasted
// entries are ordered by their numeric key so the result is deterministic
// across runs (Go map iteration is not).
func promptText(raw rawLine) string {
	parts := make([]string, 0, len(raw.PastedContents)+1)
	if display := strings.TrimSpace(raw.Display); display != "" {
		parts = append(parts, display)
	}

	keys := make([]string, 0, len(raw.PastedContents))
	for k := range raw.PastedContents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if content := strings.TrimSpace(raw.PastedContents[k].Content); content != "" {
			parts = append(parts, content)
		}
	}

	return strings.Join(parts, "\n\n")
}

// ProjectSlug converts an absolute project path into the slug Claude Code uses
// for its per-project directory names. For example
// "/home/u/workspace/app" becomes "-home-u-workspace-app".
func ProjectSlug(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	trimmed := strings.Trim(path, `/\`)
	if trimmed == "" {
		return ""
	}
	replacer := strings.NewReplacer("/", "-", `\`, "-", ":", "-")
	return "-" + replacer.Replace(trimmed)
}

// formatTimestamp converts Claude Code's millisecond epoch value into an
// RFC3339 string, matching the timestamps stored for session transcripts.
func formatTimestamp(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
