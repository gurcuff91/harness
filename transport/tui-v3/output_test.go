package tuiv3

import "testing"

func TestUnescapeArgs(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"newline":        {`a\nb`, "a\nb"},
		"double newline": {`a\n\nb`, "a\n\nb"},
		"tab":            {`a\tb`, "a\tb"},
		"literal backslash": {`a\\b`, `a\b`},
		"backslash then n (escaped)": {`a\\nb`, `a\nb`}, // \\ → \, then literal n
		"no escapes":     {"plain text", "plain text"},
		"trailing backslash": {`abc\`, `abc\`},
		"mixed":          {`"text": "line1\nline2\ttabbed"`, "\"text\": \"line1\nline2\ttabbed\""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := unescapeArgs(c.in); got != c.want {
				t.Errorf("unescapeArgs(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
