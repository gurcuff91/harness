package term

import (
	"reflect"
	"testing"
)

func TestExtractCompleteSequences(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantSeqs []string
		wantRem  string
	}{
		{"plain ascii", "abc", []string{"a", "b", "c"}, ""},
		{"arrow up", "\x1b[A", []string{"\x1b[A"}, ""},
		{"two arrows", "\x1b[A\x1b[B", []string{"\x1b[A", "\x1b[B"}, ""},
		{"ascii then arrow", "x\x1b[C", []string{"x", "\x1b[C"}, ""},
		{"incomplete csi", "\x1b[", nil, "\x1b["},
		{"lone esc", "\x1b", nil, "\x1b"},
		{"enter", "\r", []string{"\r"}, ""},
		{"ctrl-c", "\x03", []string{"\x03"}, ""},
		{"sgr mouse", "\x1b[<35;20;5M", []string{"\x1b[<35;20;5M"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seqs, rem := extractCompleteSequences(tt.in)
			if !reflect.DeepEqual(seqs, tt.wantSeqs) {
				t.Errorf("sequences = %q, want %q", seqs, tt.wantSeqs)
			}
			if rem != tt.wantRem {
				t.Errorf("remainder = %q, want %q", rem, tt.wantRem)
			}
		})
	}
}

func TestStdinBufferReassembly(t *testing.T) {
	var got []string
	b := newStdinBuffer(func(s string) { got = append(got, s) }, nil)

	// Arrow key split across three chunks.
	b.process("\x1b")
	b.process("[")
	b.process("A")

	want := []string{"\x1b[A"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStdinBufferPaste(t *testing.T) {
	var data []string
	var pastes []string
	b := newStdinBuffer(
		func(s string) { data = append(data, s) },
		func(p string) { pastes = append(pastes, p) },
	)

	b.process(bracketedPasteStart + "hello\nworld" + bracketedPasteEnd)

	if len(pastes) != 1 || pastes[0] != "hello\nworld" {
		t.Errorf("pastes = %q, want [hello\\nworld]", pastes)
	}
	if len(data) != 0 {
		t.Errorf("expected no data events during paste, got %q", data)
	}
}

func TestStdinBufferPasteSplit(t *testing.T) {
	var pastes []string
	b := newStdinBuffer(func(string) {}, func(p string) { pastes = append(pastes, p) })

	// Paste arriving in fragments.
	b.process(bracketedPasteStart + "part one ")
	b.process("part two" + bracketedPasteEnd)

	if len(pastes) != 1 || pastes[0] != "part one part two" {
		t.Errorf("pastes = %q, want [part one part two]", pastes)
	}
}

func TestStdinBufferTextBeforePaste(t *testing.T) {
	var data []string
	var pastes []string
	b := newStdinBuffer(
		func(s string) { data = append(data, s) },
		func(p string) { pastes = append(pastes, p) },
	)

	b.process("ab" + bracketedPasteStart + "X" + bracketedPasteEnd)

	if !reflect.DeepEqual(data, []string{"a", "b"}) {
		t.Errorf("data = %q, want [a b]", data)
	}
	if len(pastes) != 1 || pastes[0] != "X" {
		t.Errorf("pastes = %q, want [X]", pastes)
	}
}
