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
	holdUntil  map[[2]int]time.Time // do not step down to these floors before then
	fails      map[[2]int]int
	nextProbe  time.Time
	probeEvery time.Duration
	busyRef    float64 // CPU plus GPU busy share when the current floors were settled on
	busyOff    int     // consecutive ticks the busy share has been far from that
	// A hold count the content was seen to keep even at ample clocks (a game that
	// caps itself at half the panel rate), so sitting on it is not suspicious.
	confirmedLock int
	lockAtProbe   int       // the hold count the frames were locked to when the probe began
	lateSince     time.Time // start of the 6 s window used for mild sustained lateness
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
	overlap       = 1.30  // CPU and GPU busy shares adding up past this run side by side, not in turn
	idleTicks     = 10
	firstProbe    = 20 * time.Second
	maxProbe      = 120 * time.Second
	busyShift     = 0.15 // a change in busy share this large at an unchanged rate means the scene changed
	// Content on its own clock drifts against the panel and shows an odd hold every
	// few seconds (3 s for 59.94, 7 s for 30.00 on this 60.27 Hz panel). Frames that
	// hold exactly the same number of refreshes for this long are locked to the
	// panel instead, which is what a starved game looks like.
	lockedLong = 12 * time.Second
	firstHold  = 30 * time.Second
)

// sample is one tick of measurements for the app in front.
type sample struct {
	now     time.Time
	flowing bool    // the app presented at least a few frames in the last second
	fps     float64 // delivered rate over about the last second of frames
	fps6    float64 // the same over about six seconds
	panelHz float64
	gpuBusy float64 // GPU busy share of wall time
	hotBusy float64 // busy share of the app's busiest thread
	cpuNow  int     // MHz the busiest thread's cores actually ran at
	gpuNow  int     // MHz the GPU actually ran at
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
		holdUntil: map[[2]int]time.Time{}, fails: map[[2]int]int{}, probeEvery: firstProbe}
}

func (c *controller) enter(s state, now time.Time) {
	c.state, c.ticks, c.good, c.above = s, 0, 0, 0
	c.lateSince = now
}

// step consumes one sample, updates the two floors and names what it did.
func (c *controller) step(s sample) (action string) {
	defer func() {
		c.cpu, c.gpu = min(c.cpu, s.cpuLimit), min(c.gpu, s.gpuLimit)
	}()
	if !s.flowing {
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
		return fmt.Sprintf("target-%.1f", c.target)

	case probe:
		c.cpu, c.gpu = s.cpuLimit, s.gpuLimit
		if c.ticks < learnTicks {
			return "probing"
		}
		if s.fps > c.target*(1+fasterBy) {
			// The content had more to give: it was being held on a lower rate.
			c.target, c.fast, c.probeEvery, c.confirmedLock = s.fps, true, firstProbe, 0
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
			c.nextProbe, c.lateSince = s.now.Add(c.probeEvery), s.now
			return fmt.Sprintf("target-up-%.1f", c.target)
		}
	} else {
		c.above = 0
	}

	late := s.fps < c.target*(1-lateNow) ||
		(s.now.Sub(c.lateSince) >= 6*time.Second && s.fps6 < c.target*(1-lateSustained))
	if late {
		c.good, c.fast = 0, false
		if s.now.Sub(c.lastDown) < 3*time.Second && c.lastDownTo == [2]int{c.cpu, c.gpu} {
			// The step we just took was one too many. Undo it and leave that setting
			// alone for a while, longer each time it fails.
			c.fails[c.lastDownTo]++
			c.holdUntil[c.lastDownTo] = s.now.Add(firstHold << min(c.fails[c.lastDownTo]-1, 5))
			c.cpu, c.gpu = c.undo[0], c.undo[1]
			c.lateSince = s.now
			return "undo-late"
		}
		// Late for a reason we did not cause: the scene got heavier, or the content
		// changed rate. Either way ample clocks answer it.
		c.cpu, c.gpu = s.cpuLimit, s.gpuLimit
		c.enter(learn, s.now)
		return "rescue-late"
	}

	c.good++
	// A scene that changes while it is being held on a clean lower rate gives no
	// lateness to notice, but its busy share moves. Treat that as a reason to look.
	busy := s.hotBusy + s.gpuBusy
	if c.good == 1 || c.busyRef == 0 {
		c.busyRef = busy
	}
	if d := busy - c.busyRef; d > busyShift || d < -busyShift {
		c.busyOff++
	} else {
		c.busyOff = 0
	}
	locked := s.lockK >= 2 && s.lockedFor >= lockedLong && s.lockK != c.confirmedLock
	sceneChanged := c.busyOff >= 2 || locked
	// Content already at the panel's rate cannot want more; anything slower might.
	if c.target < s.panelHz*0.985 && (sceneChanged || s.now.After(c.nextProbe)) && (c.cpu < s.cpuLimit || c.gpu < s.gpuLimit) {
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
	if c.good < need {
		return ""
	}

	// Candidate steps down and the busy shares each would leave behind.
	type cand struct {
		to       [2]int
		hot, gpu float64
		name     string
	}
	var cands []cand
	if c.cpu > 0 {
		hot := s.hotBusy // a floor the CPU governor already runs above costs nothing to lower
		if s.cpuNow <= c.cpuMHz[c.cpu] {
			hot = s.hotBusy * float64(s.cpuNow) / float64(c.cpuMHz[c.cpu-1])
		}
		cands = append(cands, cand{[2]int{c.cpu - 1, c.gpu}, hot, s.gpuBusy, fmt.Sprintf("cpu%d", c.cpuMHz[c.cpu-1])})
	}
	if c.gpu > 0 {
		gpu := s.gpuBusy
		if s.gpuNow <= c.gpuMHz[c.gpu] {
			gpu = s.gpuBusy * float64(s.gpuNow) / float64(c.gpuMHz[c.gpu-1])
		}
		cands = append(cands, cand{[2]int{c.cpu, c.gpu - 1}, s.hotBusy, gpu, fmt.Sprintf("gpu%d", c.gpuMHz[c.gpu-1])})
	}
	best := -1
	for i, k := range cands {
		ok := k.hot < slack && k.gpu < slack
		// A thread and a GPU that take turns share one frame between them.
		if s.hotBusy+s.gpuBusy < overlap {
			ok = ok && k.hot+k.gpu < slack
		}
		if !ok || s.now.Before(c.holdUntil[k.to]) {
			continue
		}
		if best < 0 || k.hot+k.gpu < cands[best].hot+cands[best].gpu {
			best = i
		}
	}
	if best < 0 {
		c.fast = false // nothing safe left to lower: the quick descent is over
		return ""
	}
	c.undo = [2]int{c.cpu, c.gpu}
	c.cpu, c.gpu = cands[best].to[0], cands[best].to[1]
	c.lastDown, c.lastDownTo = s.now, cands[best].to
	c.good = 0
	return "down-" + cands[best].name
}
