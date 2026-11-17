# The display

What is known about the Astra's panel timing, what it means for games and
video, and what was tried to change it. astra-governor does not touch the
panel; this is here because the panel's real rates shape every frame-rate
measurement and explain several things users notice.

## The panel's real refresh rates

The five refresh modes do not run at their nominal rates. From the present
timestamps Android records for every frame:

| Mode | Measured event rate | Above nominal |
| --- | --- | --- |
| 60 | 60.272 Hz | 0.453% |
| 90 | 90.408 Hz | 0.453% |
| 120 | 120.544 Hz | 0.453% |
| 144 | 144.653 Hz | 0.453% |
| 165 | 166.056 Hz | 0.640% |

Four of the five are exactly 0.4533% fast, which points to one clock ratio
rather than drift. The rate is the same in every vendor mode, Diablo included,
in games and on the home screen, and it does not wander (its period varies by
about 0.1 microsecond).

**What these numbers are, exactly.** They are the rate of Android's
actual-present events (SurfaceFlinger), not an optical measurement of the OLED
emitting. Later work traced the driver's separate vblank, retire and output
paths and could not tie the measured fence to physical scanout. Treat them as
the rate frames are presented at, which is what governs pacing, and not as a
proven physical scan rate.

## What that means for content

| Content | On this panel | What helps |
| --- | --- | --- |
| A native game paced by Android | runs at the panel's rate (60.27, 166.06...) | nothing needed; measured at 120, 144 and 165 Hz |
| An emulator at 59.94 fps (NTSC) | one frame repeated about every 3 s at the 60 and 120 modes | running the emulator 0.55% fast (Dolphin `EmulationSpeed = 1.00554`) was reported to remove it over five minutes, but that test's records could not be re-verified later; a checked trial at 1.0025 roughly halved the repeats (22 to 12 in 66 seconds) and raised Dolphin to 60.09 fps. Audio and game timing run fast by the same amount |
| 50 fps content (PAL) | uneven at every mode; at 60 Hz about 10 frames a second are shown twice | none clean |
| 60.000 fps video | at 120 Hz one long hold every 3.7 s (16.4 a minute); Chrome shows it as a four-refresh hold, VLC as a three-refresh one | none; at 60 Hz every second was clean |
| Game streaming at exactly 60 or 120 | drifts against 60.27 / 120.54 | ask the host for the panel's real rate (Artemis's custom refresh rate); the host tried did not hold it (it gave 164.83 Hz for 166.06) |
| 60 fps content at 120 or 165 Hz | 120.54 / 2 = 60.27, so the same as at 60; at 165 Hz frames alternate two and three refreshes | as above |

astra-governor judges lateness against the measured rate, so a 59.94 fps game
repeating a frame every three seconds is not mistaken for a starved one, and
the test bench does not count those repeats against a run.

## The controller

The panel's timing controller is a Himax HX8874 (device-tree node
`qcom,mdss_dsi_himax_hx8874_9p06_dsc_cmd`: command mode, dual DSI, display
stream compression). It times the refresh itself, on its own clock. The tablet
asks for a rate class, and the controller sorts any request into one of five
fixed classes (61-90 gives 90, 91-120 gives 120, 121-144 gives 144, 165 gives
165, anything at or below 60 gives 60). No request produced a sixth frame
length. Readback of the controller's scanline counter fitted every mode as a
whole number of lines (4320, 2880, 2160, 1800 and 1568) of one line time,
3.8406 microseconds; lower rates are the same scan with more blanking. That
fit is a consistent model, not proof of the physical clock.

## What was tried to correct it, and the outcome

Over three days of investigation (2026-11-09 to 11-12) the following were
examined. **No safe correction was found or applied, and the screen is not
fixed.**

- **Controller registers** over the vendor kernel's `/sys/reg_debug` nodes:
  the rate request and commit, the rate field, clock-generator parameters, and
  178 fields in the timing blocks. None moved the 60.272 Hz rate. The timing
  generator's blocks are set by the controller's own start-up scripts.
- **Android and vendor display controls**: native mode selection is integer
  only; the vendor's LTPO, VRR, AVR, EPT, frame-interval and transfer-time
  paths were traced in the driver. A complete read-only query of the live DSI
  connector (33 properties) and CRTC (61) found no attached property that sets
  a fractional period. `/proc/driver/lcd_fps` is cached reporting, not a
  control.
- **The controller's flash** was read (12 MB, code and configuration read
  twice and matching), never written. Changing the frame timing would mean
  changing its start-up configuration in flash, which has no independent
  recovery path.
- **Comparable firmware** (an Honor tablet with a related panel) was searched
  offline for a timing configuration to compare against; nothing usable.

**Hazards.** Register traffic through `/sys/reg_debug` caused kernel panics
when the display was off or powering down, when a read asked for more than 4
bytes, and when unpopulated blocks or the controller's RAM were read; some
reads broke the display link until a reboot. Android partition recovery does
not recover the panel controller's flash. astra-governor sends no panel
traffic, and none should be added to it.

The full research record, register map, tools and evidence are kept outside
this repository, in `~/workspace/astra-root/governor/` (`README.md`,
`HANDOFF.md`, `tcon-fw/SCREEN-SOFTWARE-RESULT.md`,
`tcon-fw/TIMING-SOURCE-OWNERSHIP.md`) and `~/workspace/astra-root/evidence/`.
They contain earlier claims that later work corrected; the summary above
follows the corrections.
