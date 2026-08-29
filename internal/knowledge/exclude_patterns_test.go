package knowledge

import "testing"

func TestExcludedByPatternsNormalizesSeparators(t *testing.T) {
	tests := []struct {
		name     string
		rel      string
		patterns []string
		want     bool
	}{
		{name: "slash globstar", rel: `docs\guide.md`, patterns: []string{"docs/**"}, want: true},
		{name: "backslash globstar", rel: "docs/guide.md", patterns: []string{`docs\**`}, want: true},
		{name: "dot prefix", rel: "./private/token.md", patterns: []string{"private/**"}, want: true},
		{name: "directory root", rel: "docs", patterns: []string{"docs/**"}, want: true},
		{name: "basename glob", rel: "nested/ignored.md", patterns: []string{"ignored.md"}, want: true},
		{name: "directory suffix", rel: "tmp/out.log", patterns: []string{"tmp/"}, want: true},
		{name: "non match", rel: "docs/guide.md", patterns: []string{"src/**"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExcludedByPatterns(tt.rel, tt.patterns); got != tt.want {
				t.Fatalf("ExcludedByPatterns(%q, %#v) = %v, want %v", tt.rel, tt.patterns, got, tt.want)
			}
		})
	}
}
