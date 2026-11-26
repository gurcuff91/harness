package tui

import (
	"math/rand"
	"time"
)

// spinnerLabels are the themed status messages (mirrors frontend/tui).
var spinnerLabels = []string{
	"Boostaffing", "Maskarizing", "Outworlding", "Khanifying", "Emeraldizing",
	"Razoranging", "Brutalizing", "Windwalking", "Edenianizing", "Kitananizing",
}

// spinnerLabel picks a label at random. It uses math/rand's top-level functions, which are safe for
// concurrent use (a *rand.Rand is NOT): setSpinning(true) is reached from the event-stream goroutine
// and from the input goroutine alike. The generator is seeded randomly since Go 1.20, so no seed is
// needed.
func spinnerLabel() string {
	return spinnerLabels[rand.Intn(len(spinnerLabels))]
}

func nowMonotonic() time.Time { return time.Now() }
