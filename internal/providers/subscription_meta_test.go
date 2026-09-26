package providers

import (
	"testing"

	"github.com/gurcuff91/harness/types"
)

// TestMarkAllSubscriptionSetsEveryModel confirms the shared helper used by
// claude-oauth, codex-oauth, and opencode-go's FetchModels marks every
// element, regardless of its starting value.
func TestMarkAllSubscriptionSetsEveryModel(t *testing.T) {
	metas := []types.ModelMeta{
		{ID: "a", IsSubscription: false},
		{ID: "b", IsSubscription: false},
		{ID: "c", IsSubscription: true}, // already true — must stay true
	}
	markAllSubscription(metas)
	for _, m := range metas {
		if !m.IsSubscription {
			t.Errorf("model %q: IsSubscription = false, want true", m.ID)
		}
	}
}

// TestMarkAllSubscriptionEmptySliceIsSafe guards against a nil/empty slice
// panicking (FetchModels can legitimately return zero models).
func TestMarkAllSubscriptionEmptySliceIsSafe(t *testing.T) {
	markAllSubscription(nil)
	markAllSubscription([]types.ModelMeta{})
	// No panic = pass.
}

// TestMinimaxSubscriptionKeyPrefix locks in the "sk-cp-" heuristic that
// distinguishes a MiniMax Token Plan Subscription Key from a regular
// pay-as-you-go API Key — MiniMax exposes no authoritative endpoint for
// this, so the prefix is the sole signal (see FetchModels' doc comment for
// the accepted trade-off: a subscription key that DOESN'T follow this
// pattern, if one exists, is indistinguishable from a metered key).
func TestMinimaxSubscriptionKeyPrefix(t *testing.T) {
	cases := []struct {
		name   string
		apiKey string
		want   bool
	}{
		{"subscription key", "sk-cp-abcdef123456", true},
		{"regular api key (sk- but not sk-cp-)", "sk-abcdef123456", false},
		{"regular api key, unrelated prefix", "mmx-abcdef123456", false},
		{"empty key", "", false},
		{"prefix substring elsewhere is not a match", "abc-sk-cp-123", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := minimaxSubscriptionKeyPrefix(c.apiKey); got != c.want {
				t.Errorf("minimaxSubscriptionKeyPrefix(%q) = %v, want %v", c.apiKey, got, c.want)
			}
		})
	}
}
