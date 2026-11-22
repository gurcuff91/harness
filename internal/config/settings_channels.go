package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── Input channels: the 1:1 claim registry ───────────────────────────────
//
// A session listening to an external conversation (a Slack channel, …) claims
// it here first, so two sessions — in this process or in any other harness
// process sharing the same store — can never listen to the same conversation.
//
// Store layout: namespace "channels", key = entryKey(type, id), value =
// ChannelClaim (identity lives in the value; the key is never parsed back).

const nsChannels = "channels"

// ChannelClaim records who listens to one external conversation.
type ChannelClaim struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Session string `json:"session"`
	PID     int    `json:"pid"`
	// Started marks when the owner process started (opaque, OS-specific), so a
	// later process that reuses the pid is not mistaken for the owner. Empty
	// in claims written before it existed, or where the OS cannot tell.
	Started   string `json:"started,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	ClaimedAt string `json:"claimed_at"`
}

// ClaimChannel records c as the listener of (c.Type, c.ID) unless another LIVE
// owner holds it. It returns that owner (and claims nothing) when the
// conversation is taken. A claim is held by its (Session, PID) pair: the same
// pair claiming again succeeds (idempotent); any other pair is a conflict while
// alive(owner) is true. A claim whose process is gone is taken over. Atomic
// across processes (SettingsStore.SwapValue).
func (m *SettingsManager) ClaimChannel(c ChannelClaim, alive func(owner ChannelClaim) bool) (owner *ChannelClaim, err error) {
	if c.Type == "" || c.ID == "" || c.Session == "" {
		return nil, fmt.Errorf("config: channel claim needs a type, an id and a session")
	}
	if c.ClaimedAt == "" {
		c.ClaimedAt = time.Now().UTC().Format(time.RFC3339)
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("config: encode channel claim: %w", err)
	}
	err = m.store.SwapValue(nsChannels, entryKey(c.Type, c.ID), func(raw []byte, found bool) ([]byte, bool, error) {
		owner = nil
		if found {
			var cur ChannelClaim
			if json.Unmarshal(raw, &cur) == nil && cur.Session != "" {
				same := cur.Session == c.Session && cur.PID == c.PID
				if !same && alive(cur) {
					owner = &cur
					return nil, false, nil
				}
			}
		}
		return encoded, true, nil
	})
	if err != nil {
		return nil, err
	}
	return owner, nil
}

// ReleaseChannel removes the claim on (typ, id) only if it still belongs to
// (session, pid): a claim taken over by someone else is left alone. Atomic.
func (m *SettingsManager) ReleaseChannel(typ, id, session string, pid int) error {
	return m.store.SwapValue(nsChannels, entryKey(typ, id), func(raw []byte, found bool) ([]byte, bool, error) {
		if !found {
			return nil, false, nil
		}
		var cur ChannelClaim
		if json.Unmarshal(raw, &cur) != nil || cur.Session != session || cur.PID != pid {
			return nil, false, nil
		}
		return nil, true, nil // delete
	})
}

// ChannelClaims lists every recorded claim (live or not).
func (m *SettingsManager) ChannelClaims() []ChannelClaim {
	entries, err := m.store.List(nsChannels)
	if err != nil {
		return nil
	}
	out := make([]ChannelClaim, 0, len(entries))
	for _, raw := range entries {
		var c ChannelClaim
		if json.Unmarshal(raw, &c) == nil && c.Session != "" {
			out = append(out, c)
		}
	}
	return out
}
