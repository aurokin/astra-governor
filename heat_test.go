package main

import (
	"testing"
	"time"
)

// board is a model of the sensor the vendor throttles on, in two parts as the
// tablet showed them (../README.md, "Heat"): the case, which takes
// many minutes to follow the power drawn, and on top of it the board's own rise
// over the case, which follows the CPU's clocks within a minute or so.
type board struct {
	ambient, cPerWatt float64
	watts             float64 // drawn at full ceilings
	perStep           float64 // watts saved by each step of CPU ceiling
	gpuIdle           bool    // the GPU never reaches its top level, so its step saves nothing
	gpuStep, gpuRise  float64 // watts, and degrees of the sensor's rise, saved by the GPU's step
	spareGPU          float64 // watts saved by giving up spare GPU floor
	rise, risePerStep float64 // the sensor's rise over the case at full ceilings, and what each step takes off it
	caseTau, riseTau  time.Duration

	caseC, riseC float64
	hottest      float64
	level        int
	deepest      int
	levelsMoved  int
}

// God of War II in Rise with the fan as the vendor runs it: 10 W, the case
// heading for 44.5 C and the sensor for 8.3 C more, well past the cut at 49.
// The vendor's own cut (seven steps) takes off 1.2 W and 1.6 C of that rise.
func heavyGame() *board {
	return &board{ambient: 23.5, cPerWatt: 2.1, watts: 10, perStep: 0.17, gpuIdle: true, spareGPU: 1, rise: 8.3, risePerStep: 0.23,
		caseTau: 12 * time.Minute, riseTau: time.Minute, caseC: 28, riseC: 2}
}

// heavyRace is a game the GPU limits: the GPU at its top level all the time,
// the CPU well under its ceilings, so that CPU steps save little. The numbers
// are a guess at the shape, not a fit to the race: settling at 51 C as it is,
// and at 46.4 C with the GPU's top level given up.
func heavyRace() *board {
	return &board{ambient: 23.5, cPerWatt: 2.1, watts: 9.4, perStep: 0.05, gpuStep: 1.5, spareGPU: 0, rise: 8.3, risePerStep: 0.07, gpuRise: 1.5,
		caseTau: 12 * time.Minute, riseTau: time.Minute, caseC: 28, riseC: 2}
}

func (b *board) c() float64 { return b.caseC + b.riseC }

// cpuSteps is how many steps of CPU ceiling the level stands for; the first step is the GPU's.
func (b *board) cpuSteps() int { return max(0, b.level-1) }

func (b *board) run(h *heat, now *time.Time, d time.Duration) {
	for end := now.Add(d); now.Before(end); *now = now.Add(tick) {
		watts, rise := b.watts-b.perStep*float64(b.cpuSteps()), b.rise*b.watts/10-b.risePerStep*float64(b.cpuSteps())
		if h.saving {
			watts -= b.spareGPU
		}
		if b.level > 0 && !b.gpuIdle {
			watts, rise = watts-b.gpuStep, rise-b.gpuRise
		}
		b.caseC += (b.ambient + b.cPerWatt*watts - b.caseC) * tick.Seconds() / b.caseTau.Seconds()
		b.riseC += (rise - b.riseC) * tick.Seconds() / b.riseTau.Seconds()
		b.hottest = max(b.hottest, b.c())
		if l := h.step(*now, b.c(), b.gpuIdle); l != b.level {
			b.level, b.levelsMoved, b.deepest = l, b.levelsMoved+1, max(b.deepest, l)
		}
	}
}

func TestCeilingsKeepAHeavyGameUnderTheVendorsCut(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 8}, time.Unix(0, 0)
	b.run(h, &now, time.Hour)
	if b.hottest >= 49 {
		t.Errorf("the sensor reached %.1f C: the vendor would have cut the ceilings at 49", b.hottest)
	}
	// With the spare GPU floor given up, four to five steps of CPU ceiling hold 48 C.
	if b.cpuSteps() < 3 || b.cpuSteps() > 6 {
		t.Errorf("settled %d CPU steps down, want four or five", b.cpuSteps())
	}
	if b.c() < 47 {
		t.Errorf("ended at %.1f C: more ceiling was given up than holding 48 C needs", b.c())
	}
}

func TestCeilingsAreNotHuntedUpAndDown(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 8}, time.Unix(0, 0)
	b.run(h, &now, 40*time.Minute)
	b.levelsMoved = 0
	b.run(h, &now, 20*time.Minute)
	if b.levelsMoved > 8 {
		t.Errorf("%d ceiling changes in twenty settled minutes, want a handful", b.levelsMoved)
	}
}

func TestSpareGPUFloorGoesBeforeAnyCeiling(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 8}, time.Unix(0, 0)
	b.watts = 8.6 // settles at 48.7 C as it is, and at 46.6 C without the spare GPU floor
	b.run(h, &now, time.Hour)
	if !h.saving {
		t.Errorf("spare GPU floor still held at %.1f C", b.c())
	}
	if b.levelsMoved != 0 {
		t.Errorf("gave up ceiling %d times in a game that giving up spare GPU floor keeps under the cut", b.levelsMoved)
	}
}

func TestALighterGameIsLeftAlone(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 8}, time.Unix(0, 0)
	b.watts = 6 // settles at 41 C
	b.run(h, &now, time.Hour)
	if h.saving || b.levelsMoved != 0 {
		t.Errorf("saving=%v, %d ceiling changes in a game that stays 7 C under the target by itself", h.saving, b.levelsMoved)
	}
}

func TestCeilingsComeBackOnceTheBoardCools(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 8}, time.Unix(0, 0)
	b.run(h, &now, 40*time.Minute)
	if b.level == 0 {
		t.Fatal("no ceiling given up in the heavy stretch; the test needs one")
	}
	b.watts = 4 // a menu, or the game left for a while
	b.run(h, &now, 6*time.Minute)
	if b.level != 0 {
		t.Errorf("still %d steps down six minutes into a light stretch at %.1f C", b.level, b.c())
	}
}

// Measured after a four-minute break late in a session: the case had hardly
// cooled, and the sensor came back up at 1.5 C a minute.
func TestAGameResumedOnAWarmCaseStaysUnderTheCut(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 8}, time.Unix(0, 0)
	b.caseC, b.riseC = 41.2, 2
	b.run(h, &now, 10*time.Minute)
	if b.hottest >= 49 {
		t.Errorf("the sensor reached %.1f C within ten minutes of resuming", b.hottest)
	}
}

func TestAGameTheGPULimitsGivesUpTheGPUsTopLevelBeforeAnyCPUCeiling(t *testing.T) {
	b, h, now := heavyRace(), &heat{target: 48, max: 8}, time.Unix(0, 0)
	b.run(h, &now, time.Hour)
	if b.hottest >= 49 {
		t.Errorf("the sensor reached %.1f C: the vendor would have cut the clocks at 49", b.hottest)
	}
	if b.deepest != 1 {
		t.Errorf("went %d steps down, want the GPU's step alone, which holds this game under 48 C", b.deepest)
	}
	if b.levelsMoved != 1 {
		t.Errorf("the GPU's top level was taken away or given back %d times in an hour, want once: one steady rate", b.levelsMoved)
	}
}

func TestAGameWhoseGPUIsBelowItsTopLosesNoTimeOnTheGPUStep(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 8}, time.Unix(0, 0)
	for end := now.Add(time.Hour); now.Before(end) && b.level == 0; {
		b.run(h, &now, tick)
	}
	if b.level != 2 {
		t.Errorf("first move went to level %d, want the GPU's step and the first CPU step together (2)", b.level)
	}
}
