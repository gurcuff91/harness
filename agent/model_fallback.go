package agent

import (
	"errors"
	"fmt"

	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
)

// ModelFallback records that a session was reopened on a different model than the
// one it was saved with, because that model's provider is not available any more
// (disconnected, credentials gone, or the model retired).
type ModelFallback struct {
	// From is the "provider/model" the session was saved with — and could not use.
	From string `json:"from"`
	// To is the "provider/model" it runs on now (and is saved with from now on).
	To string `json:"to"`
	// Reason is why From was unusable (the resolution error).
	Reason string `json:"reason"`
}

// ErrNoModelAvailable is returned (wrapped) when a session can't be reopened
// because neither its own model nor any fallback model is usable: its provider is
// unavailable AND the harness has no available model to fall back to. Test with
// errors.Is.
var ErrNoModelAvailable = errors.New("no model available to reopen the session")

// resolveSessionModel finds the provider and model a persisted session should
// run on when it is reopened (ResumeSession, ForkSession).
//
// Models are scoped per session, so the saved model is always tried first and,
// when it resolves, nothing else happens. When its provider is gone — not
// connected any more, or the model no longer offered — refusing to open the
// session would strand a perfectly good history behind a provider that may stay
// disconnected, so it falls back to the harness's default model instead:
//
//  1. the configured active model (settings "active_model"), if available;
//  2. otherwise the first model of the first available provider — the same rule
//     a NEW session follows when no active model is set or usable.
//
// A fallback is reported (the *ModelFallback) so the caller can tell the user:
// silently changing model changes cost, capabilities and context window. Only when
// there is no fallback either does the open fail, naming both causes.
//
// Only an UNAVAILABLE provider/model triggers a fallback; any other resolution
// error is returned as-is.
func resolveSessionModel(saved string) (providers.Provider, string, *ModelFallback, error) {
	return resolveSessionModelWith(saved, providers.Resolve)
}

// resolveSessionModelWith is resolveSessionModel over an injectable resolver, so
// the one rule that decides whether to fall back at all — only UNAVAILABILITY
// does — can be tested against an error Resolve itself never produces today.
func resolveSessionModelWith(saved string, resolve func(string) (providers.Provider, string, error)) (providers.Provider, string, *ModelFallback, error) {
	provider, modelID, err := resolve(saved)
	if err == nil {
		return provider, modelID, nil, nil
	}
	if !providers.IsUnavailable(err) {
		return nil, "", nil, err
	}

	// Candidates, in order, without repeating the model that just failed.
	var candidates []string
	if active := config.GetSettingsManager().ActiveModel(); active != "" && active != saved {
		candidates = append(candidates, active)
	}
	var lastErr error
	for _, cand := range candidates {
		p, id, rerr := resolve(cand)
		if rerr == nil {
			return p, id, &ModelFallback{From: saved, To: p.Name() + "/" + id, Reason: err.Error()}, nil
		}
		lastErr = rerr
	}
	// No active model, or it is unavailable too: the first available model of any
	// provider (what a new session does).
	if first := firstAvailableModel(saved); first != "" {
		if p, id, rerr := resolve(first); rerr == nil {
			return p, id, &ModelFallback{From: saved, To: p.Name() + "/" + id, Reason: err.Error()}, nil
		} else {
			lastErr = rerr
		}
	}

	msg := fmt.Sprintf("its model %q is unavailable (%v)", saved, err)
	if lastErr != nil {
		msg += fmt.Sprintf(", and so is the default (%v)", lastErr)
	} else {
		msg += ", and no other provider is connected to fall back to"
	}
	return nil, "", nil, fmt.Errorf("%w: %s — connect a provider or set a default model", ErrNoModelAvailable, msg)
}

// firstAvailableModel returns the first "provider/model" any active provider
// offers, skipping skip (the model that already failed). "" when none.
func firstAvailableModel(skip string) string {
	for _, p := range providers.All() {
		if !p.IsActive() {
			continue
		}
		models := p.Models()
		if len(models) == 0 {
			models, _ = p.FetchModels()
		}
		for _, m := range models {
			if full := p.Name() + "/" + m.ID; full != skip {
				return full
			}
		}
	}
	return ""
}
