package server

import (
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
)

// A prompt's origin is persisted with the message, so a reloaded history keeps
// it — and the model is never told: the text is delivered as sent.
func TestThePromptOriginIsStoredWithTheMessage(t *testing.T) {
	srv, _, model := sharedServer(t, nil)
	c := client.New(srv.Addr())
	s, _ := c.CreateSession(model, "/p", "")
	if _, err := c.Ask(s.ID, "from a colleague", 10*time.Second, client.WithOrigin("colleague:raiden-god")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ask(s.ID, "from the user", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	msgs, _ := c.GetMessages(s.ID)
	var got []string
	for _, m := range msgs {
		if m.Role == "user" && m.Meta != nil {
			got = append(got, m.Meta.Origin)
		}
	}
	if len(got) != 1 || got[0] != "colleague:raiden-god" {
		t.Errorf("origins = %v: only the colleague's prompt carries one", got)
	}
	if llm := msgs[0].Parts[0].Text; llm != "from a colleague" {
		t.Errorf("message text = %q: the origin must not alter what the model reads", llm)
	}
}
