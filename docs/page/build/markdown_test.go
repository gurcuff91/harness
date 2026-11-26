package main

import (
	"os"
	"strings"
	"testing"
)

func md(t *testing.T, src string) string {
	t.Helper()
	out, _ := renderMarkdown(src, func(h string) string { return h })
	return out
}

func TestInlineCodeIsNotInterpreted(t *testing.T) {
	got := md(t, "Use `a **b** <x>` here")
	if !strings.Contains(got, "<code>a **b** &lt;x&gt;</code>") || strings.Contains(got, "<strong>") {
		t.Fatalf("code span was interpreted: %s", got)
	}
}

func TestHTMLIsEscaped(t *testing.T) {
	if got := md(t, "a <script>alert(1)</script> b"); strings.Contains(got, "<script>") {
		t.Fatalf("raw HTML leaked: %s", got)
	}
}

func TestTableAndCallout(t *testing.T) {
	got := md(t, "| A | B |\n|---|---|\n| `x` | y |\n\n> **Warning** careful\n")
	for _, w := range []string{"<th>A</th>", "<td><code>x</code></td>", `class="callout warning"`} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in %s", w, got)
		}
	}
}

func TestListWithContinuationAndOrdered(t *testing.T) {
	got := md(t, "- one\n  more\n- two\n\n1. a\n2. b\n")
	if strings.Count(got, "<li>") != 4 || !strings.Contains(got, "<li>one more</li>") || !strings.Contains(got, "<ol>") {
		t.Fatalf("lists: %s", got)
	}
}

func TestFencedCodeKeepsContentAndEscapes(t *testing.T) {
	got := md(t, "```bash\n$ echo \"<a>\" # note\n# only a comment\n```\n")
	if !strings.Contains(got, "&lt;a&gt;") || !strings.Contains(got, `<span class="c">`) || !strings.Contains(got, `<span class="k">$</span>`) {
		t.Fatalf("code block: %s", got)
	}
}

func TestHeadingsGetUniqueIDs(t *testing.T) {
	_, heads := renderMarkdown("## Same\n\n## Same\n\n### Other\n", func(h string) string { return h })
	if len(heads) != 3 || heads[0].ID == heads[1].ID {
		t.Fatalf("heads: %+v", heads)
	}
}

func TestBrokenLinkFailsTheBuild(t *testing.T) {
	s := &site{pages: []*page{{title: "A", section: "S", slug: "a", body: "see [b](missing.md)"}}, sections: []string{"S"}}
	if err := render(s, t.TempDir(), t.TempDir()+"/o", "v", ""); err == nil || !strings.Contains(err.Error(), "broken link") {
		t.Fatalf("err = %v", err)
	}
}

func TestTOCTextHasNoBackticks(t *testing.T) {
	s := &site{pages: []*page{{title: "A", section: "S", slug: "a", body: "## The `GET /x` call\n"}}, sections: []string{"S"}}
	out := t.TempDir()
	if err := render(s, t.TempDir(), out, "v", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out + "/a.html")
	if strings.Contains(string(b), ">The `GET") {
		t.Fatal("backticks leaked into the index")
	}
}
