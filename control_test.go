package main

import (
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

const panel = 60.2721

var (
	cpuTable = []int{384, 960, 1363, 1785, 2227, 2745, 2918}
	gpuTable = []int{222, 342, 389, 443, 525, 607, 660, 734, 832}
)

// game is a stand-in for an emulator-shaped app: every frame costs a fixed
// amount of CPU work and then a fixed amount of GPU work, and the content has
// its own clock. A frame that misses its slot waits for the next refresh, so a
// starved game settles on a clean fraction of the panel's rate.
type game struct {
	cpuMs, gpuMs float64 // work per frame with both clocks at the top of their tables
	fps          float64
	sideBySide   bool // the CPU and GPU work overlap (a threaded renderer) instead of taking turns
	// The game drops two frames by itself about this often, whatever the clocks
	// (a shader compile, a disc read). Zero means never.
	hitchEvery time.Duration
	// Every burstEvery one frame needs the CPU at burstMHz or it and the next
	// are late. The stock governor climbs to burstMHz for each burst, a frame too late.
	burstMHz   int
	burstEvery time.Duration
	// Work per frame done in turn with the rest by threads other than the busiest,
	// which no busy share the controller sees accounts for.
	hiddenMs float64
}

func (g game) run(cpuMHz, gpuMHz int) (fps, hot, gpu float64) {
	ct := g.cpuMs * float64(cpuTable[len(cpuTable)-1]) / float64(cpuMHz)
	gt := g.gpuMs * float64(gpuTable[len(gpuTable)-1]) / float64(gpuMHz)
	fps = g.fps
	frame := ct + gt + g.hiddenMs
	if g.sideBySide {
		frame = math.Max(ct, gt)
	}
	if frame > 1000/g.fps {
		fps = panel / math.Ceil(frame*panel/1000)
	}
	return fps, ct * fps / 1000, gt * fps / 1000
}

// rig runs a controller against a game whose stock governors are blind: the
// clocks sit exactly on the floors the controller sets.
type rig struct {
	c        *controller
	now      time.Time
	hist     []float64
	actions  []string
	limits   [2]int
	lateTick int
	locked   time.Duration // how long the delivered rate has sat exactly on a fraction of the panel rate
	// GPU busy shares not yet reported. After a step down the GPU's own governor
	// takes up to three seconds to come down to the new floor and the busy share
	// trails with it; a raised floor takes effect at once.
	gpuSeen  []float64
	gpuWas   int
	rnd      *rand.Rand
	hitchAt  time.Time // the game's next own late frame
	cpuWas   int
	cpuSince time.Time // when the CPU floor last changed
	echo     int       // ticks for which the last one is still inside the one-second rate
	hitches  int
	ampleFor int // ticks spent with both floors at the vendor's limits
	thrifty  bool
	gpuSum   int  // GPU floor summed over ticks, MHz
	steady   bool // stream() delivers every frame on time: a light game, not a stream
}

func newRig() *rig {
	return &rig{c: newController(cpuTable, gpuTable), now: time.Unix(1000, 0), limits: [2]int{len(cpuTable) - 1, len(gpuTable) - 1},
		rnd: rand.New(rand.NewSource(1))}
}

// tick advances half a second and returns the rate the game delivered.
func (r *rig) tick(g game) float64 {
	r.now = r.now.Add(500 * time.Millisecond)
	fps, hot, gpu := g.run(cpuTable[r.c.cpu], gpuTable[r.c.gpu])
	if r.c.cpu == r.limits[0] && r.c.gpu == r.limits[1] {
		r.ampleFor++
	}
	// The game's own late frames arrive at random, like the real ones. Each shows
	// in the newest frames for one tick and in the one-second rate for two.
	fpsNow := fps
	if g.hitchEvery > 0 {
		if r.hitchAt.IsZero() {
			r.hitchAt = r.now.Add(time.Duration(r.rnd.ExpFloat64() * float64(g.hitchEvery)))
		}
		if !r.now.Before(r.hitchAt) {
			r.hitchAt = r.now.Add(time.Duration(r.rnd.ExpFloat64() * float64(g.hitchEvery)))
			r.echo, fpsNow = 2, fps-5
			r.hitches++
		}
	}
	// Bursts come on the game's own schedule, and one that finds the floor too low
	// is late. Between them the stock governor keeps climbing above such a floor
	// for smaller ones, which shows once the floor has been in place a few seconds.
	demand := 0
	if r.c.cpu != r.cpuWas {
		r.cpuWas, r.cpuSince = r.c.cpu, r.now
	}
	if g.burstMHz > cpuTable[r.c.cpu] {
		if g.burstEvery > 0 && r.now.Sub(time.Unix(1000, 0))%g.burstEvery == 0 {
			r.echo, fpsNow = 2, fps-5
			r.hitches++
		} else if r.now.Sub(r.cpuSince) >= 3*time.Second {
			demand = g.burstMHz
		}
	}
	if r.echo > 0 {
		r.echo--
		fps -= 2
	}
	r.hist = append(r.hist, fps)
	if len(r.hist) > 12 {
		r.hist = r.hist[1:]
	}
	sum := 0.0
	for _, f := range r.hist {
		sum += f
	}
	// Content running on its own clock drifts against the panel; only a starved or
	// panel-synced game holds every frame for the same number of refreshes.
	k := int(math.Round(panel / fps))
	if k >= 2 && math.Abs(fps-panel/float64(k)) < 1e-6 {
		r.locked += 500 * time.Millisecond
	} else {
		k, r.locked = 0, 0
	}
	if r.c.gpu > r.gpuWas {
		r.gpuSeen = nil
	}
	r.gpuWas = r.c.gpu
	r.gpuSeen = append(r.gpuSeen, gpu)
	if len(r.gpuSeen) > 7 {
		r.gpuSeen = r.gpuSeen[1:]
	}
	a := r.c.step(sample{now: r.now, flowing: true, fps: fps, fps6: sum / float64(len(r.hist)), fpsNow: fpsNow, panelHz: panel, lockK: k, lockedFor: r.locked,
		gpuBusy: r.gpuSeen[0], hotBusy: hot, cpuNow: cpuTable[r.c.cpu], gpuNow: gpuTable[r.c.gpu], demand: demand, cpuLimit: r.limits[0], gpuLimit: r.limits[1], thrifty: r.thrifty})
	r.gpuSum += gpuTable[r.c.gpu]
	r.actions = append(r.actions, a)
	if fps < g.fps*0.975 {
		r.lateTick++
	}
	return fps
}

func (r *rig) run(g game, seconds int) (last float64) {
	for i := 0; i < seconds*2; i++ {
		last = r.tick(g)
	}
	return last
}

func (r *rig) count(prefix string) (n int) {
	for _, a := range r.actions {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

func TestStarvedSixtyIsFoundAndThenEconomised(t *testing.T) {
	// At the lowest clocks this game takes 76 ms a frame; left alone it would sit on a clean fraction of the panel rate.
	g := game{cpuMs: 6, gpuMs: 4, fps: 59.94}
	r := newRig()
	if fps := r.run(g, 3); fps != 59.94 {
		t.Fatalf("after 3 s the game runs at %.2f, want 59.94", fps)
	}
	r.lateTick = 0
	if fps := r.run(g, 120); fps != 59.94 {
		t.Fatalf("after settling the game runs at %.2f, want 59.94", fps)
	}
	if r.c.cpu == r.limits[0] && r.c.gpu == r.limits[1] {
		t.Errorf("still at ample clocks after two minutes; nothing was economised")
	}
	if r.lateTick > 2 {
		t.Errorf("%d late half-seconds while economising, want at most the one undone step", r.lateTick)
	}
}

func TestTrueThirtyIsLearnedAndNotHeldAtAmpleClocks(t *testing.T) {
	g := game{cpuMs: 3, gpuMs: 2, fps: 30}
	r := newRig()
	r.run(g, 600)
	if math.Abs(r.c.target-30) > 0.01 {
		t.Fatalf("target %.2f, want 30", r.c.target)
	}
	if r.lateTick != 0 {
		t.Errorf("%d late half-seconds on content that never needed more", r.lateTick)
	}
	// Probes for a higher rate must thin out once they keep finding nothing.
	if n := r.count("probe-same"); n < 2 || n > 8 {
		t.Errorf("%d fruitless probes in ten minutes, want a handful", n)
	}
	ample := 0
	for i := 0; i < 240; i++ {
		r.tick(g)
		if r.c.cpu == r.limits[0] && r.c.gpu == r.limits[1] {
			ample++
		}
	}
	if ample > 24 {
		t.Errorf("at ample clocks for %d of 240 half-seconds on light 30 fps content", ample)
	}
}

func TestContentThatSpeedsUpWhileHeldAtHalfRateIsNoticed(t *testing.T) {
	r := newRig()
	r.run(game{cpuMs: 3, gpuMs: 2, fps: 30}, 400) // settled and economised; probes have backed off
	// The cutscene ends. Find a new scene that wants 59.94 but, at the economised
	// clocks, lands on exactly half the panel rate: no lateness against a 30 fps target.
	var heavy game
	for k := 0.2; k < 4 && heavy.fps == 0; k += 0.05 {
		g := game{cpuMs: 6 * k, gpuMs: 4 * k, fps: 59.94}
		held, _, _ := g.run(cpuTable[r.c.cpu], gpuTable[r.c.gpu])
		ample, _, _ := g.run(cpuTable[r.limits[0]], gpuTable[r.limits[1]])
		if math.Abs(held-panel/2) < 0.01 && ample == 59.94 {
			heavy = g
		}
	}
	if heavy.fps == 0 {
		t.Fatalf("no scene lands on half rate at floors %d/%d; the scenario cannot be built", r.c.cpu, r.c.gpu)
	}
	found := -1
	for i := 0; i < 60; i++ {
		if r.tick(heavy) == 59.94 {
			found = i
			break
		}
	}
	if found < 0 || found > 30 {
		t.Fatalf("new scene reached full rate after %d half-seconds, want within 15 s", found)
	}
}

func TestGameThatCapsItselfAtHalfThePanelRateIsProbedOnceNotForever(t *testing.T) {
	g := game{cpuMs: 3, gpuMs: 2, fps: panel / 2} // synced to the panel by design
	r := newRig()
	r.run(g, 600)
	if n := r.count("probe-same"); n < 1 || n > 9 {
		t.Errorf("%d fruitless probes in ten minutes for content that is simply capped, want a handful", n)
	}
	if r.lateTick != 0 {
		t.Errorf("%d late half-seconds", r.lateTick)
	}
}

func TestHeavierSceneIsRescuedOnTheNextTick(t *testing.T) {
	r := newRig()
	r.run(game{cpuMs: 4, gpuMs: 3, fps: 59.94}, 120)
	heavy := game{cpuMs: 8, gpuMs: 5, fps: 59.94}
	if fps := r.tick(heavy); fps == 59.94 {
		t.Skip("economised clocks happen to carry the heavier scene")
	}
	if fps := r.tick(heavy); fps != 59.94 {
		t.Fatalf("one tick after going late the game runs at %.2f, want 59.94", fps)
	}
	if got := r.actions[len(r.actions)-2]; got != "rescue-late" {
		t.Errorf("action %q, want rescue-late", got)
	}
}

func TestStepDownThatCausesLatenessIsUndoneAndLeftAlone(t *testing.T) {
	c := newController(cpuTable, gpuTable)
	now := time.Unix(1000, 0)
	limits := [2]int{len(cpuTable) - 1, len(gpuTable) - 1}
	feed := func(fps float64) string {
		now = now.Add(500 * time.Millisecond)
		// Busy shares low enough that every step down looks safe to the predictor.
		return c.step(sample{now: now, flowing: true, fps: fps, fps6: fps, fpsNow: fps, panelHz: panel, gpuBusy: 0.2, hotBusy: 0.2,
			cpuNow: cpuTable[c.cpu], gpuNow: gpuTable[c.gpu], cpuLimit: limits[0], gpuLimit: limits[1]})
	}
	var a string
	for i := 0; i < 20 && !strings.HasPrefix(a, "down-"); i++ {
		a = feed(59.94)
	}
	if !strings.HasPrefix(a, "down-") {
		t.Fatalf("never stepped down (last action %q)", a)
	}
	failed := [2]int{c.cpu, c.gpu}
	if a = feed(50); a != "undo-late" {
		t.Fatalf("action %q after lateness right behind a step down, want undo-late", a)
	}
	if got := [2]int{c.cpu, c.gpu}; got == failed {
		t.Fatalf("floors still at the failed setting")
	}
	for i := 0; i < 50; i++ { // 25 s: inside the first hold
		feed(59.94)
		if [2]int{c.cpu, c.gpu} == failed {
			t.Fatalf("returned to the failed setting %d half-seconds after undoing it", i)
		}
	}
}

func TestNothingPresentingDropsTheFloorsAndTheNextSceneStartsFresh(t *testing.T) {
	r := newRig()
	g := game{cpuMs: 6, gpuMs: 4, fps: 59.94}
	r.run(g, 20)
	for i := 0; i < idleTicks; i++ {
		r.now = r.now.Add(500 * time.Millisecond)
		r.c.step(sample{now: r.now, cpuLimit: r.limits[0], gpuLimit: r.limits[1]})
	}
	if r.c.cpu != 0 || r.c.gpu != 0 {
		t.Fatalf("floors %d/%d after five idle seconds, want 0/0", r.c.cpu, r.c.gpu)
	}
	r.tick(g)
	if r.c.cpu != r.limits[0] || r.c.gpu != r.limits[1] {
		t.Errorf("resumed at floors %d/%d, want ample clocks first", r.c.cpu, r.c.gpu)
	}
}

func TestFloorsNeverExceedWhatTheVendorAllows(t *testing.T) {
	r := newRig()
	g := game{cpuMs: 6, gpuMs: 4, fps: 59.94}
	r.run(g, 5)
	r.limits = [2]int{3, 4} // thermal limits bite
	for i := 0; i < 60; i++ {
		r.tick(g)
		if r.c.cpu > 3 || r.c.gpu > 4 {
			t.Fatalf("floors %d/%d above the vendor's limits 3/4", r.c.cpu, r.c.gpu)
		}
	}
}

func TestSideBySideLoadIsEconomisedPastTheTakeTurnsLimit(t *testing.T) {
	// Busy shares of 0.42 and 0.48 at ample clocks: taking turns that would leave
	// no room, but these overlap, so each can grow towards a whole frame.
	g := game{cpuMs: 7, gpuMs: 8, fps: 59.94, sideBySide: true}
	r := newRig()
	r.run(g, 3)
	r.lateTick = 0
	r.run(g, 300)
	_, hot, gpu := g.run(cpuTable[r.c.cpu], gpuTable[r.c.gpu])
	if hot+gpu < 1.2 {
		t.Errorf("settled at busy shares %.2f+%.2f; clocks were not lowered past the take-turns limit", hot, gpu)
	}
	if hot >= 1 || gpu >= 1 {
		t.Errorf("settled at busy shares %.2f+%.2f, which cannot hold the rate", hot, gpu)
	}
	if r.lateTick != 0 {
		t.Errorf("%d late half-seconds on the way down, want none", r.lateTick)
	}
}

func TestTakingTurnsLoadPaysForOneTrialAndKeepsItsMargin(t *testing.T) {
	g := game{cpuMs: 6, gpuMs: 4, fps: 59.94}
	r := newRig()
	r.run(g, 3)
	r.lateTick = 0
	r.run(g, 300)
	if n := r.count("undo-trial"); n != 1 {
		t.Fatalf("%d failed trials in five minutes, want exactly one", n)
	}
	if r.lateTick > 1 {
		t.Errorf("%d late half-seconds, want only the failed trial's", r.lateTick)
	}
	if _, hot, gpu := g.run(cpuTable[r.c.cpu], gpuTable[r.c.gpu]); hot+gpu >= slack {
		t.Errorf("settled at busy shares %.2f+%.2f, inside the margin a take-turns load should keep", hot, gpu)
	}
}

func TestFloorsThatFailWhenTheSceneGetsHeavyAreNotReturnedToAtOnce(t *testing.T) {
	// Every 40 s this game has a 5 s heavy stretch that the economised floors cannot carry.
	light, heavy := game{cpuMs: 4, gpuMs: 3, fps: 59.94}, game{cpuMs: 7, gpuMs: 5, fps: 59.94}
	r := newRig()
	for i := 0; i < 15*60*2; i++ {
		if i%80 >= 70 {
			r.tick(heavy)
		} else {
			r.tick(light)
		}
	}
	// 22 heavy stretches. Returning to the same floors each time would mean a rescue for every one.
	if n := r.count("rescue-late"); n > 2 {
		t.Errorf("%d rescues in 15 minutes of a repeating heavy stretch, want the floors to learn from them", n)
	}
}

func TestLateFrameAtAmpleClocksBlamesNothing(t *testing.T) {
	c := newController(cpuTable, gpuTable)
	now := time.Unix(1000, 0)
	limits := [2]int{len(cpuTable) - 1, len(gpuTable) - 1}
	feed := func(fps float64) string {
		now = now.Add(500 * time.Millisecond)
		// Busy shares too high for any step down, so the floors stay at the limits.
		return c.step(sample{now: now, flowing: true, fps: fps, fps6: 59.94, fpsNow: fps, panelHz: panel, gpuBusy: 0.79, hotBusy: 0.79,
			cpuNow: cpuTable[c.cpu], gpuNow: gpuTable[c.gpu], cpuLimit: limits[0], gpuLimit: limits[1]})
	}
	for i := 0; i < 30; i++ {
		feed(59.94)
	}
	if got := feed(55); got != "uneven" {
		t.Fatalf("action %q for a late frame with nothing more to give, want uneven", got)
	}
	if len(c.holdUntil) != 0 {
		t.Errorf("settings held back after a late frame at ample clocks: %v", c.holdUntil)
	}
	if c.state != track || c.target < 59 {
		t.Errorf("state %v target %.1f, want the target kept and tracking to go on", c.state, c.target)
	}
}

// An hour of a game that drops two frames about once a minute whatever the
// clocks, as God of War II does. Holding those against every setting at once,
// as the first version of this law did when one arrived at ample clocks, pins
// the floors to the ceiling for the rest of the session.
func TestGameThatDropsFramesByItselfIsNotPinnedToAmpleClocks(t *testing.T) {
	for seed := int64(1); seed <= 3; seed++ {
		g := game{cpuMs: 4, gpuMs: 3, fps: 59.94, hitchEvery: time.Minute}
		r := newRig()
		r.rnd.Seed(seed)
		starved := 0
		for i := 0; i < 7200; i++ {
			if r.tick(g) < 50 {
				starved++
			}
		}
		if r.ampleFor > 7200/4 {
			t.Errorf("seed %d: at ample clocks for %d of 7200 half-seconds", seed, r.ampleFor)
		}
		if starved > 10 {
			t.Errorf("seed %d: %d half-seconds starved by the floors", seed, starved)
		}
	}
}

func TestHeavyStretchesAreStillCarriedInAGameThatDropsFramesByItself(t *testing.T) {
	// Ninety heavy stretches in the hour, each beyond what the floors for the rest can carry.
	for seed := int64(1); seed <= 3; seed++ {
		light := game{cpuMs: 4, gpuMs: 3, fps: 59.94, hitchEvery: time.Minute}
		heavy := game{cpuMs: 7, gpuMs: 5, fps: 59.94, hitchEvery: time.Minute}
		r := newRig()
		r.rnd.Seed(seed)
		starved := 0
		for i := 0; i < 7200; i++ {
			g := light
			if i%80 >= 70 {
				g = heavy
			}
			if r.tick(g) < 50 {
				starved++
			}
		}
		if starved > 12 {
			t.Errorf("seed %d: %d half-seconds starved in an hour, want the floors to learn the heavy stretches", seed, starved)
		}
	}
}

// A game whose average load is light but which has a short burst every fifteen
// seconds that only 2745 MHz absorbs. Busy shares do not show the bursts; the
// stock governor climbing for them does.
func TestBurstsTheStockGovernorClimbsForAreMetByTheFloor(t *testing.T) {
	// Looks below 2745 MHz come at most every 1, 2, 4, 8 and then 16 minutes, and
	// only a burst in the first seconds of one arrives before the floor is back.
	// Whether one does is a matter of how the bursts fall against the looks (0 to
	// 5 in a half-hour as the interval goes from 11 to 23 s), so it is counted
	// over several intervals and starts.
	late, runs := 0, 0
	for _, every := range []time.Duration{13, 15, 17, 19} {
		for shift := 0; shift < 3; shift++ {
			g := game{cpuMs: 4, gpuMs: 3, fps: 59.94, burstMHz: 2745, burstEvery: every * time.Second}
			r := newRig()
			r.run(game{cpuMs: 4, gpuMs: 3, fps: 59.94}, 2*shift) // the same game before its bursts begin
			r.hitches = 0
			r.run(g, 30*60)
			late, runs = late+r.hitches, runs+1
			if got := cpuTable[r.c.cpu]; got != 2745 {
				t.Errorf("a burst every %d s: CPU floor %d MHz after half an hour, want the 2745 the bursts need and no more", every, got)
			}
			if got := gpuTable[r.c.gpu]; got > 443 {
				t.Errorf("a burst every %d s: GPU floor %d MHz: bursts on the CPU side kept the GPU clock up too", every, got)
			}
		}
	}
	if late > 2*runs {
		t.Errorf("%d bursts arrived late in %d half-hours, want two a half-hour at most: the floor is to stay where the stock governor reached", late, runs)
	}
}

func TestHotBoardGivesUpGPUFloorHeldOnSuspicion(t *testing.T) {
	// The game's own late frames are held against whatever GPU floor is in place
	// at the time, which walks it up to the top clock although the GPU is busy an
	// eighth of the time there.
	g := game{cpuMs: 9, gpuMs: 2, fps: 59.94, sideBySide: true, hitchEvery: 40 * time.Second, burstMHz: 2918, burstEvery: 30 * time.Second}
	mean := map[bool]int{}
	for _, thrifty := range []bool{false, true} {
		r := newRig()
		r.run(g, 120)
		r.thrifty = thrifty
		r.gpuSum, r.lateTick, r.hitches = 0, 0, 0
		r.run(g, 1200)
		mean[thrifty] = r.gpuSum / 2400
		if r.lateTick > 2*r.hitches {
			t.Errorf("thrifty=%v: %d late half-seconds for %d late frames of the game's own", thrifty, r.lateTick, r.hitches)
		}
	}
	if mean[true] > mean[false]*2/3 {
		t.Errorf("GPU floor averaged %d MHz on a hot board against %d MHz on a cool one, want well under", mean[true], mean[false])
	}
}

func TestHotBoardKeepsTheGPUFloorABusyGPUNeeds(t *testing.T) {
	g := game{cpuMs: 5, gpuMs: 7, fps: 59.94, hitchEvery: 40 * time.Second, burstMHz: 2918, burstEvery: 30 * time.Second}
	r := newRig()
	r.thrifty = true
	r.run(g, 120)
	r.lateTick, r.hitches = 0, 0
	if fps := r.run(g, 1200); fps < 59 {
		t.Errorf("the game ends at %.1f fps", fps)
	}
	// One more late frame is allowed for: a floor a step too low is tried once
	// when the heaviest moment passes out of memory, and remembered from then on.
	if r.lateTick > 2*r.hitches+2 {
		t.Errorf("%d late half-seconds for %d late frames of the game's own", r.lateTick, r.hitches)
	}
}

func TestThriftyFloorThatStarvesTheGameIsGivenBackNotLearnedAsItsRate(t *testing.T) {
	// Half of this game's frame is spent in threads the busy shares do not show,
	// and its CPU and GPU work take turns, so the floor the GPU's busy share
	// calls for is too low, and stays too low however long the game is starved
	// on it: it needs the GPU within two steps of its top clock.
	g := game{cpuMs: 5, gpuMs: 3, hiddenMs: 8, fps: 59.94, burstMHz: 2918, burstEvery: 30 * time.Second}
	late := map[bool]int{}
	for _, thrifty := range []bool{false, true} {
		r := newRig()
		r.run(g, 120)
		r.thrifty = thrifty
		r.lateTick = 0
		r.run(g, 1200)
		late[thrifty] = r.lateTick
		if r.c.target < 59 {
			t.Errorf("thrifty=%v: the target became %.1f fps: the starved rate was learned as the game's own", thrifty, r.c.target)
		}
		// The thrifty floor is tried at once and then after 5 and 15 minutes.
		if tries := r.count("rescue-gpu"); tries > 3 {
			t.Errorf("thrifty=%v: the thrifty floor was tried %d times in twenty minutes, want 3 at most", thrifty, tries)
		}
	}
	if late[true] > late[false]+6 {
		t.Errorf("%d late half-seconds on a hot board against %d on a cool one, want a second more for each of at most three tries", late[true], late[false])
	}
}

// Measured on the tablet: with the thrifty floor applied to it, Harry Potter
// (25 fps) lost seven seconds in ten to uneven frames.
func TestHotBoardLeavesContentBelowThePanelRateAlone(t *testing.T) {
	g := game{cpuMs: 5, gpuMs: 2, fps: 30, hitchEvery: 40 * time.Second, burstMHz: 2918, burstEvery: 30 * time.Second}
	var floors [2][2]int
	for i, thrifty := range []bool{false, true} {
		r := newRig()
		r.thrifty = thrifty
		r.run(g, 600)
		floors[i] = [2]int{r.c.cpu, r.c.gpu}
	}
	if floors[0] != floors[1] {
		t.Errorf("ends at floors %v on a hot board and %v on a cool one, want the same", floors[1], floors[0])
	}
}

// stream is a stand-in for a game stream: next to no work on the tablet, and a
// frame rate that wanders with the machine sending it.
func (r *rig) stream(seconds int, hot, gpu float64) {
	for i := 0; i < seconds*2; i++ {
		r.now = r.now.Add(500 * time.Millisecond)
		fps := 50 + 4*math.Sin(float64(i)/3) - 6*float64(r.rnd.Intn(5)/4)
		if r.steady {
			fps = panel
		}
		r.hist = append(r.hist, fps)
		if len(r.hist) > 12 {
			r.hist = r.hist[1:]
		}
		sum := 0.0
		for _, f := range r.hist {
			sum += f
		}
		// The busy shares are what the work comes to at the clocks the floors give.
		top := float64(cpuTable[len(cpuTable)-1])
		a := r.c.step(sample{now: r.now, flowing: true, fps: fps, fps6: sum / float64(len(r.hist)), fpsNow: fps, panelHz: panel,
			hotBusy: hot * top / float64(cpuTable[r.c.cpu]), gpuBusy: gpu * float64(gpuTable[len(gpuTable)-1]) / float64(gpuTable[r.c.gpu]),
			cpuNow: cpuTable[r.c.cpu], gpuNow: gpuTable[r.c.gpu], cpuLimit: r.limits[0], gpuLimit: r.limits[1]})
		r.actions = append(r.actions, a)
		if r.c.cpu == r.limits[0] && r.c.gpu == r.limits[1] {
			r.ampleFor++
		}
	}
}

func TestStreamWhoseRateWandersIsNotHeldAtAmpleClocks(t *testing.T) {
	r := newRig()
	r.stream(10, 0.02, 0.002) // the decoder's thread 2% busy at the top clock, the GPU all but idle
	r.ampleFor, r.actions = 0, nil
	r.stream(300, 0.02, 0.002)
	if r.ampleFor != 0 {
		t.Errorf("at ample clocks for %d of 600 half-seconds on a stream", r.ampleFor)
	}
	if got := [2]int{cpuTable[r.c.cpu], gpuTable[r.c.gpu]}; got != [2]int{1363, 222} {
		t.Errorf("floors %v MHz, want the resting clock and the lowest GPU floor", got)
	}
	if n := r.count("rescue") + r.count("relearn") + r.count("probe") + r.count("undo"); n != 0 {
		t.Errorf("%d rescues, probes and undone steps on content whose lateness is not the clocks' doing: %v", n, r.actions)
	}
}

func TestGameThatStartsBehindALightMenuGetsAmpleClocksAtOnce(t *testing.T) {
	r := newRig()
	r.stream(20, 0.02, 0.002)
	if !r.c.light {
		t.Fatal("a menu this light was not recognised")
	}
	g := game{cpuMs: 6, gpuMs: 4, fps: 59.94}
	r.tick(g)
	if r.c.cpu != r.limits[0] || r.c.gpu != r.limits[1] {
		t.Fatalf("floors %d/%d MHz one tick after the game began, want the limits", cpuTable[r.c.cpu], gpuTable[r.c.gpu])
	}
	r.lateTick = 0
	if fps := r.run(g, 120); fps != 59.94 || r.lateTick > 2 {
		t.Errorf("%.2f fps with %d late half-seconds once the game runs, want 59.94 and at most the one undone step", fps, r.lateTick)
	}
	if r.c.light {
		t.Error("a game with real work is still being treated as light")
	}
}

func TestContentJustUnderTheLineDoesNotGoInAndOutOfLight(t *testing.T) {
	// 5% of the top clock is 11% at the resting one; every tenth second it is three times that.
	r := newRig()
	r.steady = true
	for i := 0; i < 60; i++ {
		r.stream(9, 0.05, 0.002)
		r.stream(1, 0.15, 0.002)
	}
	if n := r.count("light"); n > 5 {
		t.Errorf("called light %d times in ten minutes, each followed by a look at ample clocks", n)
	}
}

func TestHeavierStreamIsRecognisedByLatenessTheClocksDoNotCure(t *testing.T) {
	// 8% of the top clock is 17% at the resting one: over the line for content that is merely light.
	r := newRig()
	r.stream(60, 0.08, 0.002)
	if !r.c.light || !r.c.adrift {
		t.Fatalf("after a minute of a wandering rate that ample clocks did not steady: light=%v adrift=%v, floors %d/%d MHz",
			r.c.light, r.c.adrift, cpuTable[r.c.cpu], gpuTable[r.c.gpu])
	}
	r.ampleFor = 0
	r.stream(300, 0.08, 0.002)
	if r.ampleFor != 0 || !r.c.light {
		t.Errorf("left its resting clocks for %d of 600 half-seconds (light=%v)", r.ampleFor, r.c.light)
	}
}

func TestSteadyGameWithThatMuchWorkIsNotCalledLight(t *testing.T) {
	// The same 8% from a game that is on time: nothing says its frames are made elsewhere.
	g := game{cpuMs: 1.33, gpuMs: 0.2, fps: 59.94}
	r := newRig()
	r.run(g, 180)
	if r.c.light || r.count("light") != 0 {
		t.Errorf("a game that holds its rate was called light (%d times)", r.count("light"))
	}
}

func TestTargetCountedHighIsCorrectedNotHeldAtAmpleClocks(t *testing.T) {
	g := game{cpuMs: 6, gpuMs: 4, fps: 59.94}
	r := newRig()
	r.run(g, 3) // past the two seconds of learning, so the target below is not replaced at once
	// The one-second count the target was set from had a frame too many: under 2.5% high, so no
	// single second looks late, and over 1.1%, so every six seconds do.
	r.c.target, r.c.rough, r.c.fast = 61.0, true, false
	r.c.cpu, r.c.gpu = r.limits[0], r.limits[1]
	r.run(g, 90)
	if math.Abs(r.c.target-59.94) > 0.3 {
		t.Fatalf("target %.2f after ninety seconds of a steady 59.94, want that", r.c.target)
	}
	if r.c.cpu == r.limits[0] && r.c.gpu == r.limits[1] {
		t.Errorf("still at ample clocks: nothing was economised")
	}
}

// uncapped is a stand-in for a game that draws as fast as the GPU lets it,
// below the panel's rate: the rate wanders with the scene, and the GPU is full
// at its top clock unless the CPU floor is under needMHz, when it waits for work.
func (r *rig) uncapped(seconds, needMHz int) {
	for i := 0; i < seconds*2; i++ {
		r.now = r.now.Add(500 * time.Millisecond)
		fps := 44 + 5*math.Sin(float64(i)/5) - 4*float64(r.rnd.Intn(5)/4)
		busy := 0.99
		if cpuTable[r.c.cpu] < needMHz {
			fps, busy = fps*0.85, 0.84
		}
		r.hist = append(r.hist, fps)
		if len(r.hist) > 12 {
			r.hist = r.hist[1:]
		}
		sum := 0.0
		for _, f := range r.hist {
			sum += f
		}
		a := r.c.step(sample{now: r.now, flowing: true, fps: fps, fps6: sum / float64(len(r.hist)), fpsNow: fps, panelHz: panel,
			hotBusy: 0.5, gpuBusy: busy, cpuNow: cpuTable[r.c.cpu], gpuNow: gpuTable[r.limits[1]], cpuLimit: r.limits[0], gpuLimit: r.limits[1]})
		r.actions = append(r.actions, a)
		if r.c.cpu == r.limits[0] && r.c.gpu == r.limits[1] {
			r.ampleFor++
		}
	}
}

func TestGameTheGPULimitsDoesNotKeepTheCPUAtAmpleClocks(t *testing.T) {
	r := newRig()
	r.uncapped(60, 0)
	r.ampleFor, r.actions = 0, nil
	r.uncapped(300, 0)
	if r.ampleFor != 0 {
		t.Errorf("at ample clocks for %d of 600 half-seconds in a game the GPU limits", r.ampleFor)
	}
	if got := [2]int{cpuTable[r.c.cpu], gpuTable[r.c.gpu]}; got != [2]int{1363, 832} {
		t.Errorf("floors %v MHz, want the resting CPU clock and the GPU's top floor", got)
	}
}

func TestCPUFloorThatLeavesTheGPUShortOfWorkIsNotGoneBackTo(t *testing.T) {
	r := newRig()
	r.uncapped(120, 2227) // under 2227 MHz the CPU cannot keep the GPU fed
	starved := 0
	for i := 0; i < 600; i++ {
		r.uncapped(1, 2227)
		if cpuTable[r.c.cpu] < 2227 {
			starved++
		}
	}
	// Each look under the floor it needs costs a second short of work and some
	// ten at ample clocks; they are spaced twice as far apart each time.
	if starved > 5 {
		t.Errorf("CPU floor under what the GPU needs for %d of 600 seconds", starved)
	}
	if r.ampleFor > 160 {
		t.Errorf("at ample clocks for %d of 1440 half-seconds: the floor it can have is 2227 MHz", r.ampleFor)
	}
}

func TestGameThatHoldsItsRateWithAFullGPUIsStillJudgedByItsFrames(t *testing.T) {
	r := newRig()
	for i := 0; i < 240; i++ { // 30 fps on time, the GPU full at its top clock
		r.now = r.now.Add(500 * time.Millisecond)
		a := r.c.step(sample{now: r.now, flowing: true, fps: 30, fps6: 30, fpsNow: 30, panelHz: panel,
			hotBusy: 0.3, gpuBusy: 0.99, cpuNow: cpuTable[r.c.cpu], gpuNow: gpuTable[r.limits[1]], cpuLimit: r.limits[0], gpuLimit: r.limits[1]})
		r.actions = append(r.actions, a)
	}
	if r.c.bound || r.count("bound") != 0 {
		t.Errorf("a game on time at its own rate was called GPU-bound: %v", r.actions)
	}
	if r.c.target != 30 {
		t.Errorf("target %.1f, want the 30 fps it holds", r.c.target)
	}
}

// A game whose busy shares add up past the take-turns limit already at ample
// clocks, so every step down is a trial, and which in fact holds its rate all
// the way down (its CPU and GPU work side by side).
func TestCleanDescentTakesItsCPUAndGPUStepsInTurn(t *testing.T) {
	g := game{cpuMs: 7, gpuMs: 7.5, fps: panel, sideBySide: true}
	long := newRig()
	long.run(g, 300)
	end := [2]int{cpuTable[long.c.cpu], gpuTable[long.c.gpu]}
	if end[0] >= cpuTable[len(cpuTable)-1] || end[1] >= gpuTable[len(gpuTable)-1] {
		t.Fatalf("floors %v MHz after five minutes: the game was to be one that comes down", end)
	}
	r := newRig()
	r.run(g, 2)
	r.lateTick = 0        // the first half-second of any game is counted late
	var last [2]time.Time // when each floor was last lowered
	for i := 0; i < 96; i++ {
		was := [2]int{r.c.cpu, r.c.gpu}
		r.tick(g)
		for k, now := range [2]int{r.c.cpu, r.c.gpu} {
			if now < was[k] {
				if gap := r.now.Sub(last[k]); !last[k].IsZero() && gap < 10*time.Second {
					t.Errorf("%s floor lowered twice within %v: each floor's own signal needs its ten seconds", [2]string{"CPU", "GPU"}[k], gap)
				}
				last[k] = r.now
			}
		}
	}
	if got := [2]int{cpuTable[r.c.cpu], gpuTable[r.c.gpu]}; got != end {
		t.Errorf("floors %v MHz after 50 s, want the %v it ends on: seven steps should not take over a minute", got, end)
	}
	if r.lateTick != 0 {
		t.Errorf("%d late half-seconds on the way down", r.lateTick)
	}
}
