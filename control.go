package main

import (
	"fmt"
	"math"
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
	deaf      int
	lateRun   int           // consecutive ticks whose newest frames were late
	rest      time.Time     // after a rescue, stay at ample clocks until then
	moved     time.Time     // when the floors last changed
	cpuTop    time.Time     // since when the CPU floor has been at its limit while tracking; zero if it is not
	spend     time.Time     // a thrifty GPU floor left the game late: hold the full one until then
	spendWait time.Duration // how long that lasts; doubles each time it happens again

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

	// Content whose own work is a sliver of a frame: a game stream, whose frames
	// are made on another machine, or a menu. See lightShare.
	light      bool
	lightRun   int           // consecutive ticks on which the work was that small
	lightAfter time.Time     // it turned out not to be: do not call this content light again before then
	lightWait  time.Duration // how long that lasts; doubles each time it happens again
	ampleSince time.Time     // since when both floors have been at their limits; zero if they are not
	topLate    []time.Time   // recent late ticks with both floors at their limits: lateness the clocks did not cause
	adrift     bool          // called light on the strength of those, under the wider limit (driftShare)

	// Content whose rate the GPU sets: see boundBusy.
	bound          bool
	boundRun       int           // consecutive ticks with the GPU saturated at its top clock
	boundOff       int           // consecutive ticks, while bound, on which it had time to spare
	boundStop      int           // the CPU floor is not lowered below this index ...
	boundStopUntil time.Time     // ... until then: the GPU went short of work just under it
	boundWait      time.Duration // how long that lasts; doubles each time it happens again

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
	// Busy share a thrifty GPU floor must leave at the heaviest recent moment:
	// more room than a step down needs, because this floor is not open to blame.
	thriftySlack = 0.70
	spendFor     = 5 * time.Minute // how long a thrifty floor that left the game late is not tried again, at first
	// How long the CPU floor must have sat at its limit to count as staying there.
	// After a rescue the law leaves it within six seconds if anything lower will do.
	cpuTopFor = 10 * time.Second
	// How long both floors must have been at their limits before the six-second
	// rate is taken as the content's own: its six seconds and the second or two
	// the GPU's own governor needs to follow a raised floor.
	roughFor = 8 * time.Second

	// An app whose busiest thread would be busy less than this share of the time
	// at restMHz, and whose GPU would be at the GPU's lowest floor, is not being
	// held up by either clock, whatever its frames do. A stream played by Artemis
	// is the case in point: the decoder's thread is busy 2-7%, the frame rate
	// wanders between 46 and 54 with the host and the network, and the law above
	// read every dip as starvation and sat at the top clocks for a watt (4.0 W
	// against the vendor's 3.0 W) and not one more frame. Such content gets
	// restMHz and the GPU's lowest floor, and its late frames are held against
	// nothing. The lightest emulated game staged (Super Mario World) is at 17%.
	lightShare = 0.15
	lightTicks = 6 // three seconds of it before the content is called light
	// Content just under the line crosses it now and then, and each time costs a
	// look at ample clocks: Super Mario World and Metroid Fusion went in and out
	// every few seconds. Once content has left, it is not called light again for
	// this long, twice as long each time, and the rest of the law has it meanwhile.
	lightAgain = time.Minute
	// Seen from ample clocks a stream's work looks two or three times what it
	// comes to at the resting clock (a 1440p stream at 120 fps: 11-18% against
	// 6%), part of it because a thread's busy share is counted in steps of 2%.
	// lightShare alone then lets it in and out. Content that is late again and
	// again with both floors at their limits has shown that its lateness is not
	// the clocks' doing, and for that content the limit is this wider one. A game
	// let in by mistake still fills under a third of a frame at the resting clock.
	driftShare = 0.30
	driftLate  = 4                // late ticks at the limits ...
	driftFor   = 30 * time.Second // ... within this long
	// A GPU that is busy this share of the time at the top clock the vendor allows
	// is what sets the frame rate, and no CPU clock adds a frame. A game drawing as
	// fast as it can below the panel's rate is the case in point: SuperTuxKart
	// with every effect on makes 90-105 fps of 166 as the scene changes, the law
	// above read each dip as lateness and each rise as a new target, never left
	// the top clocks, and drew 19.1 W against the vendor's 16.4 W for the same
	// frames. Such content keeps the GPU's top floor, and the CPU floor comes down
	// a step a second for as long as the GPU stays saturated: a CPU floor that
	// starved it would show as GPU time to spare, whatever the frame rate does.
	// It is only looked for in content already late again and again with both
	// floors at their limits; a game that holds its rate with a full GPU is left
	// to the law above, which watches its frames.
	boundBusy      = 0.97
	boundLeave     = 0.90 // GPU time to spare: a lighter scene, or a CPU floor that starves it
	boundTicks     = 6    // three seconds saturated before the content is called GPU-bound
	boundLate      = 3    // late ticks at the limits within driftFor
	boundStepTicks = 2
	boundHold      = time.Minute // how long the floor just under which the GPU went short is kept, at first
	// Content below the panel's rate whose frames each hold a whole number of
	// refreshes (30 fps on a 60 Hz panel) can be late without losing a frame: one
	// frame misses its refresh and is held one longer, the next is ready early
	// and held one shorter, and every rate stays where it was. Xenoblade at
	// 30 fps with the GPU floor at 222 MHz showed about one such pair a second
	// (61-70% of seconds clean, against 94-100% from 434 MHz up) and no lateness
	// the rates could show. For such content frames held a refresh too long are
	// counted as well. The panel's own drift forces some (one every three
	// seconds for 29.97 fps at 60.27 Hz); this many more than those within six
	// seconds is late. On 1,082 half-seconds recorded at floors that carried
	// the game the count never got there; at 222 MHz it did on seven in eight.
	heldLate = 3
	// The count needs up to six seconds to show what a step down did, so a step
	// answers for it this long. Careful steps are ten seconds apart.
	heldUndo = 9 * time.Second
	// Below this the stock CPU governor is slow to leave its low clocks for work
	// that comes in a short burst per frame: outside games, played video lost a
	// frame a second at 570 MHz and a fifth of that from 1152 MHz up.
	restMHz = 1152
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
	// Frames of the last six seconds held at least one refresh longer than
	// wholeHold says content at the target's rate holds them; 0 where it says none.
	long6    int
	cpuLimit int // highest CPU floor index the vendor's current caps allow
	gpuLimit int // highest GPU floor index the vendor's current ceiling allows
	// The board is close to the vendor's thermal cut. In a game that keeps the
	// CPU floor at its limit, a GPU floor above what the GPU's busy share
	// calls for is then the cheapest heat to give up: late frames push the floor there on suspicion
	// (God of War II's own late frames take it to the top clock for about a watt
	// and no fewer late frames), and the next thing to go would be CPU clock that
	// is known to matter.
	thrifty bool
}

// wholeHold returns how many refreshes content at `target` holds each frame
// for when that is a whole number of two or more, and how many frames a
// second the panel then forces to hold one refresh longer because its clock
// and the content's drift apart. k is 0 for anything else: content at the
// panel's rate, where a late frame is a lost one and the rates show it, and
// content that does not divide the panel's rate (25 fps at 60 Hz), whose
// holds alternate between two lengths whatever the clocks.
func wholeHold(panelHz, target float64) (k int, forced float64) {
	if panelHz <= 0 || target <= 0 {
		return 0, 0
	}
	ratio := panelHz / target
	k = int(math.Round(ratio))
	if k < 2 || math.Abs(ratio-float64(k)) > 0.03 {
		return 0, 0
	}
	return k, math.Max(0, ratio-float64(k)) * target
}

func newController(cpuMHz, gpuMHz []int) *controller {
	c := &controller{cpuMHz: cpuMHz, gpuMHz: gpuMHz}
	c.reset()
	return c
}

func (c *controller) reset() {
	*c = controller{cpuMHz: c.cpuMHz, gpuMHz: c.gpuMHz,
		holdUntil: map[[2]int]time.Time{}, failed: map[[2]int][]time.Time{}, probeEvery: firstProbe, memory: firstMemory, turnsWait: turnsFor, spendWait: spendFor, lightWait: lightAgain, boundWait: boundHold}
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

// gpuNeed is the lowest GPU floor that keeps the GPU's busy share under
// thriftySlack at the heaviest recent moment, or the vendor's limit while
// nothing is known. It does not allow for the busiest thread and the GPU
// taking turns within a frame, as a step down does: in God of War II that
// asked for 900 MHz and more, where fixed floors of 525 MHz dropped no more
// frames than 1100 MHz. A game it does starve gets its floor back (rescue-gpu).
func (c *controller) gpuNeed(now time.Time, limit int) int {
	_, work, _ := c.heaviest(now)
	if work == 0 || c.target == 0 {
		return limit
	}
	for i, mhz := range c.gpuMHz[:limit+1] {
		if work*c.target/float64(mhz) < thriftySlack {
			return i
		}
	}
	return limit
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
	gpuAllowed := s.gpuLimit
	if c.cpu < s.cpuLimit || c.state != track {
		c.cpuTop = time.Time{}
	} else if c.cpuTop.IsZero() {
		c.cpuTop = s.now
	}
	if s.thrifty && !s.now.Before(c.spend) && c.target >= s.panelHz*nearPanel &&
		!c.cpuTop.IsZero() && s.now.Sub(c.cpuTop) >= cpuTopFor {
		// From here on this is the most GPU floor there is: a single late frame with
		// both floors at their limits is held against nothing.
		//
		// Only for content at the panel's rate, where a frame's share of the GPU
		// says whether it fits its refresh. Slower content does not have a whole
		// frame time to spare: each frame still has to land on its refresh, and
		// with this applied Harry Potter (25 fps) lost seven seconds in ten to
		// uneven frames at a GPU busy share of 45%. And only where the CPU floor
		// stays at its limit anyway, as bursts keep it in God of War II: elsewhere
		// the law trades one clock for the other, and a GPU floor held down makes
		// it pay in CPU clock (Mario Galaxy, +0.5 W).
		s.gpuLimit = c.gpuNeed(s.now, s.gpuLimit)
	}
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
			c.light, c.lightRun, c.lightAfter, c.lightWait, c.topLate = false, 0, time.Time{}, lightAgain, nil
			c.bound, c.boundRun, c.boundStopUntil, c.boundWait = false, 0, time.Time{}, boundHold
			c.enter(idle, s.now)
		}
		return "idle"
	}
	c.idle = 0
	c.ticks++
	if c.cpu < s.cpuLimit || c.gpu < s.gpuLimit {
		c.ampleSince = time.Time{}
	} else if c.ampleSince.IsZero() {
		c.ampleSince = s.now
	}

	rest := 0
	for rest < s.cpuLimit && c.cpuMHz[rest] < restMHz {
		rest++
	}
	for len(c.topLate) > 0 && s.now.Sub(c.topLate[0]) > driftFor {
		c.topLate = c.topLate[1:]
	}
	limit := lightShare
	if len(c.topLate) >= driftLate || (c.light && c.adrift) {
		limit = driftShare
	}
	if s.cpuNow > 0 {
		// A GPU that was switched off for the whole tick reports no clock: no work.
		// The stock governor climbing well above the resting clock (demand) means
		// bursts that an average share does not show: not light either.
		if s.hotBusy*float64(s.cpuNow)/float64(c.cpuMHz[rest]) < limit && s.gpuBusy*float64(s.gpuNow)/float64(c.gpuMHz[0]) < limit &&
			!(c.light && s.demand > c.cpuMHz[rest]) {
			c.lightRun++
		} else {
			c.lightRun = 0
		}
	}
	switch {
	case c.lightRun >= lightTicks && (limit == driftShare || !s.now.Before(c.lightAfter)):
		was := c.light
		c.bound = false
		if !was {
			c.adrift = limit == driftShare
			c.enter(track, s.now)
		}
		// The target is only what the status shows here: nothing is judged against it.
		c.light, c.cpu, c.gpu, c.target = true, rest, 0, s.fps6
		if !was {
			return "light"
		}
		return ""
	case c.light:
		// It has work to do after all (the game behind the menu started): look afresh.
		c.light, c.topLate = false, nil
		c.lightAfter = s.now.Add(c.lightWait)
		c.lightWait = min(c.lightWait*2, maxMemory)
		c.cpu, c.gpu = s.cpuLimit, s.gpuLimit
		c.enter(learn, s.now)
		return "learn"
	}

	if saturated := s.gpuBusy >= boundBusy && s.gpuNow*10 >= c.gpuMHz[gpuAllowed]*9; saturated {
		c.boundRun++
	} else {
		c.boundRun = 0
	}
	switch {
	case c.bound && s.gpuBusy < boundLeave && c.boundOff >= 1:
		// A second with GPU time to spare. If the scene got lighter, ample clocks
		// will show what it wants now; if the CPU floor was starving the GPU, they
		// will show it saturated again, and the floor then stops above this one.
		c.boundStop, c.boundStopUntil = c.cpu+1, s.now.Add(c.boundWait)
		c.boundWait = min(c.boundWait*2, maxMemory)
		c.bound, c.boundRun, c.topLate = false, 0, nil
		c.cpu, c.gpu = s.cpuLimit, gpuAllowed
		c.enter(learn, s.now)
		return "learn"
	case c.bound:
		c.boundOff = 0
		if s.gpuBusy < boundLeave {
			c.boundOff = 1
		}
		// The target is only what the status shows here: nothing is judged against it.
		c.gpu, c.target = gpuAllowed, s.fps6
		lowest := rest
		if s.now.Before(c.boundStopUntil) {
			lowest = max(lowest, c.boundStop)
		}
		if c.boundRun >= boundStepTicks && c.cpu > lowest {
			c.cpu--
			c.boundRun = 0
			return fmt.Sprintf("bound-cpu%d", c.cpuMHz[c.cpu])
		}
		return ""
	case c.boundRun >= boundTicks && len(c.topLate) >= boundLate && c.cpu >= s.cpuLimit && c.gpu >= gpuAllowed && s.fps < s.panelHz*nearPanel:
		c.bound, c.boundRun, c.boundOff = true, 0, 0
		c.enter(track, s.now)
		c.target = s.fps6
		return "bound"
	}

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
	held := false // late by frames held too long, which no rate shows: see heldLate
	if c.deaf > 0 {
		c.deaf--
	} else {
		sustained := s.now.Sub(c.lateSince) >= 6*time.Second
		// Not against a target that is still a one-second count: what the panel
		// forces is worked out from it.
		if k, forced := wholeHold(s.panelHz, c.target); k > 0 && sustained && !c.rough {
			held = s.long6 >= int(math.Ceil(6*forced-1e-9))+heldLate
		}
		late = s.fps < c.target*(1-lateNow) || (sustained && s.fps6 < c.target*(1-lateSustained)) || held
	}
	if !late {
		c.lateRun = 0
	} else {
		c.lateRun++
		c.lateSince, c.deaf = s.now, deafTicks
		c.good, c.fast = 0, false
		switch {
		case c.cpu >= s.cpuLimit && c.gpu >= s.gpuLimit && s.gpuLimit < gpuAllowed && c.lateRun >= 2:
			// Late for a second running with GPU floor held back: the busy shares were
			// wrong about what this game's GPU needs. Give it all back for a while,
			// longer each time.
			c.spend = s.now.Add(c.spendWait)
			c.spendWait = min(c.spendWait*2, turnsMax)
			s.gpuLimit = gpuAllowed
			c.gpu = gpuAllowed
			return "rescue-gpu"
		case c.cpu >= s.cpuLimit && c.gpu >= s.gpuLimit:
			c.topLate = append(c.topLate, s.now)
			if c.lateRun >= 4 {
				// Late for two seconds running with nothing more to give: the content
				// itself slowed down. Find out what it wants now.
				c.enter(learn, s.now)
				return "relearn"
			}
			if c.rough && !c.ampleSince.IsZero() && s.now.Sub(c.ampleSince) >= roughFor {
				// The target is still the one-second count it was set from, and that
				// count came out high (62 frames where the game makes 60.3: seen about
				// once in fifty). Against it the six-second rate is short for ever, so
				// the on-time stretch that would replace it never comes and the floors
				// never leave their limits. Everything shown since they got there is
				// what the content does with ample clocks: take that.
				c.target, c.rough = s.fps6, false
				return fmt.Sprintf("target-%.1f", c.target)
			}
			// A late frame with nothing more to give is the game's own.
			return "uneven"
		case c.lateRun == 1 && (s.now.Sub(c.lastDown) < 3*time.Second || (held && s.now.Sub(c.lastDown) < heldUndo)) && c.lastDownTo == [2]int{c.cpu, c.gpu}:
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
