package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// eventBufferSize is the capacity of the channel streamEvents hands decoded
// SSE events through, between the socket-reading goroutine and whatever
// consumes them (each transport's own render/dispatch loop). A turn with
// thinking:high and a long response can emit thousands of small delta events
// (one per streamed token/fragment); if the consumer falls behind a burst for
// a moment, this is how much slack it gets before backpressure reaches the
// socket read. Matches sseClientBufferSize on the server's side of the same
// pipe (internal/server/server.go) — the two ends of one pipe should have
// comparable slack, not one starving the other.
//
// This single constant replaces three independent (and drifted) values that
// existed before this package: the TUI and Telegram clients both used 4096,
// but the CLI's `-p` client used the bufio.Scanner default (64KB line limit,
// no explicit Buffer() call at all) — a real bug this unification fixes, not
// just a style difference: a single large SSE line (e.g. a big tool_result)
// could silently truncate the scan on that path and no other.
const eventBufferSize = 4096

// sseScanBufferSize is the byte buffer bufio.Scanner grows into per line
// (initial capacity; sseScanMaxLine is the hard cap). Sized well above the
// default 64KB so a single large SSE data line (a big tool_result, a long
// markdown response) doesn't hit bufio.ErrTooLong and silently end the scan.
const (
	sseScanBufferSize = 64 * 1024
	sseScanMaxLine    = 4 * 1024 * 1024
)

// streamEvents opens an SSE connection and returns a channel of decoded
// events. The reader uses a large buffer (see sseScanBufferSize/sseScanMaxLine)
// to tolerate big single-line deltas, and eventBufferSize slack downstream so
// a momentarily slow consumer doesn't stall the socket read.
func (c *Client) streamEvents(ctx context.Context, sessionID string) (<-chan map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/sessions/"+sessionID+"/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SSE connect: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("SSE: status %d", resp.StatusCode)
	}

	ch := make(chan map[string]any, eventBufferSize)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, sseScanBufferSize), sseScanMaxLine)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var evt map[string]any
			if err := json.Unmarshal([]byte(line[6:]), &evt); err != nil {
				continue
			}
			select {
			case ch <- evt:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
