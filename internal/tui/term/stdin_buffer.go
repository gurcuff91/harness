package term

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	esc                 = "\x1b"
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"
	flushTimeout        = 10 * time.Millisecond
)

// stdinBuffer reassembles raw stdin chunks into complete terminal sequences.
//
// stdin data can arrive in partial chunks — an escape sequence like a mouse
// event or arrow key may be split across reads. This buffer accumulates bytes
// until a complete sequence is detected, then emits sequences one at a time.
// Bracketed paste content is collected and surfaced via onPaste. Port of PI's
// StdinBuffer (originally from OpenTUI, MIT).
type stdinBuffer struct {
	mu          sync.Mutex
	buffer      string
	pasteMode   bool
	pasteBuffer string
	timer       *time.Timer

	onData  func(string)
	onPaste func(string)
}

func newStdinBuffer(onData, onPaste func(string)) *stdinBuffer {
	return &stdinBuffer{onData: onData, onPaste: onPaste}
}

// process feeds a raw chunk of stdin data into the buffer.
func (b *stdinBuffer) process(data string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.processLocked(data)
}

func (b *stdinBuffer) processLocked(data string) {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}

	if data == "" && b.buffer == "" {
		return
	}

	b.buffer += data

	// In paste mode: accumulate until the end marker.
	if b.pasteMode {
		b.pasteBuffer += b.buffer
		b.buffer = ""
		b.tryClosePaste()
		return
	}

	// Detect the start of a paste anywhere in the buffer.
	if idx := strings.Index(b.buffer, bracketedPasteStart); idx != -1 {
		if idx > 0 {
			before := b.buffer[:idx]
			seqs, _ := extractCompleteSequences(before)
			for _, s := range seqs {
				b.emit(s)
			}
		}
		b.buffer = b.buffer[idx+len(bracketedPasteStart):]
		b.pasteMode = true
		b.pasteBuffer = b.buffer
		b.buffer = ""
		b.tryClosePaste()
		return
	}

	// Normal path: extract complete sequences, keep the remainder.
	seqs, remainder := extractCompleteSequences(b.buffer)
	b.buffer = remainder
	for _, s := range seqs {
		b.emit(s)
	}

	// If a partial sequence remains, flush it after a short idle timeout
	// (it may be a lone ESC keypress, not the start of a sequence).
	if b.buffer != "" {
		b.timer = time.AfterFunc(flushTimeout, func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.buffer != "" {
				seq := b.buffer
				b.buffer = ""
				b.emit(seq)
			}
		})
	}
}

// tryClosePaste checks for the paste-end marker and emits the paste if found.
// Any trailing data after the marker is reprocessed.
func (b *stdinBuffer) tryClosePaste() {
	idx := strings.Index(b.pasteBuffer, bracketedPasteEnd)
	if idx == -1 {
		return
	}
	content := b.pasteBuffer[:idx]
	remaining := b.pasteBuffer[idx+len(bracketedPasteEnd):]
	b.pasteMode = false
	b.pasteBuffer = ""
	if b.onPaste != nil {
		b.onPaste(content)
	}
	if remaining != "" {
		b.processLocked(remaining)
	}
}

func (b *stdinBuffer) emit(seq string) {
	if b.onData != nil {
		b.onData(seq)
	}
}

func (b *stdinBuffer) destroy() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.buffer = ""
	b.pasteMode = false
	b.pasteBuffer = ""
}

// ── Sequence completeness detection (port of PI's isCompleteSequence) ──────

type seqStatus int

const (
	statusComplete seqStatus = iota
	statusIncomplete
	statusNotEscape
)

func isCompleteSequence(data string) seqStatus {
	if !strings.HasPrefix(data, esc) {
		return statusNotEscape
	}
	if len(data) == 1 {
		return statusIncomplete
	}
	afterEsc := data[1:]
	switch {
	case strings.HasPrefix(afterEsc, "["):
		// Old-style mouse: ESC[M + 3 bytes = 6 total.
		if strings.HasPrefix(afterEsc, "[M") {
			if len(data) >= 6 {
				return statusComplete
			}
			return statusIncomplete
		}
		return isCompleteCSI(data)
	case strings.HasPrefix(afterEsc, "]"):
		return isCompleteOSC(data)
	case strings.HasPrefix(afterEsc, "P"):
		return isCompleteTerminated(data) // DCS
	case strings.HasPrefix(afterEsc, "_"):
		return isCompleteTerminated(data) // APC
	case strings.HasPrefix(afterEsc, "O"):
		// SS3: ESC O + one char.
		if len(afterEsc) >= 2 {
			return statusComplete
		}
		return statusIncomplete
	case len(afterEsc) == 1:
		// Meta key: ESC + single char.
		return statusComplete
	default:
		return statusComplete
	}
}

func isCompleteCSI(data string) seqStatus {
	if !strings.HasPrefix(data, esc+"[") {
		return statusComplete
	}
	if len(data) < 3 {
		return statusIncomplete
	}
	payload := data[2:]
	last := payload[len(payload)-1]
	if last >= 0x40 && last <= 0x7e {
		// SGR mouse: ESC[<B;X;Y[Mm]
		if strings.HasPrefix(payload, "<") {
			body := payload[1 : len(payload)-1]
			parts := strings.Split(body, ";")
			if (last == 'M' || last == 'm') && len(parts) == 3 && allDigits(parts) {
				return statusComplete
			}
			return statusIncomplete
		}
		return statusComplete
	}
	return statusIncomplete
}

func isCompleteOSC(data string) seqStatus {
	if !strings.HasPrefix(data, esc+"]") {
		return statusComplete
	}
	if strings.HasSuffix(data, esc+"\\") || strings.HasSuffix(data, "\x07") {
		return statusComplete
	}
	return statusIncomplete
}

// isCompleteTerminated handles DCS/APC which end with ST (ESC \).
func isCompleteTerminated(data string) seqStatus {
	if strings.HasSuffix(data, esc+"\\") {
		return statusComplete
	}
	return statusIncomplete
}

func allDigits(parts []string) bool {
	for _, p := range parts {
		if p == "" {
			return false
		}
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				return false
			}
		}
	}
	return true
}

// extractCompleteSequences splits a buffer into complete sequences, returning
// any trailing incomplete remainder. Port of PI's extractCompleteSequences.
func extractCompleteSequences(buffer string) (sequences []string, remainder string) {
	pos := 0
	for pos < len(buffer) {
		remaining := buffer[pos:]
		if strings.HasPrefix(remaining, esc) {
			seqEnd := 1
			matched := false
			for seqEnd <= len(remaining) {
				candidate := remaining[:seqEnd]
				status := isCompleteSequence(candidate)
				if status == statusComplete {
					// WezTerm edge case: '\x1b\x1b' followed by a sequence
					// introducer means a lone ESC press + a real sequence.
					// Emit just the first ESC and restart from the second.
					if candidate == "\x1b\x1b" && seqEnd < len(remaining) {
						next := remaining[seqEnd]
						if next == '[' || next == ']' || next == 'O' || next == 'P' || next == '_' {
							sequences = append(sequences, esc)
							pos++
							matched = true
							break
						}
					}
					sequences = append(sequences, candidate)
					pos += seqEnd
					matched = true
					break
				} else if status == statusIncomplete {
					seqEnd++
				} else {
					sequences = append(sequences, candidate)
					pos += seqEnd
					matched = true
					break
				}
			}
			if !matched || seqEnd > len(remaining) {
				return sequences, remaining
			}
		} else {
			// Take a single UTF-8 rune (not just a byte) to keep multibyte
			// input intact.
			_, size := utf8.DecodeRuneInString(remaining)
			if size == 0 {
				size = 1
			}
			sequences = append(sequences, remaining[:size])
			pos += size
		}
	}
	return sequences, ""
}
