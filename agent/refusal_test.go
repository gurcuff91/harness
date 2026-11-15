package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

func cyberRefusal() error {
	return &llm.RefusalError{
		Category:    "cyber",
		Explanation: "Cyber safeguards flagged this message. If this is legitimate security work, you can apply for the Cyber Verification Program: https://example.test/cvp",
	}
}

// A refusal ends the turn with an error that says what happened and ends with the
// hint — and, being deterministic for the history, it is NOT retried: the provider
// is called once, no retry event is emitted, and the turn takes no backoff wait.
func TestRefusalEndsTheTurnWithAnErrorAndIsNotRetried(t *testing.T) {
	_, events, err, p := runScripted(t, reply{err: cyberRefusal()})

	if !errors.Is(err, llm.ErrRefused) {
		t.Fatalf("turn err = %v, want llm.ErrRefused", err)
	}
	var errEvents []types.Event
	for _, e := range events {
		if e.Type == types.EventError {
			errEvents = append(errEvents, e)
		}
	}
	if len(errEvents) != 1 {
		t.Fatalf("error events = %+v, want exactly one", errEvents)
	}
	msg := errEvents[0].Message
	for _, want := range []string{"model refused the request", "(cyber)", "Cyber safeguards flagged this message", "Cyber Verification Program", "Hint: try another provider"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if !strings.HasSuffix(msg, llm.RefusalHint) {
		t.Errorf("the hint must close the error: %q", msg)
	}
	if n := p.callCount(); n != 1 {
		t.Errorf("provider called %d times, want 1: a refusal is never retried", n)
	}
	if n := countType(events, types.EventRetry); n != 0 {
		t.Errorf("%d retry events, want none", n)
	}
	if countType(events, types.EventLoopStart) != 1 || countType(events, types.EventLoopEnd) != 1 || countType(events, types.EventTurnEnd) != 1 {
		t.Errorf("loop_start/loop_end/turn_end = %d/%d/%d, want 1/1/1",
			countType(events, types.EventLoopStart), countType(events, types.EventLoopEnd), countType(events, types.EventTurnEnd))
	}
}

// 3-A: the model's side is never stored (no assistant message — nothing empty is
// replayed later), while the user's own message stays, as for any other provider
// error.
func TestRefusalPersistsNoAssistantMessageButKeepsTheUsers(t *testing.T) {
	sess, _, _, _ := runScripted(t, reply{err: cyberRefusal()})

	if got := assistantMessages(sess); len(got) != 0 {
		t.Fatalf("a refusal left %d assistant message(s) in the history", len(got))
	}
	msgs := sess.AllMessages()
	if len(msgs) != 1 || msgs[0].Role != types.RoleUser || msgs[0].TextContent() != "do the task" {
		t.Fatalf("history = %+v, want exactly the user's message", msgs)
	}
}

// The session is not left in a broken state: after a refusal the same session
// answers normally once the model can (e.g. after switching provider).
func TestSessionAnswersNormallyAfterARefusal(t *testing.T) {
	sess, _, _, p := runScripted(t, reply{err: cyberRefusal()}, reply{text: "all good"})
	out, err := sess.PromptAndWait(t.Context(), "try again")
	if err != nil || out != "all good" {
		t.Fatalf("turn after the refusal: %q %v", out, err)
	}
	if n := p.callCount(); n != 2 {
		t.Errorf("provider calls = %d, want 2 (one refused, one answered)", n)
	}
}

// Only a refusal is "not transient": the classifier still retries what it always did.
func TestRefusalIsNotTransient(t *testing.T) {
	if isTransientNetworkError(cyberRefusal()) {
		t.Error("a refusal must not be classified as transient")
	}
	if isTransientNetworkError(&llm.RefusalError{}) {
		t.Error("a refusal with no details must not be classified as transient")
	}
	if !isTransientNetworkError(ErrEmptyResponse) {
		t.Error("an empty response must stay transient")
	}
}
