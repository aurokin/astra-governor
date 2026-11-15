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
	gpuSum   int // GPU floor summed over ticks, MHz
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
	g := game{cpuMs: 4, gpuMs: 3, fps: 59.94, burstMHz: 2745, burstEvery: 15 * time.Second}
	r := newRig()
	r.run(g, 30*60)
	// Looks below 2745 MHz come at most every 1, 2, 4, 8 and then 16 minutes, and
	// only a burst in the first seconds of one arrives before the floor is back.
	if r.hitches > 2 {
		t.Errorf("%d of 120 bursts arrived late, want the floor to stay where the stock governor reached", r.hitches)
	}
	if got := cpuTable[r.c.cpu]; got != 2745 {
		t.Errorf("CPU floor %d MHz after half an hour, want the 2745 the bursts need and no more", got)
	}
	if got := gpuTable[r.c.gpu]; got > 443 {
		t.Errorf("GPU floor %d MHz: bursts on the CPU side kept the GPU clock up too", got)
	}
}

func TestHotBoardGivesUpGPUFloorHeldOnSuspicion(t *testing.T) {
	// The game's own late frames are held against whatever GPU floor is in place
	// at the time, which walks it up to the top clock although the GPU is busy an
	// eighth of the time there.
	g := game{cpuMs: 9, gpuMs: 2, fps: 59.94, sideBySide: true, hitchEvery: 40 * time.Second}
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
	g := game{cpuMs: 5, gpuMs: 7, fps: 59.94, hitchEvery: 40 * time.Second}
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

func TestHotBoardLeavesRoomForWorkThatTakesTurnsWithTheGPU(t *testing.T) {
	// At the GPU floor its own busy share allows (222 MHz), this game's CPU and
	// GPU work no longer fit into one frame together.
	g := game{cpuMs: 6, gpuMs: 3, fps: 59.94, hitchEvery: 40 * time.Second}
	r := newRig()
	r.thrifty = true
	r.run(g, 120)
	r.lateTick, r.hitches = 0, 0
	r.run(g, 1200)
	// One more late frame is allowed for: the floors in place when the board got
	// hot traded GPU clock for CPU clock, and that trade ends with a late frame.
	if r.lateTick > 2*r.hitches+2 || r.c.target < 59 {
		t.Errorf("%d late half-seconds for %d late frames of the game's own, target %.1f fps", r.lateTick, r.hitches, r.c.target)
	}
}

func TestThriftyFloorThatStarvesTheGameIsGivenBackNotLearnedAsItsRate(t *testing.T) {
	// Half of this game's frame is spent in threads the busy shares do not show,
	// so the floor they call for is too low, and stays too low however long the
	// game is starved on it: it needs the GPU within two steps of its top clock.
	g := game{cpuMs: 5, gpuMs: 3, hiddenMs: 8, fps: 59.94}
	r := newRig()
	r.run(g, 120)
	r.thrifty = true
	r.lateTick = 0
	r.run(g, 1200)
	if r.c.target < 59 {
		t.Errorf("the target became %.1f fps: the starved rate was learned as the game's own", r.c.target)
	}
	// A second late each time the thrifty floor is tried: at once, then after 5
	// and 15 minutes. The law's own steps down fail another six times in a game
	// this tight, a half-second each.
	if tries := r.count("rescue-gpu"); tries > 3 || r.lateTick > 2*tries+6 {
		t.Errorf("%d tries of the thrifty floor and %d late half-seconds in twenty minutes, want 3 and 12", tries, r.lateTick)
	}
}
