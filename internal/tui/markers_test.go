package tui

import (
	"sync"
	"testing"
)

// spinnerLabel is called by setSpinning(true), which the event-stream goroutine and the input
// goroutine (submitting a prompt) both reach. It must be safe from any number of goroutines.
func TestSpinnerLabelIsSafeFromManyGoroutines(t *testing.T) {
	known := map[string]bool{}
	for _, l := range spinnerLabels {
		known[l] = true
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20000; i++ {
				if l := spinnerLabel(); !known[l] {
					t.Errorf("unknown label %q", l)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// All the labels can come up (a constant or a stuck generator would fail this).
func TestSpinnerLabelVaries(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		seen[spinnerLabel()] = true
	}
	if len(seen) != len(spinnerLabels) {
		t.Fatalf("saw %d of %d labels in 5000 draws", len(seen), len(spinnerLabels))
	}
}
