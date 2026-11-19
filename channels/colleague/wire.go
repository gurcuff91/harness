package colleague

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/gurcuff91/harness/types"
)

// The frame: one connection per message, one JSON line each way.
//
//	→ {"kind":"ask"|"reply","from":"scorpion-hero","text":"…","images":[…]}
//	← {"ok":true} | {"ok":false,"error":"…"}
//
// "ok" means the message was queued in the target session; the answer to an
// ask comes back later as a reply, over the asker's own socket.

const (
	kindAsk   = "ask"
	kindReply = "reply"
)

// maxFrame bounds one line (images travel base64 inside it). A variable for tests.
var maxFrame = 50 << 20

// dialTimeout and ioTimeout bound one delivery (a variable for tests).
var (
	dialTimeout = 2 * time.Second
	ioTimeout   = 30 * time.Second
)

type message struct {
	Kind   string            `json:"kind"`
	From   string            `json:"from"`
	Text   string            `json:"text"`
	Images []types.ImageData `json:"images,omitempty"`
}

type ack struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// errGone: nobody listens at the address (the colleague left).
var errGone = errors.New("not online")

// send delivers m to the socket at path and returns the receiver's verdict.
func send(ctx context.Context, path string, m message) error {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return errGone
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(line) > maxFrame {
		return fmt.Errorf("message too large (%d bytes, max %d)", len(line), maxFrame)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	var a ack
	if err := readLine(conn, &a); err != nil {
		return fmt.Errorf("no answer from the colleague: %w", err)
	}
	if !a.OK {
		return errors.New(a.Error)
	}
	return nil
}

// readLine reads one JSON line of at most maxFrame bytes into v.
func readLine(conn net.Conn, v any) error {
	r := bufio.NewReaderSize(conn, 64<<10)
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxFrame {
			return fmt.Errorf("frame over %d bytes", maxFrame)
		}
		if !isPrefix {
			break
		}
	}
	return json.Unmarshal(buf, v)
}

// respond writes the verdict line back.
func respond(conn net.Conn, err error) {
	a := ack{OK: err == nil}
	if err != nil {
		a.Error = err.Error()
	}
	line, _ := json.Marshal(a)
	_, _ = conn.Write(append(line, '\n'))
}
