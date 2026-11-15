package main

import "time"

// Staying under the vendor's thermal cut.
//
// The vendor's thermal-engine.conf watches one board sensor, xo-therm, and
// takes the CPU ceilings to 2400/2841 MHz in one step when it gets too warm,
// giving them back two degrees lower. A heavy game is then cut for minutes at
// a time, and for good once the case behind the board has warmed up. Where
// "too warm" is depends on the mode: 49.0 C in Rise (three cuts out of three),
// 41 C in Balance, which is what the file says.
//
// In Rise there is room to act first, and this does. In Balance there is not:
// God of War II reaches 41 C after a quarter of an hour at 7 W, and between
// Balance's own ceilings (2918/3072 MHz) and the cut there is one table entry.
//
// Two gentler things done sooner hold the sensor under that. First, GPU floor
// the busy share does not call for is given up (see sample.thrifty): about a
// watt in God of War II. Then CPU ceilings come down one table entry at a time,
// as far as the vendor's own cut and no further.
//
// The ceilings are written to scaling_max_freq, which on this kernel is a
// request of its own: the vendor's limits (the mode's ceiling held by IFAS, the
// thermal cooling devices, msm_performance) are separate requests and the
// lowest wins. Ours can only lower a ceiling, never lift one of theirs.

const (
	heatWindow = 60 * time.Second // the sensor's slope is taken over this long
	heatAhead  = 90 * time.Second // and projected this far ahead
	heatWait   = 30 * time.Second // between steps down: about how long a step takes to show in the sensor
	// Before a step is given back near the target. The sensor falls half a degree
	// within a minute of a step and then creeps up again with the case; given back
	// at once, the same step was taken and returned every minute.
	heatBack = 2 * time.Minute
	heatEdge = 5 * time.Second // readings averaged at each end of the window
	// Within this many degrees of the target the control law is asked to hold no
	// more GPU floor than the busy share calls for, until a degree further away.
	thriftyWithin = 3.0
)

type reading struct {
	at time.Time
	c  float64
}

// heat decides how many steps of CPU ceiling to give up to hold the sensor at
// or under target.
type heat struct {
	target float64 // C; 0 leaves the ceilings and the GPU floor alone
	max    int     // steps down to the vendor's own first cut; past it theirs is no worse than ours
	level  int
	saving bool // close enough to the target that spare GPU floor is given up
	hist   []reading
	moved  time.Time
}

// ahead is where the sensor will be after heatAhead at its current slope.
func (h *heat) ahead(now time.Time) float64 {
	var old, recent, oldAt, recentAt float64
	var nOld, nRecent int
	for _, r := range h.hist {
		if age := now.Sub(r.at); age >= heatWindow-heatEdge {
			old, oldAt, nOld = old+r.c, oldAt+age.Seconds(), nOld+1
		} else if age <= heatEdge {
			recent, recentAt, nRecent = recent+r.c, recentAt+age.Seconds(), nRecent+1
		}
	}
	if nRecent == 0 {
		return 0
	}
	recent, recentAt = recent/float64(nRecent), recentAt/float64(nRecent)
	if nOld == 0 {
		return recent // too little history for a slope
	}
	slope := (recent - old/float64(nOld)) / (oldAt/float64(nOld) - recentAt)
	return recent + slope*heatAhead.Seconds()
}

func (h *heat) step(now time.Time, c float64) int {
	h.hist = append(h.hist, reading{now, c})
	for now.Sub(h.hist[0].at) > heatWindow {
		h.hist = h.hist[1:]
	}
	if h.target == 0 {
		h.level, h.saving = 0, false
		return 0
	}
	h.saving = c >= h.target-thriftyWithin || (h.saving && c >= h.target-thriftyWithin-1)
	since := now.Sub(h.moved)
	if since < heatWait {
		return h.level
	}
	switch ahead := h.ahead(now); {
	case ahead > h.target && h.level < h.max:
		h.level, h.moved = h.level+1, now
	case ahead < h.target-1 && c < h.target-0.5 && h.level > 0 && (since >= heatBack || !h.saving):
		// Well clear of the target (a lighter game, or none) the steps come back quickly.
		h.level, h.moved = h.level-1, now
	}
	return h.level
}
