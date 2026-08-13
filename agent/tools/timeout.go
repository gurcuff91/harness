package tools

import (
	"errors"
	"net"
)

// isTimeout reports whether err is a timeout (a deadline being exceeded),
// as opposed to any other failure — including a user-initiated cancellation
// (context.Canceled), which is deliberately NOT a timeout.
//
// It matches on the net.Error contract rather than a specific sentinel so it
// works uniformly across every tool that can time out, whatever the source:
//   - context.DeadlineExceeded (Subagent's per-call ctx, ColleagueAsk's HTTP
//     client ctx) — implements net.Error with Timeout() == true.
//   - http.Client.Timeout (Fetch) — surfaces a *url.Error whose underlying
//     error reports Timeout() == true.
//
// context.Canceled reports Timeout() == false, so a Stop is never mistaken
// for a timeout.
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
