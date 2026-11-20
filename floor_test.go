package main

import "testing"

func TestPerformanceCoresKeepAMinimumWhenTheGameRunsOnThePrimeCores(t *testing.T) {
	g := &governor{cpuFreqs: map[string][]int{
		"policy0": {384000, 556800, 1152000, 1555200, 2227200, 3532800},
		"policy6": {1017600, 2246400, 3801600, 4320000},
	}}
	// Every heavy thread on the prime cores: the game's work on the other six is a few percent of it.
	g.side = []float64{0.02, 0.05, 0.03}
	if khz := g.sideFloor("policy0", 3532); khz != 384000 {
		t.Fatalf("without -base: %d kHz, want the bottom of the table", khz)
	}
	g.base = 1152000
	if khz := g.sideFloor("policy0", 3532); khz != 1152000 {
		t.Errorf("%d kHz, want the minimum", khz)
	}
	// A game thread there that needs more than the minimum still gets it.
	g.side = append(g.side, 0.6)
	if khz := g.sideFloor("policy0", 3532); khz != 2227200 {
		t.Errorf("%d kHz, want what the thread calls for", khz)
	}
	// The prime cores are not held up when the busiest thread is on the other six.
	g.side = []float64{0.02}
	if khz := g.sideFloor("policy6", 3532); khz != 1017600 {
		t.Errorf("prime cores at %d kHz, want the bottom of their table", khz)
	}
}
