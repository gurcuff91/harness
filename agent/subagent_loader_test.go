package agent

import (
	"testing"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
)

// copyTrackingLoader wraps a resources.ResourceLoader and records every
// Copy() call, returning itself wrapped again (so nested calls — e.g. a
// sub-agent's own sub-agent, if that were ever allowed — would also be
// tracked, though Subagent explicitly disallows recursion).
type copyTrackingLoader struct {
	resources.ResourceLoader
	copies *int
}

func (c *copyTrackingLoader) Copy() resources.ResourceLoader {
	*c.copies++
	return &copyTrackingLoader{ResourceLoader: c.ResourceLoader.Copy(), copies: c.copies}
}

// TestNewLoaderCopiesCustomResourceLoader is the regression test for the bug
// reported against buildSessionTools' Subagent executor: it used to
// hardcode resources.NewFileResourceLoader(cwd) for every sub-agent,
// silently ignoring whatever ResourceLoader the parent Agent was actually
// configured with (e.g. one injected via harness.WithResourceLoader/
// AgentOptions.ResourceLoader to load skills from a database or object
// store) — every sub-agent saw a completely different, filesystem-only
// context than the parent session did.
//
// This can't drive the bug through the Subagent TOOL end-to-end without a
// live provider call (same limitation TestCurrentModelReflectsSwitchModel's
// comment describes), so it verifies the mechanism the fix relies on
// directly: newLoader (used by NewSession/ResumeSession/ForkSession, and
// whose result buildSessionTools threads into the Subagent executor's
// ResourceLoader: loader.Copy()) must call Copy() on a custom loader —
// never silently fall back to a fresh FileResourceLoader — and the returned
// instance must be the copy, not the original.
func TestNewLoaderCopiesCustomResourceLoader(t *testing.T) {
	copies := 0
	custom := &copyTrackingLoader{ResourceLoader: resources.NilLoader{}, copies: &copies}

	a := New(AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: custom})
	defer a.Close()

	got := a.newLoader("/some/cwd")

	if copies != 1 {
		t.Fatalf("Copy() was called %d times, want 1 — newLoader must copy a configured custom loader, never fall back to FileResourceLoader silently", copies)
	}
	gotTracking, ok := got.(*copyTrackingLoader)
	if !ok {
		t.Fatalf("newLoader returned %T, want *copyTrackingLoader (the Copy() result)", got)
	}
	if gotTracking == custom {
		t.Fatal("newLoader returned the SAME instance as a.resourceLoader — must return the Copy(), not the original (a sub-agent sharing the parent's loader instance is the exact bug this fixes)")
	}
}

// TestNewLoaderDefaultsToFileResourceLoaderWhenUnconfigured verifies the
// other half: with no custom loader configured (the common case), newLoader
// still returns a working FileResourceLoader for the given cwd — Copy()
// only comes into play once a custom loader exists.
func TestNewLoaderDefaultsToFileResourceLoaderWhenUnconfigured(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	got := a.newLoader("/some/cwd")
	if _, ok := got.(*resources.FileResourceLoader); !ok {
		t.Fatalf("newLoader() = %T, want *resources.FileResourceLoader when no custom loader is configured", got)
	}
}
