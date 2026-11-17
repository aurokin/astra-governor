# Measured runs

`campaigns/<name>.jsonl` holds one line per recording: the summary the test
bench wrote after each run (760 runs, 2026-11-13 to 11-17). The raw
recordings (every frame's present time, clocks ten times a second) are about
670 MB and stay with the test bench in `~/workspace/astra-root/lab/runs/`.

```sh
python3 tools/results.py campaigns
python3 tools/results.py table real2 stock v2 v2rise diablo
python3 tools/results.py runs native3
```

## Arms

| Arm | What controlled the clocks |
| --- | --- |
| `stock`, `stockrise` | the vendor alone, in Balance / Rise |
| `astrad` | the governor installed at the time, in Balance: the first governor before 2026-11-14 11:37 (`real1`, `real2`, `sweep*`, `loadgen-first`, `validate`), astra-governor after |
| `rise` | the installed governor, in Rise |
| `eco`, `diablo` | those vendor modes, the installed governor on (it stands down in Diablo) |
| `v2`, `v2rise` | astra-governor run as a test build in Balance / Rise; flags after `+` (`v2rise+heat=48+thrifty`), `bin=NAME` for another build |
| `holdCxG` | the test build holding fixed minimums of C MHz (CPU) and G MHz (GPU) |
| `staticN` | the vendor's own knobs held fixed (CPU-cap ratchet, GPU load gain N) |
| `asis`, `p0=KHZ`, `gpu=N` | outside games: nothing touched, or a minimum held by hand |
| `app`, `app-stock`, `balance`, `rise` (chrome) | Chrome as an ordinary app with and without the non-game floor, or as a Game Space app |

## Fields

| Field | Meaning |
| --- | --- |
| `pacing.fps`, `target_fps`, `speed_pct`, `panel_hz` | throughput, the content's rate, their ratio, the panel's measured rate |
| `pacing.clean_seconds_pct` | seconds with no late frame beyond what the panel forces |
| `pacing.excess_long_holds_per_s` | late frames: one-refresh delays the panel does not explain, per second |
| `pacing.hitches_per_min`, `worst_gap_ms` | delays of two refreshes or more; the longest gap |
| `pacing.holds` | how many frames were held for 1, 2, 3... refreshes |
| `clocks.*` | mean CPU and GPU clocks, median ceilings, GPU busy share, GPU levels |
| `thermal.*` | skin at start and end, hottest CPU and GPU junction, fan level |
| `power.system_w` | the tablet's draw (USB input less the battery's charge, or the battery's draw) |
| `threads.*` | the app's busiest threads: busy share, time on the prime cores |
| `state.*` | performance mode, refresh setting, display mode, battery, IFAS at the start |
| `astrad2_actions` | the governor's actions during the run, counted |

## Campaigns

| Campaign | What it measured |
| --- | --- |
| `setup-check` | every staged scene launched and checked, nothing controlled |
| `loadgen-first`, `validate`, `sweep1`-`sweep3` | synthetic loads: the vendor, the first governor, fixed knobs, astra-governor |
| `real1`-`real7`, `dev` | emulator and native scenes at 60 Hz; `real2` is the main comparison |
| `soak1`-`soak3` | twenty minutes or more of God of War II per arm |
| `floors1` | fixed minimums, to see what minimums alone do |
| `heat1`-`heat8`, `heat5-warm` | heat control, the fan, standing and flat, GPU heat, an hour-long load |
| `turnip1` | Eden on the Mesa Turnip driver |
| `nongame1`, `nongame-165`, `nongame-auto`, `nongame2` | video, scrolling, YouTube outside games |
| `stream1`-`stream5` | Artemis and Moonlight streams |
| `panel1`-`panel3` | a game at 90, 120, 144 and 165 Hz (`panel1`'s two synthetic `rise` runs are void: the panel had gone back to 60 Hz) |
| `chrome0`, `chrome1` | a web game and a video in Chrome at every rate, in and out of Game Space |
| `native1`-`native5` | SuperTuxKart, Shattered Pixel Dungeon, Geometry Dash at 120-165 Hz; the GPU-bound rule; long runs |
| `ceil1`-`ceil3` | CPU maximums switched in blocks in a GPU-limited game |
| `fan0`-`fan2` | the fan switched in blocks; matched-start fan pairs |
| `turns1` | the alternating-descent build against the installed one |
| `thrifty1`-`thrifty3` | the thrifty GPU rule from the start; Soulcalibur against the vendor |
| `release1` | the 0.3 release zip installed fresh through Magisk, after a reboot: Super Mario Galaxy against stock |
| `_loadgen_csv` | per-frame logs written by the synthetic load app itself |
