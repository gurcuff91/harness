package providers

import (
	"testing"

	"github.com/gurcuff91/harness/internal/config"
)

// buildCustomProviders is pulled out of initRegistry specifically so it can
// be tested without touching the real ~/.harness/settings.json (which
// config.GetSettingsManager() is a process-wide singleton over) — see its
// own doc comment.

func TestBuildCustomProviders_DisabledIsExcluded(t *testing.T) {
	custom := map[string]config.CustomProvider{
		"enabled-one":  {Type: "openai", URL: "https://a"},
		"disabled-one": {Type: "openai", URL: "https://b", Disabled: true},
	}
	got := buildCustomProviders(custom)
	if len(got) != 1 {
		t.Fatalf("got %d providers, want 1 (disabled one excluded): %+v", len(got), got)
	}
	if got[0].Name() != "enabled-one" {
		t.Errorf("got provider %q, want enabled-one", got[0].Name())
	}
}

func TestBuildCustomProviders_ReservedNameIsExcludedDefensively(t *testing.T) {
	// Simulates a hand-edited settings.json bypassing SetCustomProvider's
	// own validation — the registry must still refuse to shadow a built-in.
	custom := map[string]config.CustomProvider{
		"openai":    {Type: "openai", URL: "https://evil-proxy"},
		"anthropic": {Type: "openai", URL: "https://also-evil"},
		"my-proxy":  {Type: "openai", URL: "https://fine"},
	}
	got := buildCustomProviders(custom)
	if len(got) != 1 {
		t.Fatalf("got %d providers, want 1 (only the non-reserved name): %+v", len(got), got)
	}
	if got[0].Name() != "my-proxy" {
		t.Errorf("got provider %q, want my-proxy", got[0].Name())
	}
}

func TestBuildCustomProviders_UnsupportedTypeIsExcluded(t *testing.T) {
	// Defensive: even though SetCustomProvider rejects a non-"openai" type
	// at write time, a hand-edited file could still contain one — the
	// switch's default (no case) means it's silently skipped, not crashed
	// on or misrouted to the wrong constructor.
	custom := map[string]config.CustomProvider{
		"weird": {Type: "something-unsupported", URL: "https://x"},
	}
	got := buildCustomProviders(custom)
	if len(got) != 0 {
		t.Errorf("got %d providers, want 0 (unsupported type skipped): %+v", len(got), got)
	}
}

func TestBuildCustomProviders_EmptyMapYieldsNoProviders(t *testing.T) {
	got := buildCustomProviders(nil)
	if len(got) != 0 {
		t.Errorf("got %d providers, want 0", len(got))
	}
}

func TestBuildCustomProviders_DeterministicOrder(t *testing.T) {
	custom := map[string]config.CustomProvider{
		"zebra": {Type: "openai", URL: "https://z"},
		"alpha": {Type: "openai", URL: "https://a"},
		"mango": {Type: "openai", URL: "https://m"},
	}
	got := buildCustomProviders(custom)
	if len(got) != 3 {
		t.Fatalf("got %d providers, want 3", len(got))
	}
	want := []string{"alpha", "mango", "zebra"}
	for i, w := range want {
		if got[i].Name() != w {
			t.Errorf("position %d: got %q, want %q (order must be deterministic/sorted)", i, got[i].Name(), w)
		}
	}
}
