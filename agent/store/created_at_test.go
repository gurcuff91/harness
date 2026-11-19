package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

// Every message the session logs gets a time, in UTC; one the caller set is
// kept; the checkpoint of a compaction gets one too; it is in the JSON line.
func TestMessagesAreStamped(t *testing.T) {
	port := NewInMemoryStore()
	s, err := CreateSession(port, SessionMeta{ID: "s1", CWD: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Second)
	_ = s.AddMessage(types.Message{Role: types.RoleUser, Parts: []types.ContentPart{{Text: "a"}}})
	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	_ = s.AddMessage(types.Message{Role: types.RoleAssistant, Parts: []types.ContentPart{{Text: "b"}}, CreatedAt: fixed})
	if err := s.AddCompactionSummary("sum"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := port.LoadMessages("s1", 0)
	if len(msgs) != 3 {
		t.Fatalf("%d messages", len(msgs))
	}
	if c := msgs[0].CreatedAt; c.Before(before) || c.Location() != time.UTC {
		t.Errorf("first message time = %v", c)
	}
	if !msgs[1].CreatedAt.Equal(fixed) {
		t.Errorf("a caller's time was overwritten: %v", msgs[1].CreatedAt)
	}
	if msgs[2].CreatedAt.IsZero() {
		t.Error("the compaction checkpoint has no time")
	}
	b, _ := json.Marshal(msgs[0])
	if !strings.Contains(string(b), `"created_at":"`) {
		t.Errorf("not in the JSON: %s", b)
	}
	// An old message (no time) is written without the field.
	old, _ := json.Marshal(types.Message{Role: types.RoleUser})
	if strings.Contains(string(old), "created_at") {
		t.Errorf("zero time serialized: %s", old)
	}
}

// Round trip through the file store: the time is written and read back.
func TestCreatedAtSurvivesTheFileStore(t *testing.T) {
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := CreateSession(fs, SessionMeta{ID: "s1", CWD: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddMessage(types.Message{Role: types.RoleUser, Parts: []types.ContentPart{{Text: "a"}}})
	msgs, err := fs.LoadMessages("s1", 0)
	if err != nil || len(msgs) != 1 || msgs[0].CreatedAt.IsZero() {
		t.Fatalf("reloaded = %+v, %v", msgs, err)
	}
}
