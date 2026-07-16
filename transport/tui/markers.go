package tui

import (
	"math/rand"
	"time"
)

// spinnerLabels are the themed status messages (mirrors transport/tui).
var spinnerLabels = []string{"Boostaffing", "Maskarizing", "Outworlding", "Khanifying", "Emeraldizing"}

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

func spinnerLabel() string {
	return spinnerLabels[rng.Intn(len(spinnerLabels))]
}

func nowMonotonic() time.Time { return time.Now() }
