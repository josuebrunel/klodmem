package planfile

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		path string
		raw  string
		want Plan
	}{
		{
			name: "heading becomes name and description",
			path: "/home/u/.claude/plans/bug-md-glittery-raven.md",
			raw:  "# Plan: fix the build\n\n## Context\n\nThe build fails on CI.\n",
			want: Plan{
				Path:        "/home/u/.claude/plans/bug-md-glittery-raven.md",
				Name:        "Plan: fix the build",
				Description: "Plan: fix the build",
				Content:     "# Plan: fix the build\n\n## Context\n\nThe build fails on CI.",
				MTime:       1000,
			},
		},
		{
			name: "no heading falls back to filename and first line",
			path: "/home/u/.claude/plans/how-would-you-make-imperative-cray.md",
			raw:  "We are weighing STAR bullets.\n\nMore detail.\n",
			want: Plan{
				Path:        "/home/u/.claude/plans/how-would-you-make-imperative-cray.md",
				Name:        "how-would-you-make-imperative-cray",
				Description: "We are weighing STAR bullets.",
				Content:     "We are weighing STAR bullets.\n\nMore detail.",
				MTime:       1000,
			},
		},
		{
			name: "empty file",
			path: "/home/u/.claude/plans/empty-file.md",
			raw:  "",
			want: Plan{
				Path:  "/home/u/.claude/plans/empty-file.md",
				Name:  "empty-file",
				MTime: 1000,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.path, []byte(tt.raw), 1000)
			if got != tt.want {
				t.Fatalf("Parse() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
