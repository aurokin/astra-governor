package main

import (
	"fmt"
	"time"
)

// controller is the frame-rate control law. It holds no I/O: the daemon feeds
// it one sample per tick and applies the two floors it leaves behind.
//
// The first version of this daemon started from the vendor's low clocks and
// raised them when frames arrived late. That fails whenever a starved game
// settles on a clean half rate: a 60 fps game held at exactly 30 looks like a
// healthy 30 fps game, and nothing ever raises the clocks. This version works
// the other way round. It first gives the game ample clocks, takes the rate
// the game reaches there as what the content wants, and only then lowers the
// clocks step by step while that rate holds. Anything it cannot explain sends
// it back to ample clocks to look again.
//
// Two things it has learned from real games since:
//
//   - Many games drop a frame now and then whatever the clocks (God of War II
//     at the same moments of a scene on every run, in Diablo too). A late frame
//     with both floors at the vendor's limits is therefore held against
//     nothing; held against the floors, it blocks every setting at once.
//   - Average busy shares do not show short bursts. The same game's bursts
//     need its cores near their top clock although its busiest thread is busy
//     a third of the time. The stock CPU governor does show them: it climbs
//     for each burst, too late to save that frame. How high it climbs is
//     taken as how high the floor has to be.
type controller struct {
	cpuMHz []int // CPU floor choices, ascending; index 0 is the lowest clock, i.e. no floor
	gpuMHz []int // GPU floor choices, ascending; index 0 is the vendor's own floor

	cpu, gpu int // floors to ask for, as indices into the tables above

	state      state
	ticks      int     // ticks spent in the current state
	target     float64 // the rate the content reached with ample clocks
	fast       bool    // descending quickly after a look at ample clocks
	good       int     // consecutive on-time ticks while tracking
	above      int     // consecutive ticks the game ran faster than the target
	idle       int
	saved      [2]int // floors to return to after a probe that found nothing
	lastDown   time.Time
	lastDownTo [2]int
	undo       [2]int
	holdUntil  map[[2]int]time.Time   // do not step down to these floors before then
	failed     map[[2]int][]time.Time // when each setting failed, within maxMemory
	nextProbe  time.Time
	probeEvery time.Duration
	busyRef    float64 // CPU plus GPU busy share when the current floors were settled on
	busyOff    int     // consecutive ticks the busy share has been far from that
	// A hold count the content was seen to keep even at ample clocks (a game that
	// caps itself at half the panel rate), so sitting on it is not suspicious.
	confirmedLock int
	lockAtProbe   int       // the hold count the frames were locked to when the probe began
	lateSince     time.Time // start of the 6 s window used for mild sustained lateness
	rough         bool      // the target is still a one-second count, to be replaced by a six-second one
	// A late frame stays inside the one-second rate for two more ticks. Those are
	// judged on the newest frames alone, so one late frame is one event.
	deaf    int
	lateRun int       // consecutive ticks whose newest frames were late
	rest    time.Time // after a rescue, stay at ample clocks until then
	moved   time.Time // when the floors last changed

	// Whether the busiest thread and the GPU take turns within a frame or work
	// side by side decides how far the clocks can come down: taking turns, their
	// busy shares must fit into one frame together; side by side, each has a whole
	// frame. Busy shares that add up to more than a frame prove the second case.
	// Until then a step past the take-turns limit is a trial, and a failed trial
	// settles the first case for a while.
	sideBySide  bool
	over        int           // consecutive on-time ticks with the busy shares adding up past a frame
	turnsUntil  time.Time     // a trial failed: assume taking turns until then
	turnsWait   time.Duration // how long that lasts; doubles each time trials fail again
	trial       bool          // floors are below trialBase on the strength of trials
	trialBase   [2]int        // floors before the first trial step
	lastDownWas bool          // the last step down was a trial

	// Steps down are judged against the heaviest recent moment, not the current
	// one, so a scene that is heavy now and then keeps the clocks it needs then.
	recent []work
	memory time.Duration // how far back "recent" reaches; doubles each time a heavy moment catches the floors out
}

// work is what one frame cost at one on-time moment, as busy share times clock
// (MHz) per frame per second, so it can be scaled to any clock and frame rate.
type work struct {
	at       time.Time
	cpu, gpu float64
	demand   int // MHz the stock CPU governor was reaching above our floor, 0 if it stayed on it
}

type state int

const (
	idle  state = iota // nothing presenting
	learn              // ample clocks, finding out what the content wants
	track              // holding the target with as little as possible
	probe              // a brief look at ample clocks to see if the content wants more
)

func (s state) String() string { return [...]string{"idle", "learn", "track", "probe"}[s] }

// Tuning. A tick is half a second.
const (
	learnTicks    = 4     // two seconds at ample clocks before trusting the rate
	lateNow       = 0.025 // short-window shortfall that counts as late (1.5 frames/s at 60)
	lateSustained = 0.011 // 6 s shortfall that counts as late (4 frames in 6 s at 60)
	fasterBy      = 0.025 // running this far above the target means the content sped up
	slowDownTicks = 20    // on-time ticks before a careful step down
	fastDownTicks = 2     // the same while descending after a look at ample clocks
	slack         = 0.80  // a step down must leave the predicted busy share below this
	overlap       = 1.10  // CPU and GPU busy shares adding up past this cannot be taking turns
	overlapTicks  = 3
	turnsFor      = 10 * time.Minute // how long a failed trial rules out further ones, at first
	turnsMax      = 80 * time.Minute
	firstMemory   = time.Minute
	maxMemory     = 16 * time.Minute
	idleTicks     = 10
	firstProbe    = 20 * time.Second
	maxProbe      = 120 * time.Second
	busyShift     = 0.15 // a change in busy share this large at an unchanged rate means the scene changed
	settleTicks   = 4    // ticks after a change of floors before busy shares are trusted
	// After a look at ample clocks the GPU's own governor takes two to three
	// seconds to come back down to the floor, and its busy share with it.
	refTicks  = 8
	nearPanel = 0.97 // content this close to the panel's rate has nothing more to ask for
	// Content on its own clock drifts against the panel and shows an odd hold every
	// few seconds (3 s for 59.94, 7 s for 30.00 on this 60.27 Hz panel). Frames that
	// hold exactly the same number of refreshes for this long are locked to the
	// panel instead, which is what a starved game looks like.
	lockedLong = 12 * time.Second
	firstHold  = 30 * time.Second

	deafTicks = 2
	shortNow  = 0.9             // frames missing from the newest 0.4 s that count as late again
	restFor   = 5 * time.Second // at ample clocks after a rescue, so the heavy moment is seen before stepping down
)

// sample is one tick of measurements for the app in front.
type sample struct {
	now     time.Time
	flowing bool    // the app presented at least a few frames in the last second
	fps     float64 // delivered rate over about the last second of frames
	fps6    float64 // the same over about six seconds
	fpsNow  float64 // the same over the newest 0.4 s, which a late frame leaves after one tick
	panelHz float64
	gpuBusy float64 // GPU busy share of wall time
	hotBusy float64 // busy share of the app's busiest thread
	cpuNow  int     // MHz the busiest thread's cores actually ran at
	// MHz those cores reached or passed for a tenth of the last ten seconds, if
	// that is above our floor; 0 if the stock governor stayed on the floor.
	demand int
	gpuNow int // MHz the GPU actually ran at
	// Every recent frame held exactly lockK refreshes (0 if the holds are mixed),
	// and has done for lockedFor.
	lockK     int
	lockedFor time.Duration
	cpuLimit  int // highest CPU floor index the vendor's current caps allow
	gpuLimit  int // highest GPU floor index the vendor's current ceiling allows
}

func newController(cpuMHz, gpuMHz []int) *controller {
	c := &controller{cpuMHz: cpuMHz, gpuMHz: gpuMHz}
	c.reset()
	return c
}

func (c *controller) reset() {
	*c = controller{cpuMHz: c.cpuMHz, gpuMHz: c.gpuMHz,
		holdUntil: map[[2]int]time.Time{}, failed: map[[2]int][]time.Time{}, probeEvery: firstProbe, memory: firstMemory, turnsWait: turnsFor}
}

func (c *controller) enter(s state, now time.Time) {
	c.state, c.ticks, c.good, c.above = s, 0, 0, 0
	c.lateSince = now
	if s != probe { // a probe returns to the floors it left; anything else starts again from ample clocks
		c.trial = false
	}
}

// blocked reports whether a setting is being left alone: it failed recently, or
// it is no faster in either clock than one that did.
func (c *controller) blocked(to [2]int, now time.Time) bool {
	for failed, until := range c.holdUntil {
		if now.Before(until) && to[0] <= failed[0] && to[1] <= failed[1] {
			return true
		}
	}
	return false
}

// heaviest returns the most a frame has cost within the controller's memory,
// and the highest clock the stock CPU governor reached for in that time.
func (c *controller) heaviest(now time.Time) (cpu, gpu float64, demand int) {
	for _, w := range c.recent {
		if now.Sub(w.at) <= c.memory {
			cpu, gpu, demand = max(cpu, w.cpu), max(gpu, w.gpu), max(demand, w.demand)
		}
	}
	return cpu, gpu, demand
}

// fail records that a setting could not hold the target and leaves it alone
// for a while, longer the more often it has failed lately.
func (c *controller) fail(at [2]int, now time.Time) {
	var recent []time.Time
	for _, t := range c.failed[at] {
		if now.Sub(t) < maxMemory {
			recent = append(recent, t)
		}
	}
	c.failed[at] = append(recent, now)
	c.holdUntil[at] = now.Add(firstHold << min(len(recent), 5))
}

// noTrials rules out steps past the take-turns limit for a while, longer each
// time, so a steady game is not made late by the same experiment every ten minutes.
func (c *controller) noTrials(now time.Time) {
	c.turnsUntil, c.trial = now.Add(c.turnsWait), false
	c.turnsWait = min(c.turnsWait*2, turnsMax)
}

// note keeps what a frame cost at this on-time moment. The GPU's busy figure
// trails a change of floors by a second or so; paired with the new clock it
// would describe a moment that never happened.
func (c *controller) note(s sample) {
	if s.fps > 0 && s.cpuNow > 0 && s.gpuNow > 0 && s.now.Sub(c.moved) >= settleTicks*500*time.Millisecond {
		c.recent = append(c.recent, work{s.now, s.hotBusy * float64(s.cpuNow) / s.fps, s.gpuBusy * float64(s.gpuNow) / s.fps, s.demand})
		for s.now.Sub(c.recent[0].at) > maxMemory {
			c.recent = c.recent[1:]
		}
	}
}

// step consumes one sample, updates the two floors and names what it did.
func (c *controller) step(s sample) (action string) {
	defer func(cpu, gpu int) {
		c.cpu, c.gpu = min(c.cpu, s.cpuLimit), min(c.gpu, s.gpuLimit)
		if c.cpu != cpu || c.gpu != gpu {
			c.moved = s.now
		}
	}(c.cpu, c.gpu)
	if !s.flowing {
		// A gap in the frames is not lateness once they resume: let it leave the rates first.
		c.deaf, c.lateSince, c.lateRun = deafTicks, s.now, 0
		if s.gpuBusy > 0.15 {
			return "blind" // the GPU is working but no frames were seen: keep what we have
		}
		// Paused, loading or screen off. Drop the floors after a few seconds; whatever
		// comes next may be a different scene, so it starts with a fresh look.
		if c.idle++; c.idle >= idleTicks && c.state != idle {
			c.cpu, c.gpu = 0, 0
			c.enter(idle, s.now)
		}
		return "idle"
	}
	c.idle = 0
	c.ticks++

	switch c.state {
	case idle:
		c.cpu, c.gpu = s.cpuLimit, s.gpuLimit
		c.enter(learn, s.now)
		return "learn"

	case learn:
		c.cpu, c.gpu = s.cpuLimit, s.gpuLimit
		if c.ticks < learnTicks {
			return "learning"
		}
		c.target, c.fast, c.probeEvery, c.confirmedLock = s.fps, true, firstProbe, 0
		c.nextProbe = s.now.Add(c.probeEvery)
		c.enter(track, s.now)
		c.rough = true
		return fmt.Sprintf("target-%.1f", c.target)

	case probe:
		c.cpu, c.gpu = s.cpuLimit, s.gpuLimit
		if c.ticks < learnTicks {
			return "probing"
		}
		if s.fps > c.target*(1+fasterBy) {
			// The content had more to give: it was being held on a lower rate.
			c.target, c.fast, c.probeEvery, c.confirmedLock = s.fps, true, firstProbe, 0
			c.rough = true
			action = fmt.Sprintf("probe-found-%.1f", c.target)
		} else {
			c.cpu, c.gpu = c.saved[0], c.saved[1]
			c.probeEvery = min(c.probeEvery*2, maxProbe)
			c.confirmedLock = c.lockAtProbe
			action = "probe-same"
		}
		c.nextProbe = s.now.Add(c.probeEvery)
		c.enter(track, s.now)
		return action
	}

	// Tracking.
	if s.fps > c.target*(1+fasterBy) {
		// Running faster than the target with clocks to spare: the content sped up
		// (a 30 fps cutscene ended) and the clocks happened to be enough for it.
		if c.above++; c.above >= 2 {
			c.target, c.above, c.probeEvery = s.fps, 0, firstProbe
			c.nextProbe, c.lateSince, c.rough = s.now.Add(c.probeEvery), s.now, true
			return fmt.Sprintf("target-up-%.1f", c.target)
		}
	} else {
		c.above = 0
	}

	// While a late frame already dealt with is still inside the one-second rate,
	// only the newest frames can show that lateness goes on.
	late := (c.target-s.fpsNow)*0.4 >= shortNow
	if c.deaf > 0 {
		c.deaf--
	} else {
		late = s.fps < c.target*(1-lateNow) ||
			(s.now.Sub(c.lateSince) >= 6*time.Second && s.fps6 < c.target*(1-lateSustained))
	}
	if !late {
		c.lateRun = 0
	} else {
		c.lateRun++
		c.lateSince, c.deaf = s.now, deafTicks
		c.good, c.fast = 0, false
		switch {
		case c.cpu >= s.cpuLimit && c.gpu >= s.gpuLimit:
			if c.lateRun >= 4 {
				// Late for two seconds running with nothing more to give: the content
				// itself slowed down. Find out what it wants now.
				c.enter(learn, s.now)
				return "relearn"
			}
			// A late frame with nothing more to give is the game's own.
			return "uneven"
		case c.lateRun == 1 && s.now.Sub(c.lastDown) < 3*time.Second && c.lastDownTo == [2]int{c.cpu, c.gpu}:
			// The step we just took was one too many. Undo it and leave that setting
			// alone for a while, longer each time it fails.
			c.fail(c.lastDownTo, s.now)
			c.cpu, c.gpu = c.undo[0], c.undo[1]
			if c.lastDownWas {
				// The trial found the edge, so this thread and the GPU take turns. Go
				// back to where the trials began: the settings in between hold the
				// target, but with nothing to spare.
				c.noTrials(s.now)
				c.cpu, c.gpu = c.trialBase[0], c.trialBase[1]
				return "undo-trial"
			}
			return "undo-late"
		}
		// Late for a reason we did not cause by a step: the scene got heavier, or the
		// content changed rate. Ample clocks answer the first at once, and these
		// floors have shown they cannot carry this game through its heavier moments.
		// (If it is the second, frames stay late at ample clocks and the case above
		// learns the new rate.)
		c.fail([2]int{c.cpu, c.gpu}, s.now)
		if c.trial {
			// Floors reached by trials gave way, which settles that this thread and
			// the GPU take turns as surely as an undone trial does.
			c.noTrials(s.now)
		}
		c.memory = min(c.memory*2, maxMemory)
		c.cpu, c.gpu = s.cpuLimit, s.gpuLimit
		c.trial, c.fast, c.rest = false, true, s.now.Add(restFor)
		return "rescue-late"
	}

	c.good++
	if c.rough && s.now.Sub(c.lateSince) >= 6*time.Second {
		// Six seconds on time since the target was set: all of them were at this rate.
		c.target, c.rough = s.fps6, false
	}
	c.note(s)
	if s.demand > c.cpuMHz[c.cpu] && c.cpu < s.cpuLimit {
		// The stock governor keeps climbing past our floor: bursts we would otherwise
		// meet a frame late. Go where it goes, and wait longer before trying lower.
		for c.cpu < s.cpuLimit && c.cpuMHz[c.cpu] < s.demand {
			c.cpu++
		}
		c.recent = append(c.recent, work{at: s.now, demand: s.demand})
		c.memory = min(c.memory*2, maxMemory)
		c.good = 0
		return fmt.Sprintf("up-cpu%d", c.cpuMHz[c.cpu])
	}
	// A scene that changes while it is being held on a clean lower rate gives no
	// lateness to notice, but its busy share moves. Treat that as a reason to look.
	busy := s.hotBusy + s.gpuBusy
	if busy >= overlap {
		if c.over++; c.over >= overlapTicks {
			c.sideBySide = true
		}
	} else {
		c.over = 0
	}
	// The GPU's busy figure trails a change of floors by a second or so, so the
	// reference is taken, and compared against, only once it has caught up.
	if c.good <= refTicks {
		c.busyRef, c.busyOff = busy, 0
	} else if d := busy - c.busyRef; d > busyShift || d < -busyShift {
		c.busyOff++
	} else {
		c.busyOff = 0
	}
	locked := s.lockK >= 2 && s.lockedFor >= lockedLong && s.lockK != c.confirmedLock
	sceneChanged := c.busyOff >= 2 || locked
	// Content already at the panel's rate cannot want more; anything slower might.
	if c.target < s.panelHz*nearPanel && (sceneChanged || s.now.After(c.nextProbe)) && (c.cpu < s.cpuLimit || c.gpu < s.gpuLimit) {
		if sceneChanged {
			c.probeEvery = firstProbe
		}
		c.busyOff, c.lockAtProbe = 0, 0
		if s.lockedFor >= lockedLong {
			c.lockAtProbe = s.lockK
		}
		c.saved = [2]int{c.cpu, c.gpu}
		c.cpu, c.gpu = s.cpuLimit, s.gpuLimit
		c.enter(probe, s.now)
		return "probe"
	}
	need := slowDownTicks
	if c.fast {
		need = fastDownTicks
	}
	if c.good < need || s.now.Before(c.rest) {
		return ""
	}

	// Candidate steps down, and the busy shares each would leave behind at the
	// heaviest recent moment if the clocks then sat exactly on the floors. The
	// stock governors may well run above a floor, but they are what lets a game
	// starve in the first place, so nothing is assumed of them.
	type cand struct {
		to       [2]int
		hot, gpu float64
		name     string
	}
	cpuWork, gpuWork, demand := c.heaviest(s.now)
	share := func(to [2]int) (hot, gpu float64) {
		return cpuWork * c.target / float64(c.cpuMHz[to[0]]), gpuWork * c.target / float64(c.gpuMHz[to[1]])
	}
	var cands []cand
	if c.cpu > 0 && c.cpuMHz[c.cpu-1] >= demand {
		to := [2]int{c.cpu - 1, c.gpu}
		hot, gpu := share(to)
		cands = append(cands, cand{to, hot, gpu, fmt.Sprintf("cpu%d", c.cpuMHz[to[0]])})
	}
	if c.gpu > 0 {
		to := [2]int{c.cpu, c.gpu - 1}
		hot, gpu := share(to)
		cands = append(cands, cand{to, hot, gpu, fmt.Sprintf("gpu%d", c.gpuMHz[to[1]])})
	}
	best, bestTrial := -1, false
	for i, k := range cands {
		if k.hot >= slack || k.gpu >= slack || c.blocked(k.to, s.now) {
			continue
		}
		// Past the take-turns limit a step is a trial, unless the two are known to
		// work side by side; no trials while a failed one is remembered.
		trial := !c.sideBySide && k.hot+k.gpu >= slack
		if trial && s.now.Before(c.turnsUntil) {
			continue
		}
		better := best < 0 || (bestTrial && !trial) ||
			(trial == bestTrial && k.hot+k.gpu < cands[best].hot+cands[best].gpu)
		if better {
			best, bestTrial = i, trial
		}
	}
	if best < 0 || (bestTrial && c.fast) {
		c.fast = false // nothing safe left to lower quickly: the rest goes one careful step at a time
		return ""
	}
	if bestTrial && !c.trial {
		c.trial, c.trialBase = true, [2]int{c.cpu, c.gpu}
	}
	c.undo = [2]int{c.cpu, c.gpu}
	c.cpu, c.gpu = cands[best].to[0], cands[best].to[1]
	c.lastDown, c.lastDownTo, c.lastDownWas = s.now, cands[best].to, bestTrial
	c.good = 0
	if bestTrial {
		return "try-" + cands[best].name
	}
	return "down-" + cands[best].name
}
