package llm

import (
	"bufio"
	"io"
	"strings"
)

// SSEEvent represents a single Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
}

// ParseSSE reads an SSE stream and yields events on a channel.
// Blocks until the reader is exhausted or an error occurs.
func ParseSSE(r io.Reader) <-chan SSEEvent {
	ch := make(chan SSEEvent, 32)

	go func() {
		defer close(ch)

		scanner := bufio.NewScanner(r)
		// Increase buffer for large SSE payloads
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

		var currentEvent string
		var dataLines []string

		for scanner.Scan() {
			line := scanner.Text()

			// Empty line = end of event, dispatch it
			if line == "" {
				if currentEvent != "" || len(dataLines) > 0 {
					event := currentEvent
					if event == "" {
						event = "message"
					}
					data := strings.Join(dataLines, "\n")
					currentEvent = ""
					dataLines = dataLines[:0]

					if event != "ping" {
						ch <- SSEEvent{Event: event, Data: data}
					}
				}
				continue
			}

			// Parse field
			if strings.HasPrefix(line, "event: ") {
				currentEvent = line[7:]
			} else if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, line[6:])
			} else if strings.HasPrefix(line, ":") {
				// Comment — ignore
			}
		}

		// Flush any remaining event
		if currentEvent != "" || len(dataLines) > 0 {
			event := currentEvent
			if event == "" {
				event = "message"
			}
			data := strings.Join(dataLines, "\n")
			if event != "ping" {
				ch <- SSEEvent{Event: event, Data: data}
			}
		}
	}()

	return ch
}
