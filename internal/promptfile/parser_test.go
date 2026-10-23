package promptfile

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantErr bool
		wantOK  bool
		want    Prompt
	}{
		{
			name:   "display only",
			line:   `{"display":"fix the build","pastedContents":{},"timestamp":1784946759607,"project":"/home/u/workspace/app","sessionId":"s1"}`,
			wantOK: true,
			want: Prompt{
				Path:      "/home/u/.claude/history.jsonl",
				Project:   "-home-u-workspace-app",
				SessionID: "s1",
				Timestamp: "2026-07-25T02:32:39Z",
				Text:      "fix the build",
			},
		},
		{
			name:   "pasted content is appended in key order",
			line:   `{"display":"see below","pastedContents":{"2":{"content":"second"},"1":{"content":"first"}},"timestamp":0,"project":"/p","sessionId":"s2"}`,
			wantOK: true,
			want: Prompt{
				Path:      "/home/u/.claude/history.jsonl",
				Project:   "-p",
				SessionID: "s2",
				Timestamp: "",
				Text:      "see below\n\nfirst\n\nsecond",
			},
		},
		{
			name:   "pasted content only",
			line:   `{"display":"","pastedContents":{"1":{"content":"pasted body"}},"timestamp":0,"project":"","sessionId":"s3"}`,
			wantOK: true,
			want: Prompt{
				Path:      "/home/u/.claude/history.jsonl",
				SessionID: "s3",
				Text:      "pasted body",
			},
		},
		{
			name:   "no text is skipped",
			line:   `{"display":"   ","pastedContents":{},"timestamp":0,"project":"/p","sessionId":"s4"}`,
			wantOK: false,
		},
		{
			name:    "malformed json",
			line:    `{"display":`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := Parse("/home/u/.claude/history.jsonl", []byte(tt.line))
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
			if !tt.wantOK {
				return
			}
			if got != tt.want {
				t.Fatalf("Parse() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestProjectSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "/home/u/workspace/app", want: "-home-u-workspace-app"},
		{in: "/home/u", want: "-home-u"},
		{in: "", want: ""},
		{in: "/", want: ""},
		{in: "relative/path", want: "-relative-path"},
		{in: `C:\Users\u\app`, want: "-C--Users-u-app"},
	}

	for _, tt := range tests {
		if got := ProjectSlug(tt.in); got != tt.want {
			t.Fatalf("ProjectSlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
