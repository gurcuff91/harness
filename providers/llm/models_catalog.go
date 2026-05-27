package llm

// ── Model metadata lookup ──────────────────────────────────────────────────

// FindMeta searches all registries (provider caches + hardcoded + remote) for a model ID.
// Returns nil if the model is not found anywhere.
func FindMeta(full string) *ModelMeta {
	_, modelID := ParseModel(full)
	if meta := LookupModel(modelID); meta != nil {
		return meta
	}
	enriched := EnrichMeta(ModelMeta{ID: modelID})
	if enriched.ID != "" {
		return &enriched
	}
	return nil
}
