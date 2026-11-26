package acp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// quotaProvider gives a registered provider a fixed quota.
type quotaProvider struct {
	providers.Provider
	q *types.Quota
}

func (p quotaProvider) Quota(context.Context) (*types.Quota, error) { return p.q, nil }

// /info over ACP fetches the quota of the SESSION's provider through the API.
func TestSlashInfoShowsTheSessionProvidersQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	gw := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(gw.Close)
	const name = "acpquota"
	models := func() ([]types.ModelMeta, error) { return []types.ModelMeta{{ID: "m", ContextWindow: 1000}}, nil }
	if err := admin.RegisterOpenAI(name, gw.URL, admin.ProviderWithFetchModels(models)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider(name) })
	q := &types.Quota{Provider: name, Plan: "pro", FetchedAt: time.Now().UTC(), Windows: []types.QuotaWindow{
		{Name: "weekly", Label: "Weekly (all models)", UsedPercent: 42},
	}}
	t.Cleanup(providers.SetForTest([]providers.Provider{quotaProvider{providers.Find(name), q}}))
	config.GetSettingsManager().SetActiveModel(name + "/m")

	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Fatalf("session/new: %v", resp["error"])
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)
	h.send(2, "session/prompt", promptParams{SessionID: sessionID, Prompt: []contentBlock{textBlock("/info")}})
	_, notifications := h.readResponseForCollecting(2)
	var text string
	for _, n := range notifications {
		params, _ := n["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == "agent_message_chunk" {
			s, _ := update["content"].(map[string]any)["text"].(string)
			text += s
		}
	}
	for _, want := range []string{"quota           " + name + " · pro", "weekly          ████░░░░░░  42%"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in /info:\n%s", want, text)
		}
	}
}
