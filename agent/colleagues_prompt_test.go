package agent

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
)

// The Colleagues section explains the colleague's own cwd — the one place that
// places a colleague with no active session (a headless server).
func TestColleaguesPromptExplainsInstanceCWD(t *testing.T) {
	on := New(AgentOptions{Store: store.NewInMemoryStore(), EnableColleagues: true})
	defer on.Close()
	prompt, _ := on.buildSystemPrompt(t.TempDir(), &resources.Resources{})
	for _, want := range []string{"- cwd — the working directory it was started in", "no active session"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("## Colleagues section lacks %q", want)
		}
	}
	off := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer off.Close()
	if p, _ := off.buildSystemPrompt(t.TempDir(), &resources.Resources{}); strings.Contains(p, "## Colleagues") {
		t.Error("the section must stay gated on EnableColleagues")
	}
}
