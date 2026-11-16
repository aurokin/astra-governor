package main

import "testing"

func TestFanIsMadeFasterAndTheVendorsLevelPutBack(t *testing.T) {
	// The vendor has the fan at 2; the tablet gets hot, then cools.
	set, ours, was := fanStep(2, 5, 0, 0)
	if set != 5 || ours != 5 || was != 2 {
		t.Fatalf("hot: level %d, ours %d, vendor's %d; want 5, 5, 2", set, ours, was)
	}
	if set, ours, was = fanStep(5, 5, ours, was); set != 5 || ours != 5 || was != 2 {
		t.Fatalf("still hot: level %d, ours %d, vendor's %d; want it left at 5", set, ours, was)
	}
	if set, ours, _ = fanStep(5, 0, ours, was); set != 2 || ours != 0 {
		t.Fatalf("cooled: level %d, ours %d; want the vendor's 2 back and nothing of ours standing", set, ours)
	}
}

func TestFanIsNeverSlowedBelowTheVendorsChoice(t *testing.T) {
	// The vendor already runs it faster than we ask.
	if set, ours, _ := fanStep(4, 3, 0, 0); set != 4 || ours != 0 {
		t.Errorf("level %d, ours %d; want the vendor's 4 left alone", set, ours)
	}
	// We raised it to 5 over the vendor's 2; the vendor then went to 4 by itself.
	set, ours, was := fanStep(4, 5, 5, 2)
	if set != 5 || was != 4 {
		t.Fatalf("level %d, vendor's %d; want 5 again and 4 remembered as the vendor's", set, was)
	}
	if set, _, _ = fanStep(5, 0, ours, was); set != 4 {
		t.Errorf("cooled: level %d, want the vendor's newer 4, not the 2 from before", set)
	}
}

func TestFanFallsBackToTheStandingMinimumNotBelowIt(t *testing.T) {
	// -fan 3 always, top level when hot: cooling down ends at 3, the vendor's 2 being slower.
	set, ours, was := fanStep(2, 5, 0, 0)
	if set, ours, was = fanStep(set, 3, ours, was); set != 3 || ours != 3 || was != 2 {
		t.Errorf("level %d, ours %d, vendor's %d; want 3, 3, 2", set, ours, was)
	}
}
