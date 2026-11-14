package main

import (
	"math"
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
}

func (g game) run(cpuMHz, gpuMHz int) (fps, hot, gpu float64) {
	ct := g.cpuMs * float64(cpuTable[len(cpuTable)-1]) / float64(cpuMHz)
	gt := g.gpuMs * float64(gpuTable[len(gpuTable)-1]) / float64(gpuMHz)
	fps = g.fps
	frame := ct + gt
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
	gpuSeen  []float64     // GPU busy shares not yet reported: the kernel's figure trails by about a second
}

func newRig() *rig {
	return &rig{c: newController(cpuTable, gpuTable), now: time.Unix(1000, 0), limits: [2]int{len(cpuTable) - 1, len(gpuTable) - 1}}
}

// tick advances half a second and returns the rate the game delivered.
func (r *rig) tick(g game) float64 {
	r.now = r.now.Add(500 * time.Millisecond)
	fps, hot, gpu := g.run(cpuTable[r.c.cpu], gpuTable[r.c.gpu])
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
	r.gpuSeen = append(r.gpuSeen, gpu)
	if len(r.gpuSeen) > 3 {
		r.gpuSeen = r.gpuSeen[1:]
	}
	a := r.c.step(sample{now: r.now, flowing: true, fps: fps, fps6: sum / float64(len(r.hist)), panelHz: panel, lockK: k, lockedFor: r.locked,
		gpuBusy: r.gpuSeen[0], hotBusy: hot, cpuNow: cpuTable[r.c.cpu], gpuNow: gpuTable[r.c.gpu], cpuLimit: r.limits[0], gpuLimit: r.limits[1]})
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
		return c.step(sample{now: now, flowing: true, fps: fps, fps6: fps, panelHz: panel, gpuBusy: 0.2, hotBusy: 0.2,
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
