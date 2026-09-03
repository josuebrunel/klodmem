package transcript

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantOK  bool
		wantErr bool
		want    Message
	}{
		{
			name:   "user message with plain string content",
			line:   `{"type":"user","sessionId":"s1","timestamp":"2026-08-26T08:50:25.893Z","message":{"role":"user","content":"."}}`,
			wantOK: true,
			want: Message{
				Project: "proj", SessionID: "s1", Role: "user", Text: ".", Timestamp: "2026-08-26T08:50:25.893Z",
			},
		},
		{
			name:   "assistant message with text block",
			line:   `{"type":"assistant","sessionId":"s1","timestamp":"2026-08-26T08:50:31.520Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"Here is the answer."}]}}`,
			wantOK: true,
			want: Message{
				Project: "proj", SessionID: "s1", Role: "assistant", Text: "Here is the answer.", Timestamp: "2026-08-26T08:50:31.520Z",
			},
		},
		{
			name:   "assistant message with only tool_use, no text",
			line:   `{"type":"assistant","sessionId":"s1","timestamp":"t","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{}}]}}`,
			wantOK: false,
		},
		{
			name:   "user message wrapping a tool_result with nested image content",
			line:   `{"type":"user","sessionId":"s1","timestamp":"t","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"image","source":{"type":"base64","data":"abc"}}]}]}}`,
			wantOK: false,
		},
		{
			name:   "non-message line type is skipped",
			line:   `{"type":"file-history-snapshot","sessionId":"s1"}`,
			wantOK: false,
		},
		{
			name:   "sidechain messages are skipped",
			line:   `{"type":"user","sessionId":"s1","isSidechain":true,"message":{"role":"user","content":"hidden"}}`,
			wantOK: false,
		},
		{
			name:   "whitespace-only text is skipped",
			line:   `{"type":"user","sessionId":"s1","message":{"role":"user","content":"   \n  "}}`,
			wantOK: false,
		},
		{
			name:    "malformed json is an error",
			line:    `{not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := Parse("proj", []byte(tt.line))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() unexpected error: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("Parse() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("Parse() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseTruncatesLongText(t *testing.T) {
	long := ""
	for i := 0; i < maxTextRunes+500; i++ {
		long += "a"
	}
	line := `{"type":"user","sessionId":"s1","message":{"role":"user","content":"` + long + `"}}`

	got, ok, err := Parse("proj", []byte(line))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !ok {
		t.Fatalf("Parse() ok = false, want true")
	}
	if r := []rune(got.Text); len(r) != maxTextRunes+1 { // +1 for the truncation marker
		t.Fatalf("Parse() text length = %d, want %d", len(r), maxTextRunes+1)
	}
}
