package llm

import (
	"testing"

	"github.com/gurcuff91/harness/types"
)

// useCatalog installs a fixed catalog (no network, no disk) for the test.
func useCatalog(t *testing.T, body string) {
	t.Helper()
	resetRegistryForTest()
	registry.mu.Lock()
	registry.reg, registry.loaded = parseRegistry([]byte(body)), true
	registry.mu.Unlock()
	t.Cleanup(resetRegistryForTest)
}

const provCatalog = `{"data":[
 {"id":"acme/priced-1","name":"Priced One","context_length":200000,
  "architecture":{"input_modalities":["text","image"]},"supported_parameters":["reasoning"],
  "pricing":{"prompt":"0.000003","completion":"0.000015"},"top_provider":{"max_completion_tokens":64000}},
 {"id":"acme/free-1","name":"Free One","context_length":64000,
  "pricing":{"prompt":"0","completion":"0"},"top_provider":{"max_completion_tokens":8000}},
 {"id":"acme/noprice-1","name":"No Price","context_length":32000,"top_provider":{}}
]}`

// Every datum EnrichMeta fills says which tier it came from; what the provider
// already supplied is marked provider and never overwritten.
func TestEnrichMetaRecordsProvenance(t *testing.T) {
	useCatalog(t, provCatalog)

	t.Run("everything from the catalog", func(t *testing.T) {
		m := EnrichMeta(types.ModelMeta{ID: "priced-1"})
		want := map[string]string{
			types.FieldDisplayName: types.SourceCatalog, types.FieldContextWindow: types.SourceCatalog,
			types.FieldMaxTokens: types.SourceCatalog, types.FieldVision: types.SourceCatalog,
			types.FieldThinking: types.SourceCatalog, types.FieldPricing: types.SourceCatalog,
		}
		for k, v := range want {
			if m.Sources[k] != v {
				t.Errorf("sources[%s] = %q, want %q", k, m.Sources[k], v)
			}
		}
	})

	t.Run("provider's own values win and are marked provider", func(t *testing.T) {
		m := EnrichMeta(types.ModelMeta{ID: "priced-1", DisplayName: "Mine", ContextWindow: 12345, InputPrice: 9})
		if m.ContextWindow != 12345 || m.DisplayName != "Mine" || m.InputPrice != 9 {
			t.Fatalf("provider data overwritten: %+v", m)
		}
		for _, f := range []string{types.FieldDisplayName, types.FieldContextWindow, types.FieldPricing} {
			if m.Sources[f] != types.SourceProvider {
				t.Errorf("sources[%s] = %q, want provider", f, m.Sources[f])
			}
		}
		if m.Sources[types.FieldMaxTokens] != types.SourceCatalog {
			t.Errorf("max_tokens the provider didn't give should come from the catalog, got %q", m.Sources[types.FieldMaxTokens])
		}
	})

	t.Run("catalog knows the model is free: priced by the catalog, not unknown", func(t *testing.T) {
		m := EnrichMeta(types.ModelMeta{ID: "free-1"})
		if m.InputPrice != 0 || m.Sources[types.FieldPricing] != types.SourceCatalog {
			t.Errorf("free model: price=%v source=%q, want 0/catalog", m.InputPrice, m.Sources[types.FieldPricing])
		}
	})

	t.Run("in no source at all: guesses are labelled as guesses", func(t *testing.T) {
		m := EnrichMeta(types.ModelMeta{ID: "deepseek-mystery-vl"}) // not in the catalog; name implies vision + a 128k class
		if m.Sources[types.FieldContextWindow] != types.SourceInferred || m.ContextWindow != 128000 {
			t.Errorf("context: %d from %q, want 128000 inferred", m.ContextWindow, m.Sources[types.FieldContextWindow])
		}
		if m.Sources[types.FieldVision] != types.SourceInferred || !m.Vision {
			t.Errorf("vision: %v from %q, want true inferred", m.Vision, m.Sources[types.FieldVision])
		}
		if m.Sources[types.FieldMaxTokens] != types.SourceDefault {
			t.Errorf("max_tokens source = %q, want default", m.Sources[types.FieldMaxTokens])
		}
		if m.Sources[types.FieldPricing] != types.SourceUnknown {
			t.Errorf("a model nobody priced must be pricing=unknown (0 ≠ free), got %q", m.Sources[types.FieldPricing])
		}
		if m.Sources[types.FieldDisplayName] != types.SourceDefault || m.DisplayName != "deepseek-mystery-vl" {
			t.Errorf("display name: %q from %q, want the id as default", m.DisplayName, m.Sources[types.FieldDisplayName])
		}
	})

	t.Run("a bare model gets the generic defaults, labelled", func(t *testing.T) {
		m := EnrichMeta(types.ModelMeta{ID: "zzz-unheard-of"})
		if m.ContextWindow != 32000 || m.Sources[types.FieldContextWindow] != types.SourceInferred {
			t.Errorf("ctx %d / %q", m.ContextWindow, m.Sources[types.FieldContextWindow])
		}
		if m.Sources[types.FieldVision] != types.SourceDefault || m.Sources[types.FieldThinking] != types.SourceDefault {
			t.Errorf("capabilities nobody reported must be default: %v", m.Sources)
		}
	})

	t.Run("hardcoded registry tier", func(t *testing.T) {
		m := EnrichMeta(types.ModelMeta{ID: "deepseek-v4"})
		if m.Sources[types.FieldContextWindow] != types.SourceRegistry || m.Sources[types.FieldThinking] != types.SourceRegistry {
			t.Errorf("sources = %v, want registry for context/thinking", m.Sources)
		}
	})
}

// Provenance is purely additive: the cascade yields the same values it did.
func TestEnrichMetaValuesUnchangedByProvenance(t *testing.T) {
	useCatalog(t, provCatalog)
	m := EnrichMeta(types.ModelMeta{ID: "priced-1"})
	if m.ContextWindow != 200000 || m.MaxTokens != 64000 || !m.Vision || !m.Thinking ||
		m.InputPrice != 3 || m.OutputPrice != 15 || m.DisplayName != "Priced One" {
		t.Errorf("values changed: %+v", m)
	}
}

// ApplyRegistryPricing (used by Anthropic / Ollama Cloud) labels pricing too.
func TestApplyRegistryPricingProvenance(t *testing.T) {
	useCatalog(t, provCatalog)
	cases := map[string]struct {
		in   types.ModelMeta
		want string
	}{
		"catalog hit":                {types.ModelMeta{ID: "priced-1"}, types.SourceCatalog},
		"catalog hit, free":          {types.ModelMeta{ID: "free-1"}, types.SourceCatalog},
		"catalog miss":               {types.ModelMeta{ID: "nobody-has-me"}, types.SourceUnknown},
		"provider already priced it": {types.ModelMeta{ID: "priced-1", InputPrice: 1}, types.SourceProvider},
		"in catalog without a price": {types.ModelMeta{ID: "noprice-1"}, types.SourceCatalog},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := tc.in
			ApplyRegistryPricing(&m)
			if got := m.Sources[types.FieldPricing]; got != tc.want {
				t.Errorf("pricing source = %q, want %q", got, tc.want)
			}
		})
	}
}

// The catalog's per-token prices become per-million without float noise.
func TestParseRegistryPricesHaveNoFloatNoise(t *testing.T) {
	reg := parseRegistry([]byte(`{"data":[
	 {"id":"a/x","pricing":{"prompt":"0.00000005","completion":"0.00000045","input_cache_read":"0.000000005","input_cache_write":"0.0000000625"}}]}`))
	m := reg["x"]
	if m.InputPrice != 0.05 || m.OutputPrice != 0.45 || m.CacheRead != 0.005 || m.CacheWrite != 0.0625 {
		t.Errorf("prices = %v %v %v %v, want exactly 0.05 0.45 0.005 0.0625", m.InputPrice, m.OutputPrice, m.CacheRead, m.CacheWrite)
	}
}

// A catalog written to the disk cache by an earlier version holds unrounded
// per-million prices; they must come out clean through every read path.
func TestCachedUnroundedPricesAreCleanedOnRead(t *testing.T) {
	resetRegistryForTest()
	registry.mu.Lock()
	registry.reg = map[string]types.ModelMeta{"noisy": {ID: "noisy", InputPrice: 0.049999999999999996, OutputPrice: 0.44999999999999996, CacheRead: 0.005000000000000001, ContextWindow: 1}}
	registry.loaded = true
	registry.mu.Unlock()
	t.Cleanup(resetRegistryForTest)

	em := EnrichMeta(types.ModelMeta{ID: "noisy"})
	if em.InputPrice != 0.05 || em.OutputPrice != 0.45 || em.CacheRead != 0.005 {
		t.Errorf("EnrichMeta prices = %v %v %v", em.InputPrice, em.OutputPrice, em.CacheRead)
	}
	am := types.ModelMeta{ID: "noisy"}
	ApplyRegistryPricing(&am)
	if am.InputPrice != 0.05 || am.OutputPrice != 0.45 || am.CacheRead != 0.005 {
		t.Errorf("ApplyRegistryPricing prices = %v %v %v", am.InputPrice, am.OutputPrice, am.CacheRead)
	}
	if lm := LookupModel("noisy"); lm == nil || lm.InputPrice != 0.05 {
		t.Errorf("LookupModel price = %+v", lm)
	}
}
