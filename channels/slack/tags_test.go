package slack

import (
	"reflect"
	"testing"
)

func TestBuildPrompt(t *testing.T) {
	got := buildPrompt("U1", "D9", "  read this  ", []string{"/tmp/a b.txt", `/tmp/q"x.csv`})
	want := "<slack user=\"U1\" channel=\"D9\">\nread this\n<slack-attach path=\"/tmp/a b.txt\"/>\n<slack-attach path=\"/tmp/q&#34;x.csv\"/>\n</slack>"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := buildPrompt("U1", "C1", "", []string{"/tmp/f"}); got != "<slack user=\"U1\" channel=\"C1\">\n<slack-attach path=\"/tmp/f\"/>\n</slack>" {
		t.Errorf("file only: %q", got)
	}
}

func TestExtractUploads(t *testing.T) {
	cases := []struct {
		in    string
		paths []string
		clean string
	}{
		{"plain text", nil, "plain text"},
		{"here\n<slack-upload path=\"/a.png\"/>\nand\n<slack-upload path='/b c.pdf' />", []string{"/a.png", "/b c.pdf"}, "here\n\nand"},
		{"<slack-upload path=\"/x&amp;y\">", []string{"/x&y"}, ""},
		{"<slack-upload path=\"  \"/>ok", nil, "ok"},
		// inbound tags the model echoed are stripped; real Slack markup kept
		{"<slack user=\"U1\" channel=\"C1\">\n<@U1> done <#C2> <https://a|b>\n</slack>", nil, "<@U1> done <#C2> <https://a|b>"},
		{"see <slack-attach path=\"/tmp/f\"/> above", nil, "see  above"},
		// not our tags
		{"<slackware> and <slack-uploader>", nil, "<slackware> and <slack-uploader>"},
	}
	for _, c := range cases {
		paths, clean := extractUploads(c.in)
		if !reflect.DeepEqual(paths, c.paths) || clean != c.clean {
			t.Errorf("extractUploads(%q) = %q, %q; want %q, %q", c.in, paths, clean, c.paths, c.clean)
		}
	}
}

func TestLooksLikeID(t *testing.T) {
	for s, want := range map[string]bool{
		"C0123ABC": true, "D9X": true, "U42": true, "W0AB": true, "G1Z": true,
		"general": false, "Cgeneral": false, "C1": false, "X123": false, "c0123": false, "C01-23": false,
	} {
		if got := looksLikeID(s); got != want {
			t.Errorf("looksLikeID(%q) = %v", s, got)
		}
	}
}
