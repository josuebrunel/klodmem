package memoryfile

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		raw     string
		wantErr bool
		want    Memory
	}{
		{
			name: "valid feedback memory",
			path: "/home/u/.claude/projects/-home-u-app/memory/testing_style.md",
			raw: "---\n" +
				"name: testing-style\n" +
				"description: prefers table-driven tests\n" +
				"metadata:\n" +
				"  type: feedback\n" +
				"---\n\n" +
				"Use table-driven tests. **Why:** matches existing suite.\n",
			want: Memory{
				Path:        "/home/u/.claude/projects/-home-u-app/memory/testing_style.md",
				Project:     "-home-u-app",
				Name:        "testing-style",
				Type:        "feedback",
				Description: "prefers table-driven tests",
				Content:     "Use table-driven tests. **Why:** matches existing suite.",
				MTime:       1000,
			},
		},
		{
			name:    "missing opening delimiter",
			path:    "/x/memory/bad.md",
			raw:     "no frontmatter here\n",
			wantErr: true,
		},
		{
			name:    "unterminated frontmatter",
			path:    "/x/memory/bad.md",
			raw:     "---\nname: foo\n",
			wantErr: true,
		},
		{
			name:    "malformed yaml",
			path:    "/x/memory/bad.md",
			raw:     "---\nname: [unterminated\n---\nbody\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mtime := int64(1000)
			got, err := Parse(tt.path, []byte(tt.raw), mtime)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Parse() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
