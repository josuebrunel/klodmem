// Package transcript parses Claude Code's session transcript JSONL files
// (one JSON object per line) into searchable message records, extracting
// only the authored text of user/assistant turns — never tool input/output,
// thinking blocks, or images.
package transcript

import (
	"encoding/json"
	"strings"
)

// maxTextRunes bounds how much text a single message contributes to the
// index, so one large paste doesn't dominate a session's footprint.
const maxTextRunes = 4000

// Message is one indexable user/assistant turn extracted from a transcript.
type Message struct {
	Project   string
	SessionID string
	Role      string
	Text      string
	Timestamp string
}

type rawLine struct {
	Type        string      `json:"type"`
	SessionID   string      `json:"sessionId"`
	Timestamp   string      `json:"timestamp"`
	IsSidechain bool        `json:"isSidechain"`
	Message     *rawMessage `json:"message"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type rawBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Parse extracts a Message from one transcript JSONL line. ok is false (with
// a nil error) for lines that aren't a user/assistant turn, are part of a
// sidechain, or contain no authored text (e.g. a message that's only tool
// calls) — none of those are errors, just nothing to index.
func Parse(project string, line []byte) (msg Message, ok bool, err error) {
	var raw rawLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return Message{}, false, err
	}

	if raw.IsSidechain || raw.Message == nil {
		return Message{}, false, nil
	}
	if raw.Type != "user" && raw.Type != "assistant" {
		return Message{}, false, nil
	}

	text, err := extractText(raw.Message.Content)
	if err != nil {
		return Message{}, false, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, false, nil
	}

	return Message{
		Project:   project,
		SessionID: raw.SessionID,
		Role:      raw.Message.Role,
		Text:      truncate(text, maxTextRunes),
		Timestamp: raw.Timestamp,
	}, true, nil
}

// extractText pulls out authored prose from a message's content field, which
// is either a plain string or an array of typed blocks. Only "text" blocks
// are taken — tool_use, tool_result, thinking, and image blocks (and any
// text nested inside a tool_result's own content) are deliberately excluded.
func extractText(content json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s, nil
	}

	var blocks []rawBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return "", err
	}

	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
