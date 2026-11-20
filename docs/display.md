# The display

The Astra's panel refreshes slightly faster than each of its modes claims.
This page is what that means for games and video, how display calibration
corrects it, what was measured, and what is not yet settled.

## In one minute

- **The fault.** The five refresh modes run 0.45% fast (0.64% at 165 Hz):
  "60 Hz" is 60.272 Hz. Content at exactly 60 or 59.94 frames a second cannot
  keep step with it, so a frame is repeated or dropped every few seconds, in
  every performance mode.
- **The correction.** The panel's timing controller keeps one horizontal and
  one vertical timing value for each mode. Calibration writes ten corrected
  values after every boot and wake, then has Android switch modes for a
  moment so the controller takes them up. Nothing is written to the panel's
  flash, and a panel power-down clears it.
- **The result.** Every mode within 0.004 Hz of its nominal rate, measured at
  the panel's own timing signal, on both supported firmware builds. The 60 Hz
  mode can instead be matched to 59.94, 59.73 or 60.10 Hz for consoles that
  ran at those rates.
- **The open problem.** Two system restarts on waking, on days calibration
  was in use, are unexplained. Calibration ships switched off and marked
  experimental until they are.

## The panel's real refresh rates

Measured at the controller's tearing-effect pin (kernel GPIO 598, one pulse
per refresh) against the tablet's own monotonic clock, with every edge fitted
over two five-second windows:

| Mode | Factory | Above nominal | Calibrated, 11.0.7 | Calibrated, 11.0.8MR1 |
| --- | ---: | ---: | ---: | ---: |
| 60 (Modern) | 60.2722 Hz | 0.453% | 59.99998 Hz | 59.99968 Hz |
| 90 | 90.4085 Hz | 0.453% | 89.99988 Hz | 89.99954 Hz |
| 120 | 120.5442 Hz | 0.453% | 119.99964 Hz | 119.99972 Hz |
| 144 | 144.6524 Hz | 0.453% | 143.99639 Hz | 143.99627 Hz |
| 165 | 166.0558 Hz | 0.640% | 164.99855 Hz | 164.99893 Hz |

The factory rate is the same in every vendor mode, Diablo included, in games
and on the home screen, on both firmware builds, and it does not wander.

The other three choices for the 60 Hz mode:

| Choice | Aimed at | Measured, 11.0.7 | Measured, 11.0.8MR1 |
| --- | ---: | ---: | ---: |
| NTSC | 59.94006 Hz (60000/1001) | 59.94006 Hz | 59.94018 Hz |
| GBA | 59.72750 Hz | 59.72711 Hz | 59.72696 Hz |
| NES | 60.09881 Hz | 60.09911 Hz | 60.09909 Hz |

**How good is the reference?** The pin is timed by the tablet's clock, which
ran 32-38 parts per million fast against the development Mac's. On the Mac's
clock the Modern rate is about 60.002 Hz. An audio clock or a video source
has its own small error of the same order, so the last digits above are
finer than anything content can be matched to.

## What that means for content

| Content | On the factory panel | Calibrated |
| --- | --- | --- |
| A native game paced by Android | runs at the panel's rate (60.27, 166.06...) | runs at 60.00, 165.00... |
| An emulator at 59.94 fps (GameCube, Wii, PS2) | one frame repeated about every 3 s | **NTSC**: in step. Modern: one repeat about every 17 s |
| Game Boy and GBA at 59.73 fps | one repeat every 1.8 s | **GBA**: in step |
| NES and Super NES at 60.10 fps | one repeat every 5.8 s | **NES**: in step |
| 60.000 fps video | at 60 Hz, 15-57 long frames in 30 s; at 120 Hz one long hold every 3.7 s | **Modern**: in step |
| 59.94 fps video | 14-18 long frames in 30 s | **NTSC**: in step |
| Game streaming at exactly 60 or 120 | drifts against 60.27 / 120.54 | **Modern**: in step with a host at 60.000 or 120.000 |
| 50 fps content (PAL) | uneven at every mode | still uneven: no mode is a multiple of 50 |

"In step" means the panel no longer forces a repeat; a game can still miss a
frame on its own. The video rows and the 59.94 fps emulator row were measured
as frames presented; the Game Boy and NES rows are arithmetic from the
measured rates ([what is not measured](#measured-and-not-measured)).

The governor judges lateness against the rate the panel is actually running
at, so it works the same with calibration on or off, and the test bench does
not count repeats the panel forces against a run.

## How calibration works

The panel's timing controller is a Himax HX8874 (device-tree node
`qcom,mdss_dsi_himax_hx8874_9p06_dsc_cmd`: command mode, dual DSI, display
stream compression). It times the refresh itself, on its own clock. The tablet
only asks it for one of five rate classes.

Inside the controller, block `0xE781` holds a horizontal and a vertical
timing word for each class, ten words in all. Three things had to be found
before changing them did anything, which is why the first three days of
investigation concluded that the rate could not be changed
([history](#how-it-was-found)):

1. **A timing word only reads back changed after two more writes**
   (`0xE781016C = 1`, then `0xE781017C = 0`). Without them the controller
   keeps the factory value.
2. **The controller only takes up a mode's words when that mode is selected.**
   The mode in use keeps its old timing until the tablet switches to another
   mode and back.
3. **The vertical word alone moves the rate in coarse steps.** The horizontal
   word, changed four counts at a time, gives the fine ones. Each rate is the
   closest pair found near the factory values.

So calibrating is: write the ten words, each with its two follow-up writes
and a read-back; then, if the mode on screen was among those changed, ask
SurfaceFlinger for the other of 60 and 120 Hz for as long as it takes to see
the mode change, and put the display policy back exactly as it was. Android's
saved refresh setting is never touched. The higher modes take up their words
whenever Android next selects them.

The controller forgets all of it when the panel powers down, which happens at
every screen-off. Calibration is therefore reapplied after every wake.

### The parts

| Part | What it does |
| --- | --- |
| `panel-service` (Go, root) | decides when: waits for boot and for a display that has been steadily on, runs the helper once per wake, and reports its state on `127.0.0.1:7879/calibration` (read-only) |
| `panel-table.sh` (root) | the only thing that talks to the controller: checks, writes and verifies the ten words through the vendor kernel's `/sys/reg_debug` nodes, keeping a journal of what it owns |
| `panel-mode.jar` (`PanelMode.java`, run with `app_process`) | the momentary mode change through SurfaceFlinger, with a receipt so an interrupted one is undone |
| The Astra app | saves the owner's choice in a file in its device-protected storage; the service reads it. The app has no access to the panel |

### When it runs

| Moment | What happens |
| --- | --- |
| Boot | waits for Android to finish booting, then 30 s, then for the display to have been on for 8 s; about 67 s from the reboot request to **Calibrated** in two measured boots |
| Wake | 2 s after the display is steadily on, the helper runs for 2-6 s; one helper run per wake |
| In use | nothing: no reads, no writes. Mode changes use the words already in the controller |
| Choice changed in the app | applied within about a second while the screen is on |
| Switched off in the app | the factory words are written back and verified, then nothing more |
| Module switched off or removed in Magisk | treated as "switched off": factory timing is restored while the tablet is still running |
| Screen off | nothing is sent to the panel |

### What keeps it safe

The first days of work on this controller cost several kernel panics, all
from register traffic at the wrong moment ([hazards](#hazards)). The service
is built around not repeating them:

- **Only on a fully-on panel.** The helper checks the display's power state
  before every single transfer and stops at the first one that is not "on".
- **Only on a known tablet and firmware**: model NP05J, builds
  `REDMAGICOS11.0.7_NP05J_GB` and `REDMAGICOS11.0.8MR1_NP05J_GB`. The timing
  words were measured on those; on anything else it leaves the panel alone
  and says so.
- **Only a controller that identifies itself** (chip ID `0x0008874B`), and
  only words it recognises: a value in the controller that is neither factory
  nor one of its own stops it.
- **A journal** records each word before it is written, so an interrupted run
  is finished or undone at the next wake rather than guessed at.
- **A guard file** is put down, synced, before the first transfer and removed
  after the last. If the tablet dies in between, the service finds the guard
  at the next start and does nothing until the screen has been fully off and
  on again.
- **After any error it stops** until the next full sleep and wake, rather
  than retrying against a display link that may be unwell.
- **Four-byte reads only**, and only the registers named above.

## Measured and not measured

Measured, on both firmware builds unless noted:

- **Every rate at the pin**, above, and again after an ordinary wake.
- **Factory timing back** after switching off: 60.2721 Hz at the pin, all ten
  words read back as factory.
- **60.000 and 59.94 fps video in VLC, 30 s each.** On factory timing the two
  clips had 15-16 and 14-18 presentation intervals longer than 1.5 times the
  median; matched, each had none. In twelve later 60 fps captures on
  11.0.8MR1, eight had none against 32-57 on factory timing; two had 227 and
  430, with VLC reporting normal speed and the next capture on the same boot
  clean. The cause of those two stalls is not known.
- **Four sleep-wake cycles**: one helper run each, **Calibrated** each time,
  no display-link errors in the kernel log.
- **A game opened 1.0 and 3.5 s after waking** (SuperTuxKart at 165 Hz in
  Game Space, system setting on Auto): 165 Hz held, 164.998 Hz at the pin.
- **An interrupted run**: with the guard file left in place, the service
  stayed paused through a boot and resumed after one sleep and wake.
- **A 59.94 fps emulator on NTSC** (11.0.8MR1, campaign `mr1-ntsc1`): Super
  Mario Galaxy's heavy scene under the governor for two minutes showed 7,470
  frames for one refresh each and 2 for two. On factory timing the same scene
  repeats a frame every three seconds whatever the clocks do: 22 in 67
  seconds in `mr1-real1`. Android's present times put the panel at 59.940 Hz
  during the run.
- **Through the app, on the release zip**: on, Modern, NTSC, a sleep and
  wake, and off again, with the app's own meter reading 60.27, 60.00, 59.94
  and 60.27 Hz and no display-link error in the kernel log.

Not measured:

- **Light from the panel.** The pin is the controller's timing signal, not a
  photodiode on the glass.
- **The GBA and NES choices with an emulator**, and the NTSC one with any
  game but the one above.
- **Battery cost**, which should be nil: nothing runs once the words are in.
- **Weeks of use.** Everything above is from three days.

## The open problem: restarts on waking

Twice the tablet restarted by itself while waking: once on 11.0.7 (2026-11-18)
and once on 11.0.8MR1 (2026-11-19). Both times it came back up by itself.
After the second, a game's settings file and one setting written seconds
before had to be put back; nothing else was lost.

What is known:

| | 11.0.7 | 11.0.8MR1 |
| --- | --- | --- |
| The fault | a kernel panic consistent with `rpmh-rsc.c:1106`, Qualcomm's power-resource driver, 11 s after the touchscreen resumed | `rpmh-rsc.c:1106`, 10 s after the storage controller timed out entering its low-power state on wake |
| Calibration that boot | applied (Modern) | applied (Modern) |
| Last helper run before it | 32 minutes earlier, and it had been interrupted by the screen going off | 3 minutes earlier, completed; the run before that had been interrupted |
| The governor | running | stopped 77 s earlier |

- **The panic is not in the helper's own traffic.** In both cases the helper
  had finished, or been stopped, minutes before; the guard file agrees.
- **The same panic was seen on 11-09 and 11-10** when registers were written
  with the display off or powering down ([hazards](#hazards)). That is the
  reason to suspect calibration at all.
- **Both boots had a helper run cut short by the screen going off** before
  the panic. Three more interrupted runs, in the comparison below, were
  followed by no panic.
- **The vendor's write path does not exclude a power-down.** In the display
  driver (`msm_drm.ko`, the same file on both builds) a write through
  `/sys/reg_debug/gendwritelp` checks one "panel is initialised" flag and
  sends the packet without taking the panel's lock; reads, and the driver
  switching the panel off, take it. The helper's own check of the display
  state before each packet has the same gap. So a packet can be in transfer
  while the screen powers down, which is the condition that panicked the
  kernel on 11-09. Every read the helper makes starts with such a write.
- **It could not be reproduced on demand.** Six pairs of matched blocks on
  11.0.8MR1 (video, a race, battery standby), 24 app wakes on factory timing
  with both parts off and 24 with calibration on Modern, gave no restart
  either way, and no storage error in 2,290 traced storage events. Sixteen
  more wakes on factory timing gave none.
- **The governor ran alone for four days and several hundred recorded runs**
  before calibration existed, with no restart of this kind recorded.
- **An owner of another Astra has reported spontaneous restarts** on stock
  firmware; whether they are this fault is unknown.

What that adds up to: calibration is the likeliest trigger and is not proven
to be one; if it is, the path runs through something left behind after the
helper has finished or been interrupted, not through a transfer in progress;
and it is rare, roughly one in a few dozen wakes at worst. So calibration is
off until switched on, the app says why, and a tablet that restarts by itself
with calibration on should have it switched off.

What would settle it: provoking interrupted helper runs on purpose (screen
off two to four seconds after each wake) to see whether the panic follows
them. If it does, the helper's packets, now spread over two to six seconds
of each wake, would have to go out in one burst of a tenth of a second,
started only while Android is fully awake, so that a screen-off beginning
after the burst has started cannot reach the panel before it ends.

## Hazards

Register traffic through `/sys/reg_debug` caused kernel panics during the
investigation when the display was off or powering down (`BUG at
drivers/soc/qcom/rpmh-rsc.c`), when a read asked for more than 4 bytes (`BUG
at lib/string_helpers.c`), and when unpopulated blocks or the controller's RAM
were read; some reads broke the display link until a reboot. Android partition
recovery does not recover the panel controller's flash, which calibration
never writes. Any change to `panel-table.sh` should be read against the
[register rules](#where-the-rest-is) first.

## The vendor's refresh setting

Two behaviours of RedMagicOS that look like calibration faults and are not:

- **Auto can pull a 165 Hz game down to 120.** On 11.0.7 the Refresh Rate
  quick-settings tile rewrites the saved rate when it refreshes, every ten
  minutes, and Android then selects 120 Hz under a running game. With
  calibration off and factory timing confirmed at the pin it still happened.
  Removing the tile from the active tiles, or setting the system to a fixed
  165 Hz, held 165 for the eleven minutes measured. On 11.0.8MR1 the same
  tile left the game at 165, with and without calibration.
- **The preset only shows at 60 Hz.** If Auto holds an app at 120 Hz, it runs
  at a corrected 120.000 Hz and the 60 Hz choice is not in play.

## How it was found

From 2026-11-09 to 11-12 the controller's registers, Android's and the
vendor's display controls, the controller's flash image and a related Honor
tablet's firmware were all examined, and nothing moved the rate. The
conclusion then was that the timing was fixed by the controller's start-up
configuration in flash. That record, and its refuted attempts, are kept
[outside this repository](#where-the-rest-is).

On 11-18 the timing words in block `0xE781` were written again, this time
followed by the two writes the controller's own firmware makes after it sets
them, and then by a switch to 120 Hz and back. The pin moved from 60.2721 to
59.9941 Hz, held for a minute, and returned to 60.2721 Hz when the factory
word was put back. The same day the horizontal word gave finer steps, all
five modes were matched, and the service and app were written; the GBA and
NES choices followed. On 11-19 the helper stopped using Android's saved
refresh setting for the mode change (a game opened a second after waking
could lose its 165 Hz), the tablet was updated to 11.0.8MR1 and every rate was
measured again there.

## Where the rest is

Outside this repository, on the development Mac:

- `~/workspace/astra-root/governor/README.md`: the register map and the rules
  for talking to the controller, learned the hard way.
- `~/workspace/astra-root/governor/tcon-fw/`: the controller's firmware
  analysis from the first investigation.
- `~/workspace/astra-root/evidence/`: every capture behind this page.
  `astra-panel-apply-20261118` (the first change), `astra-all-rates-20261118`
  (all modes, the clock comparison, playback), `astra-auto-20261118` and
  `astra-reliability-20261118` (the service), `astra-migration-20261119`
  (11.0.8MR1), `astra-panic-20261119` and `astra-attribution-20261119` (the
  restarts).
