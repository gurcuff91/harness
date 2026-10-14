package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// Criterion 3: a message never serializes "parts": null — even one built
// empty — and a stored null decodes to an empty slice.
func TestMessagePartsNeverNull(t *testing.T) {
	b, err := json.Marshal(types.NewAssistantToolCallMessage("", "", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") || !strings.Contains(string(b), `"parts":[]`) {
		t.Fatalf("encoded = %s, want parts:[]", b)
	}
	var m types.Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","parts":null}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Parts == nil {
		t.Fatal("a stored parts:null must decode to a non-nil empty slice")
	}
	b, _ = json.Marshal(m)
	if strings.Contains(string(b), "null") {
		t.Fatalf("re-encoded = %s", b)
	}
}

// recordingProvider answers "ok" and records the history of every request.
type recordingProvider struct {
	fakeThinkingProbeProvider
	mu   sync.Mutex
	reqs [][]types.Message
}

func (p *recordingProvider) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req.Messages)
	p.mu.Unlock()
	cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "ok"})
	return &types.Response{Text: "ok", Message: types.NewAssistantToolCallMessage("ok", "", "", nil)}, nil
}

// Criterion 4: a session file that already holds {"role":"assistant",
// "parts":null} (written by an older version) loads, resumes and runs a new
// turn; the empty message is never replayed to the provider, is served as
// parts:[] (never null), and message indices stay stable.
func TestSessionWithLegacyNullPartsResumes(t *testing.T) {
	withCleanProviderRegistry(t)
	var thinking string
	p := &recordingProvider{}
	p.fakeThinkingProbeProvider = fakeThinkingProbeProvider{name: "legacy-null", lastThinking: &thinking}
	providers.All = append(providers.All, p)

	dir := t.TempDir()
	fs, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := New(AgentOptions{Store: fs})
	sess, err := a.NewSession(t.TempDir(), "legacy-null/fake-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.PromptAndWait(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	id := sess.ID()
	sess.Close()
	a.Close()

	// Append the legacy line exactly as the incident left it.
	var jsonl string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if strings.HasSuffix(path, id+".jsonl") {
			jsonl = path
		}
		return nil
	})
	f, err := os.OpenFile(jsonl, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"role":"assistant","parts":null}` + "\n")
	f.Close()

	fs2, _ := store.NewFileStore(dir)
	a2 := New(AgentOptions{Store: fs2})
	defer a2.Close()
	resumed, err := a2.ResumeSession(id)
	if err != nil {
		t.Fatalf("resume a session with a legacy parts:null message: %v", err)
	}
	all := resumed.AllMessages()
	legacyIdx := len(all) - 1
	if all[legacyIdx].Parts == nil {
		t.Fatal("the legacy message must load with non-nil (empty) parts")
	}
	if b, _ := json.Marshal(all); strings.Contains(string(b), `"parts":null`) {
		t.Fatal("history must never serialize parts:null")
	}

	p.mu.Lock()
	p.reqs = nil
	p.mu.Unlock()
	if _, err := resumed.PromptAndWait(context.Background(), "second"); err != nil {
		t.Fatalf("new turn after resume: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.reqs[0] {
		if m.Role == types.RoleAssistant && !m.HasContent() {
			t.Fatal("the empty legacy message was replayed to the provider")
		}
	}
	after := resumed.AllMessages()
	if after[legacyIdx].Role != types.RoleAssistant || after[legacyIdx+1].Role != types.RoleUser {
		t.Error("indices must stay stable: the legacy message keeps its position")
	}
}
