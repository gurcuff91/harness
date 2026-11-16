package ansi

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	esc = "\x1b"
	bel = "\x07"
	st  = "\x1b\\"
)

// Every class of sequence that a terminal INTERPRETS, written as a hostile text would. None of them
// may survive SafeLine: that is the whole point of the barrier.
var hostile = map[string]string{
	"OSC 0 title":                esc + "]0;PWN" + bel,
	"OSC 2 title":                esc + "]2;PWN" + st,
	"OSC 52 clipboard":           esc + "]52;c;UFdOLUNMSVA=" + bel,
	"OSC 52 clipboard (ST)":      esc + "]52;c;UFdOLUNMSVA=" + st,
	"OSC 7 cwd":                  esc + "]7;file:///etc" + bel,
	"OSC 7501 status":            esc + "]7501;state=done:app=pwn" + st,
	"OSC 1337 (iTerm)":           esc + "]1337;File=inline=1:AAAA" + bel,
	"OSC 8 with a bad scheme":    esc + "]8;;javascript:alert(1)" + st + "x" + esc + "]8;;" + st,
	"OSC 8 with params (id=)":    esc + "]8;id=1;https://ok.example" + st,
	"OSC 8 terminated by BEL":    esc + "]8;;https://ok.example" + bel,
	"OSC 8 file:":                esc + "]8;;file:///etc/passwd" + st,
	"OSC 8 with a control in it": esc + "]8;;https://a.example/\x01b" + st,
	"OSC 8 unterminated":         esc + "]8;;https://a.example/",
	"CSI report cursor 6n":       esc + "[6n",
	"CSI device attributes c":    esc + "[c",
	"CSI alt screen ?1049h":      esc + "[?1049h",
	"CSI mouse ?1000h":           esc + "[?1000h",
	"CSI bracketed paste ?2004h": esc + "[?2004h",
	"CSI sync output ?2026h":     esc + "[?2026h",
	"CSI cursor up A":            esc + "[5A",
	"CSI cursor position H":      esc + "[1;1H",
	"CSI erase screen 2J":        esc + "[2J",
	"CSI erase scrollback 3J":    esc + "[3J",
	"CSI erase line with param":  esc + "[2K",
	"CSI window ops t":           esc + "[22;0t",
	"CSI save/restore cursor s":  esc + "[s",
	"CSI soft reset !p":          esc + "[!p",
	"CSI with intermediate":      esc + "[0 q",
	"CSI cursor style":           esc + "[5 q",
	"CSI kitty keyboard >":       esc + "[>1u",
	"RIS full reset":             esc + "c",
	"DECSC save cursor":          esc + "7",
	"DECRC restore cursor":       esc + "8",
	"IND index":                  esc + "D",
	"NEL":                        esc + "E",
	"charset G0":                 esc + "(0",
	"DCS":                        esc + "Pq#PWN" + st,
	"APC":                        esc + "_PWNAPC" + st,
	"PM":                         esc + "^PWNPM" + st,
	"SOS":                        esc + "XPWNSOS" + st,
	"lone ESC at the end":        "text" + esc,
	"ESC ESC":                    esc + esc + "[6n",
	"conceal (SGR 8)":            esc + "[8m",
	"blink (SGR 5)":              esc + "[5m",
	"rapid blink (SGR 6)":        esc + "[6m",
	"unknown SGR 21":             esc + "[21m",
	"unknown SGR 53":             esc + "[53m",
	"SGR 38 with a bad shape":    esc + "[38;9;1m",
	"SGR 38;5 out of range":      esc + "[38;5;999m",
	"SGR 38;2 truncated":         esc + "[38;2;1;2m",
	"SGR huge number":            esc + "[99999999999m",
	"SGR with an intermediate":   esc + "[1 m",
	"SGR far too long":           esc + "[" + strings.Repeat("1;", 200) + "1m",
	"C1 CSI U+009B":              "\u009b6n",
	"C1 OSC U+009D":              "\u009d0;PWN\u009c",
	"C1 DCS U+0090":              "\u0090q\u009c",
	"C1 ST U+009C":               "\u009c",
	"C1 APC U+009F":              "\u009fPWN\u009c",
	"BEL":                        bel,
	"BS":                         "\b",
	"VT":                         "\v",
	"FF":                         "\f",
	"SO/SI charset shift":        "\x0e\x0f",
	"NUL":                        "\x00",
	"DEL":                        "\x7f",
	"CR (cursor to column 0)":    "\r",
	"LF":                         "\n",
	"invalid UTF-8":              "\xff\xfe",
}

// allowedShape is an INDEPENDENT statement of what may remain on a line after SafeLine: an SGR, a
// bare erase-to-end-of-line, or one of the two OSC 8 forms. It deliberately does not call
// allowedSequence — an oracle built from the code under test would approve whatever a broken
// version of that code lets through.
var allowedShape = regexp.MustCompile(`\x1b\[[0-9;:]*m|\x1b\[K|\x1b\]8;;[!-~]*\x1b\\|\x1b\]8;;\x1b\\`)

// dangerous reports whether s still carries something a terminal would interpret: an ESC that is
// not part of an allowed shape, a C0/DEL/C1 control, or invalid UTF-8.
func dangerous(s string) (string, bool) {
	rest := allowedShape.ReplaceAllString(s, "")
	for i := 0; i < len(rest); {
		c := rest[i]
		switch {
		case c == 0x1b:
			return "an ESC that is not an allowed sequence", true
		case c < 0x20 && c != '\t', c == 0x7f:
			return "a C0/DEL control character", true
		case c >= 0x80:
			r, size := utf8.DecodeRuneInString(rest[i:])
			if r == utf8.RuneError && size == 1 {
				return "invalid UTF-8", true
			}
			if r >= 0x80 && r <= 0x9f {
				return "a C1 control", true
			}
			i += size
			continue
		}
		i++
	}
	// An allowed SGR must not hide text or blink it: look at what the shapes above matched.
	for _, m := range regexp.MustCompile(`\x1b\[([0-9;:]*)m`).FindAllStringSubmatch(s, -1) {
		for k, f := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' }) {
			prev := ""
			if fs := strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' }); k > 0 {
				prev = fs[k-1]
			}
			if (f == "8" || f == "5" || f == "6") && prev != "5" && prev != "2" && prev != "38" && prev != "48" {
				return "an SGR that conceals or blinks text", true
			}
		}
	}
	return "", false
}

func TestEveryClassOfInterpretedSequenceIsNeutralized(t *testing.T) {
	for name, payload := range hostile {
		t.Run(name, func(t *testing.T) {
			for _, in := range []string{
				payload,
				"before " + payload + " after",
				payload + payload,
				"[" + payload + "]",
			} {
				got := SafeLine(in)
				if why, bad := dangerous(got); bad {
					t.Errorf("SafeLine(%q) = %q still carries %s", in, got, why)
				}
				if !utf8.ValidString(got) {
					t.Errorf("SafeLine(%q) = %q is not valid UTF-8", in, got)
				}
			}
			// what was around the sequence is still there: nothing is silently swallowed
			if got := SafeLine("before " + payload + " after"); !strings.HasPrefix(got, "before ") || !strings.HasSuffix(got, " after") {
				t.Errorf("the surrounding text was damaged: %q", got)
			}
		})
	}
}

func TestNeutralizedTextIsStillReadable(t *testing.T) {
	for in, want := range map[string]string{
		esc + "]52;c;UFdO" + bel: "␛]52;c;UFdO␇", // visible, not hidden: the user sees there was something
		esc + "[6n":              "␛[6n",
		"a" + bel + "b":          "a␇b",
		"x\u009b6ny":             "x6ny",
		"a\x00b\x7fc":            "abc",
		"a\r\nb":                 "ab",
		"ok\xff!":                "ok!",
	} {
		if got := SafeLine(in); got != want {
			t.Errorf("SafeLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// The exact result for the cases that sit on the boundary of the allow-list. A property check
// ("nothing dangerous remains") cannot tell a sequence that was neutralized from one that was
// WRONGLY kept when the check itself is the thing under test, so each boundary is pinned by value.
func TestBoundaryCasesByExactValue(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"erase line with a parameter (2K)":   {"a\x1b[2Kb", "a␛[2Kb"},
		"erase line with a parameter (0K)":   {"a\x1b[0Kb", "a␛[0Kb"},
		"erase to end of line (K) is ours":   {"a\x1b[Kb", "a\x1b[Kb"},
		"SGR conceal":                        {"a\x1b[8mb", "a␛[8mb"},
		"SGR blink":                          {"a\x1b[5mb", "a␛[5mb"},
		"SGR conceal inside a list":          {"a\x1b[1;8mb", "a␛[1;8mb"},
		"SGR unknown":                        {"a\x1b[53mb", "a␛[53mb"},
		"SGR with a leading-zero long value": {"a\x1b[0001mb", "a␛[0001mb"},
		"SGR value that overflows to 1":      {"a\x1b[18446744073709551617mb", "a␛[18446744073709551617mb"},
		"SGR colon form (ITU T.416) is ours": {"a\x1b[38:2:1:2:3mb", "a\x1b[38:2:1:2:3mb"},
		"OSC 8 closed by BEL":                {"\x1b]8;;https://a.example\x07x", "␛]8;;https://a.example␇x"},
		"OSC 8 closed by ST is ours":         {"\x1b]8;;https://a.example\x1b\\x", "\x1b]8;;https://a.example\x1b\\x"},
		"OSC 8 with an id parameter":         {"\x1b]8;id=1;https://a.example\x1b\\x", "␛]8;id=1;https://a.example␛\\x"},
		"OSC 8 to a file URL":                {"\x1b]8;;file:///etc/passwd\x1b\\x", "␛]8;;file:///etc/passwd␛\\x"},
		"OSC 8 to javascript:":               {"\x1b]8;;javascript:alert(1)\x1b\\x", "␛]8;;javascript:alert(1)␛\\x"},
		"OSC 0 closed by ST":                 {"\x1b]0;t\x1b\\x", "␛]0;t␛\\x"},
	} {
		got := SafeLine(c.in)
		if got != c.want {
			t.Errorf("%s: SafeLine(%q) = %q, want %q", name, c.in, got, c.want)
		}
	}
}

// What the TUI emits ITSELF has to pass untouched, or the barrier would break the interface.
func TestWhatTheTUIEmitsItselfPassesUntouched(t *testing.T) {
	for name, in := range map[string]string{
		"plain":                  "hello world",
		"tab":                    "a\tb",
		"unicode":                "ñandú 日本語 🚀 ⚔️ — │ █ ▄",
		"reset":                  Reset,
		"bold dim ital under":    Bold + Dim + Ital + Under + "x" + Reset,
		"inverse strike":         Inv + Strike + "x" + Reset,
		"resets 22 23 24 27 29":  esc + "[22m" + esc + "[23m" + esc + "[24m" + esc + "[27m" + esc + "[29m",
		"bare m":                 esc + "[m",
		"truecolor fg":           esc + "[38;2;38;166;154mx" + Reset,
		"truecolor fg+bg":        esc + "[38;2;26;26;26m" + esc + "[48;2;38;166;154mx" + Reset,
		"combined":               esc + "[1;38;2;200;217;106mx" + Reset,
		"256 colors":             esc + "[38;5;240mx" + esc + "[48;5;17mx",
		"16 colors":              esc + "[31m" + esc + "[42m" + esc + "[91m" + esc + "[103m" + esc + "[39m" + esc + "[49m",
		"erase to end of line":   esc + "[K" + "x",
		"hyperlink":              Hyperlink("text", "https://example.com/x?a=1&b=2"),
		"hyperlink styled":       Hyperlink(Under+"text"+Reset, "http://example.com"),
		"mailto":                 Hyperlink("me", "mailto:a@b.example"),
		"close alone":            esc + "]8;;" + st,
		"every palette function": Primary("a") + Accent("b") + Err("c") + Warn("d") + Dimmed("e") + Muted("f") + Boldify("g") + PrimaryBold("h") + Cursor("i") + CursorColored("j", HexWarn) + FG(HexAccent, "k"),
		"empty":                  "",
	} {
		if got := SafeLine(in); got != in {
			t.Errorf("%s: SafeLine changed the TUI's own text:\n  in : %q\n  out: %q", name, in, got)
		}
	}
}

func TestSafeURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://example.com":            true,
		"http://example.com/a?b=c&d=e#f": true,
		"HTTPS://EXAMPLE.COM":            true,
		"mailto:me@example.com":          true,
		"https://":                       false,
		"http://":                        false,
		"mailto:":                        false,
		"":                               false,
		"javascript:alert(1)":            false,
		"file:///etc/passwd":             false,
		"data:text/html,<script>":        false,
		"vscode://x":                     false,
		"ssh://host":                     false,
		"//example.com":                  false,
		"https://a b":                    false,
		"https://a\tb":                   false,
		"https://a\x01b":                 false,
		"https://a\x7fb":                 false,
		"https://ñ.example":              false,
		"https://example.com/\u200b":     false,
		"https://" + strings.Repeat("a", maxURLBytes):                 false,
		"https://" + strings.Repeat("a", maxURLBytes-len("https://")): true,
	} {
		if got := SafeURL(url); got != want {
			t.Errorf("SafeURL(%.60q) = %v, want %v", url, got, want)
		}
	}
}

func TestSGRAllowList(t *testing.T) {
	for params, want := range map[string]bool{
		"": true, "0": true, "1": true, "2": true, "3": true, "4": true, "7": true, "9": true,
		"22": true, "23": true, "24": true, "27": true, "29": true, "39": true, "49": true,
		"30": true, "37": true, "40": true, "47": true, "90": true, "97": true, "100": true, "107": true,
		"1;4": true, "1;38;2;1;2;3": true, "38;5;0": true, "38;5;255": true, "48;2;0;0;0": true, "38;2;255;255;255;48;2;0;0;0": true,
		"1:4": true,
		// refused
		"5": false, "6": false, "8": false, "21": false, "53": false, "10": false, "50": false, "38": false, "48": false,
		"38;5": false, "38;5;256": false, "38;2;1;2": false, "38;2;1;2;256": false, "38;9;1": false, "38;1": false,
		"1;8": false, "8;1": false, "0;5": false, "1000": false, "99999999999": false, "108": false, "28": false,
		"38;5;1;8": false,
		"0001":     false, "00000000000000000001": false, "18446744073709551617": false, "0000": false,
	} {
		if got := allowedSGR(params); got != want {
			t.Errorf("allowedSGR(%q) = %v, want %v", params, got, want)
		}
	}
	if allowedSGR(strings.Repeat("1;", 40) + "1") {
		t.Error("a far too long SGR sequence must be refused")
	}
}

func TestSafeLineLeavesACleanLineWithoutAllocating(t *testing.T) {
	line := Primary("hello ") + Hyperlink("link", "https://example.com") + " ñandú 日本語"
	var sink string
	if n := testing.AllocsPerRun(100, func() { sink = SafeLine(line) }); n != 0 {
		t.Errorf("%v allocations for a clean line, want 0: the barrier runs on every changed line of every frame", n)
	}
	_ = sink
}

func TestSafeLineIsIdempotent(t *testing.T) {
	for name, p := range hostile {
		once := SafeLine("a " + p + " b")
		if twice := SafeLine(once); twice != once {
			t.Errorf("%s: not idempotent: %q then %q", name, once, twice)
		}
	}
}

// isSafe is only a fast path: it must agree with the full pass on EVERY input, in both directions.
func TestFastPathAgreesWithTheFullPass(t *testing.T) {
	inputs := []string{"", "plain", "\x1b[1mx\x1b[0m", Hyperlink("a", "https://x.example")}
	for _, p := range hostile {
		inputs = append(inputs, p, "a"+p+"b", Bold+p)
	}
	for _, in := range inputs {
		changed := SafeLine(in) != in
		if isSafe(in) == changed {
			t.Errorf("isSafe(%q) = %v but SafeLine changed it = %v", in, isSafe(in), changed)
		}
	}
}

func FuzzSafeLine(f *testing.F) {
	for _, p := range hostile {
		f.Add(p)
		f.Add("a" + p + "b")
	}
	f.Add(Bold + "x" + Reset + Hyperlink("t", "https://a.example"))
	f.Fuzz(func(t *testing.T, in string) {
		got := SafeLine(in)
		if why, bad := dangerous(got); bad {
			t.Fatalf("SafeLine(%q) = %q carries %s", in, got, why)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("SafeLine(%q) = %q is not valid UTF-8", in, got)
		}
		if again := SafeLine(got); again != got {
			t.Fatalf("not idempotent on %q: %q then %q", in, got, again)
		}
		if isSafe(in) != (got == in) {
			t.Fatalf("fast path disagrees on %q", in)
		}
	})
}

// allowedSGRReference is the plain, allocating statement of the rule, kept as an ORACLE for the
// allocation-free allowedSGR: they must agree on every input. It models what a terminal does with
// the parameters: split on ';' and ':' KEEPING empty fields, and read an empty one as 0 — so "1;;4"
// is 1;0;4 and "38;5;" is 38;5;0, a valid color.
func allowedSGRReference(params string) bool {
	if params == "" {
		return true
	}
	fields := strings.Split(strings.ReplaceAll(params, ":", ";"), ";")
	if len(fields) > maxSGRParams {
		return false
	}
	nums := make([]int, len(fields))
	for k, p := range fields {
		if len(p) > 3 {
			return false
		}
		n := 0
		for _, c := range p {
			n = n*10 + int(c-'0')
		}
		nums[k] = n
	}
	for k := 0; k < len(nums); k++ {
		switch n := nums[k]; {
		case n == 0, n == 1, n == 2, n == 3, n == 4, n == 7, n == 9,
			n == 22, n == 23, n == 24, n == 27, n == 29, n == 39, n == 49,
			n >= 30 && n <= 37, n >= 40 && n <= 47, n >= 90 && n <= 97, n >= 100 && n <= 107:
		case n == 38 || n == 48:
			if k+1 >= len(nums) {
				return false
			}
			switch nums[k+1] {
			case 5:
				if k+2 >= len(nums) || nums[k+2] > 255 {
					return false
				}
				k += 2
			case 2:
				if k+4 >= len(nums) || nums[k+2] > 255 || nums[k+3] > 255 || nums[k+4] > 255 {
					return false
				}
				k += 4
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}

func FuzzAllowedSGRMatchesTheReference(f *testing.F) {
	for _, s := range []string{"", "0", "1;4", "38;5;1", "38;2;1;2;3", "38;2;1;2", "8", "5", "1;;4", ";;", "38;5;300", "1:4", "48;2;0;0;0;38;5;9"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, params string) {
		// only the bytes allowedCSI can hand over: digits, ';' and ':'
		for i := 0; i < len(params); i++ {
			if c := params[i]; !(c >= '0' && c <= '9' || c == ';' || c == ':') {
				t.Skip()
			}
		}
		if got, want := allowedSGR(params), allowedSGRReference(params); got != want {
			t.Fatalf("allowedSGR(%q) = %v, the reference says %v", params, got, want)
		}
	})
}
