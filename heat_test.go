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
	perStep           float64 // watts saved by each step of ceiling
	spareGPU          float64 // watts saved by giving up spare GPU floor
	rise, risePerStep float64 // the sensor's rise over the case at full ceilings, and what each step takes off it
	caseTau, riseTau  time.Duration

	caseC, riseC float64
	hottest      float64
	level        int
	levelsMoved  int
}

// God of War II in Rise with the fan as the vendor runs it: 10 W, the case
// heading for 44.5 C and the sensor for 8.3 C more, well past the cut at 49.
// The vendor's own cut (seven steps) takes off 1.2 W and 1.6 C of that rise.
func heavyGame() *board {
	return &board{ambient: 23.5, cPerWatt: 2.1, watts: 10, perStep: 0.17, spareGPU: 1, rise: 8.3, risePerStep: 0.23,
		caseTau: 12 * time.Minute, riseTau: time.Minute, caseC: 28, riseC: 2}
}

func (b *board) c() float64 { return b.caseC + b.riseC }

func (b *board) run(h *heat, now *time.Time, d time.Duration) {
	for end := now.Add(d); now.Before(end); *now = now.Add(tick) {
		watts := b.watts - b.perStep*float64(b.level)
		if h.saving {
			watts -= b.spareGPU
		}
		b.caseC += (b.ambient + b.cPerWatt*watts - b.caseC) * tick.Seconds() / b.caseTau.Seconds()
		b.riseC += (b.rise*b.watts/10 - b.risePerStep*float64(b.level) - b.riseC) * tick.Seconds() / b.riseTau.Seconds()
		b.hottest = max(b.hottest, b.c())
		if l := h.step(*now, b.c()); l != b.level {
			b.level, b.levelsMoved = l, b.levelsMoved+1
		}
	}
}

func TestCeilingsKeepAHeavyGameUnderTheVendorsCut(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 7}, time.Unix(0, 0)
	b.run(h, &now, time.Hour)
	if b.hottest >= 49 {
		t.Errorf("the sensor reached %.1f C: the vendor would have cut the ceilings at 49", b.hottest)
	}
	// With the spare GPU floor given up, four to five steps hold 48 C.
	if b.level < 3 || b.level > 6 {
		t.Errorf("settled %d steps down, want four or five", b.level)
	}
	if b.c() < 47 {
		t.Errorf("ended at %.1f C: more ceiling was given up than holding 48 C needs", b.c())
	}
}

func TestCeilingsAreNotHuntedUpAndDown(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 7}, time.Unix(0, 0)
	b.run(h, &now, 40*time.Minute)
	b.levelsMoved = 0
	b.run(h, &now, 20*time.Minute)
	if b.levelsMoved > 8 {
		t.Errorf("%d ceiling changes in twenty settled minutes, want a handful", b.levelsMoved)
	}
}

func TestSpareGPUFloorGoesBeforeAnyCeiling(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 7}, time.Unix(0, 0)
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
	b, h, now := heavyGame(), &heat{target: 48, max: 7}, time.Unix(0, 0)
	b.watts = 6 // settles at 41 C
	b.run(h, &now, time.Hour)
	if h.saving || b.levelsMoved != 0 {
		t.Errorf("saving=%v, %d ceiling changes in a game that stays 7 C under the target by itself", h.saving, b.levelsMoved)
	}
}

func TestCeilingsComeBackOnceTheBoardCools(t *testing.T) {
	b, h, now := heavyGame(), &heat{target: 48, max: 7}, time.Unix(0, 0)
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
	b, h, now := heavyGame(), &heat{target: 48, max: 7}, time.Unix(0, 0)
	b.caseC, b.riseC = 41.2, 2
	b.run(h, &now, 10*time.Minute)
	if b.hottest >= 49 {
		t.Errorf("the sensor reached %.1f C within ten minutes of resuming", b.hottest)
	}
}
