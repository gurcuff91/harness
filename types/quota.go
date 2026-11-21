package types

import "time"

// Quota is a provider's subscription usage in ONE format, whatever the
// provider's own API looks like (GET /api/providers/{name}/quota). A provider
// that reports what REMAINS is converted to what is USED; every time is UTC.
type Quota struct {
	// Provider is the provider slug ("claude-oauth", "minimax", …).
	Provider string `json:"provider"`
	// Plan is the plan name the provider reports ("free", "plus", "pro"…),
	// empty when it does not say.
	Plan string `json:"plan,omitempty"`
	// FetchedAt is when this reading was taken from the provider.
	FetchedAt time.Time `json:"fetched_at"`
	// Stale is true when the provider could not be read just now and this is
	// the last good reading (FetchedAt says how old it is).
	Stale bool `json:"stale,omitempty"`
	// Windows are the provider's usage limits, each over its own period.
	// Always an array (empty when the provider reports no window).
	Windows []QuotaWindow `json:"windows"`
	// Balance is a money allowance, for providers that meter in dollars
	// (ollama-cloud's current plans). Nil otherwise.
	Balance *QuotaBalance `json:"balance,omitempty"`
}

// QuotaWindow is one usage limit over one period.
type QuotaWindow struct {
	// Name is a stable machine key, unique within the Quota ("session",
	// "weekly", "weekly:Fable", "general:5h", …).
	Name string `json:"name"`
	// Label is a short human description ("5-hour session", "Weekly (all
	// models)").
	Label string `json:"label"`
	// Scope is what the window is restricted to (a model family, a bucket
	// such as "video" or "search"), empty when it covers everything.
	Scope string `json:"scope,omitempty"`
	// UsedPercent is how much of the window's allowance is used, 0–100.
	UsedPercent float64 `json:"used_percent"`
	// ResetsAt is when the window resets (UTC); zero when unknown.
	ResetsAt time.Time `json:"resets_at,omitzero"`
}

// QuotaBalance is a money allowance in US dollars.
type QuotaBalance struct {
	// RemainingUSD is what is left to spend (included plus purchased).
	RemainingUSD float64 `json:"remaining_usd"`
	// AllowanceUSD is the plan's included allowance for the period.
	AllowanceUSD float64 `json:"allowance_usd"`
	// ResetsAt is when the included allowance renews (UTC); zero when unknown.
	ResetsAt time.Time `json:"resets_at,omitzero"`
}
