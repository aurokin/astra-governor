# Results

Everything measured on the tablet, by scenario. Each section names the
campaigns it comes from; their per-run summaries are in
[`data/campaigns/`](../data/README.md) and `tools/results.py table CAMPAIGN`
prints them. How the numbers are made is in
[development.md](development.md#how-a-run-is-measured).

**Reading the numbers.** *Clean* is the share of seconds with no late frame
beyond what the panel itself forces. *Late frames* are extra one-refresh delays
per second. *W* is the whole tablet's draw. Power on the USB cable counts the
battery's share, but runs on the cable are not identical to runs on battery.
Most comparisons are one run per arm: differences under about half a watt, or
a few points of clean seconds, are within the spread between two runs of the
same thing.

## Summary

| Scenario | Vendor alone | astra-governor | Diablo |
| --- | --- | --- | --- |
| Mario Galaxy, heavy scene, Balance | 71% speed, 0% clean, 17 late frames/s | 100% speed, every second clean, 4.5 W | the same frames at 7.5 W |
| Emulator scenes, 60 Hz (16 scenes, Balance) | 7 of 16 below 90% clean | 15 of 16 at 88% clean or better | 1.5-2x the power; worse than the governor in two |
| SuperTuxKart, 165 Hz, 30 min | 1.35 late frames/s, 74% clean | 99.9% clean, 8.7 W | |
| Synthetic game, 165 Hz | 161 fps, 4.7 late frames/s | 166.1 fps, every second clean | the same frames at 10.6 W |
| Game streaming, 120 Hz | 3.7 W, decode 7.3 ms | 3.7 W, decode 5.8 ms | |
| 60 fps video, outside games | 58.7-58.9 fps | 59.8 fps, no measurable power | |
| GPU-limited game at 165 Hz | ~94 fps, 15.7 W | ~94 fps, 15-16 W | 91 fps, 19.3 W |

Where a game can reach its rate, the governor either removes late frames the
vendor lets through or matches the vendor; it never measured worse on frames.
It costs power where the vendor was starving the game, and up to 2 W for two
or three minutes after a launch.

## Emulators at 60 Hz

Campaign `real2` (2026-11-14): 22 scenes, a minute each, every arm on the same
running scene. "First governor" is the 2026-11-09 design; "astra-governor" is
the build of 2026-11-14, before the light, GPU-bound and thrifty rules (none of
which these scenes trigger except Soulcalibur and F-Zero GX).

| Scene | Vendor, Balance | First governor, Balance | astra-governor, Balance | astra-governor, Rise | Diablo |
| --- | --- | --- | --- | --- | --- |
| Mario Galaxy, castle gate (Wii) | 0% clean, 3.7 W, 71% speed | 100%, 4.3 W | 100%, 4.5 W | 100%, 4.5 W | 100%, 7.5 W |
| Mario Galaxy, festival (Wii) | 66%, 4.2 W | 100%, 4.8 W | 100%, 4.8 W | 100%, 4.7 W | 100%, 8.7 W |
| Xenoblade (Wii) | 73%, 4.0 W | 84%, 4.1 W | 97%, 4.5 W | 96%, 5.0 W | 79%, 9.4 W |
| F-Zero GX attract (GameCube) | 78%, 3.6 W | 83%, 4.4 W | 87%, 4.5 W | 94%, 5.8 W | 94%, 7.5 W |
| Wind Waker (GameCube) | 82%, 3.3 W | 91%, 3.6 W | 94%, 3.6 W | 91%, 3.7 W | 96%, 7.0 W |
| Harry Potter, 25 fps PAL (GameCube) | 90%, 3.7 W | 51%, 4.2 W | 94%, 3.9 W | 99%, 4.4 W | 99%, 7.5 W |
| God of War II (PS2) | 95%, 6.0 W | 83%, 6.4 W | 98%, 6.6 W | 100%, 8.6 W | 100%, 12.7 W |
| Shadow of the Colossus (PS2) | 97%, 6.7 W | 93%, 6.4 W | 100%, 7.4 W | 97%, 10.6 W | 66%, 12.3 W |
| Soulcalibur attract (Dreamcast) | 79%, 3.1 W | 80%, 3.5 W | 89%, 4.8 W | 89%, 5.2 W | 89%, 5.4 W |
| Super Mario Odyssey (Switch) | 100%, 3.9 W | 100%, 4.1 W | 100%, 4.2 W | 100%, 4.1 W | 100%, 6.9 W |
| A Link Between Worlds (3DS) | 100%, 3.3 W | 100%, 3.4 W | 100%, 3.3 W | 100%, 3.7 W | 100%, 5.9 W |
| Pokemon X (3DS) | 98%, 3.1 W | 100%, 3.2 W | 100%, 3.1 W | 100%, 3.2 W | 100%, 4.7 W |
| God of War: Chains of Olympus (PSP) | 100%, 3.1 W | 100%, 3.2 W | 100%, 3.1 W | 100%, 3.2 W | 100%, 5.0 W |
| Mario Kart DS (DS) | 100%, 3.2 W | 100%, 3.6 W | 100%, 3.7 W | 100%, 3.2 W | 100%, 5.6 W |
| Super Mario World, Metroid Fusion (RetroArch) | 100%, 2.7-2.8 W | 100% | 100% | 100% | 100%, 4.5 W |
| SuperTuxKart race on the grid (native) | 100%, 4.3 W | 100%, 4.7 W | 100%, 4.6 W | 100%, 4.9 W | 100%, 7.6 W |

- **Rise against Balance.** For heavy games (God of War II, F-Zero GX) Rise is
  better on every count but power; for light and medium games the two come
  out the same, because the governor picks low clocks in both. The vendor
  alone in Rise did not fix Mario Galaxy's gate (0% clean): the problem is the
  vendor's frame governor, not the mode's ceilings.
- **Diablo** gets every frame the clocks can buy at 1.5-2 times the power, and
  in Xenoblade and Shadow of the Colossus it measured worse than the governor
  (its own heat).
- The 25 and 30 fps games show pairs of one short and one long frame as their
  clock drifts past the panel's, in every arm; that counts against *clean*
  without saying anything about the clocks.

## Panel rates above 60 Hz

Game Space starts every listed app on its own panel rate, 60 Hz unless one is
set ([vendor-system.md](vendor-system.md#refresh-rate)).

**Synthetic game that can run at the panel's rate** (`panel2`, `panel3`, Rise):

| Panel | astra-governor | Vendor alone | Diablo |
| --- | --- | --- | --- |
| 90 Hz | 90.4 fps, every second clean, 7.0 W | 90.4 fps, clean, 5.1 W | |
| 120 Hz | 120.5 fps, every second clean, 6.8 W | 120.5 fps, clean, 5.0 W | 120.5 fps, 10.0 W |
| 144 Hz | 144.7 fps, every second clean, 6.6 W | 144.7 fps, clean, 5.6 W | |
| 165 Hz | 166.1 fps, every second clean, 7.7 W | 161.3 fps, 4.7 late frames/s, 5.7 W | 166.1 fps, 10.6 W |

At 120 Hz the vendor alone happens to hold it with the GPU 84% busy; the
governor wants the busy share under 80%, which costs 1.8 W for the same frames.

**SuperTuxKart, a race driven by scripted touches** (`native1`, `native3`, Rise):

| Panel | astra-governor | Vendor alone |
| --- | --- | --- |
| 120 Hz, 2 min | 120.54 fps, every second clean, 6.6 W | 120.50 fps, 98.4% clean, 6.3 W |
| 144 Hz, 90 s | 144.65 fps, every second clean, 7.8 W | 143.93 fps, 93.7% clean, 7.1 W |
| 165 Hz, 30 / 15 min | 166.05 fps, 99.9% clean, 8.7 W | 164.71 fps, 74.0% clean, 1.35 late/s, 8.2 W |
| 165 Hz, Balance, 2 min | 166.06 fps, every second clean, 9.4 W | 162.47 fps, 24% clean, 3.6 late/s, 8.4 W |

In the 30-minute run the board peaked at 39.8 C; the one late moment was the
race ending at 22 min 40 s.

**Geometry Dash Lite** (the only commercial game staged; `native3`): 166.06 fps,
every second clean, for 20 minutes at 4.0 W; the vendor alone the same at 3.9 W.
**Shattered Pixel Dungeon** (2D): 166 fps, 3.6 W either way.

**60 fps content on a faster panel** (Dolphin, Mario Galaxy): 59.94 fps at 120
and 165 Hz, 95-100% of seconds clean.

## A game the GPU cannot carry to the panel's rate

SuperTuxKart with every graphics option on, 165 Hz, Rise (`native1`-`native4`,
`fan2`): 90-105 fps with the GPU full at 1100 MHz.

- **Before the GPU-bound rule** the governor held the CPU at the top for the
  whole race: 19.1 W against the vendor's 16.4 W. With it: 15-16 W, level with
  the vendor.
- **The vendor's heat cap decides the frame rate.** The board reaches 50.2 C in
  two to four minutes; the vendor caps the GPU at 832 MHz until 48.1 C (about
  45 s), and the rate swings between 77-80 and 95-105 fps every minute and a
  half. Once the tablet is warm through it stays capped: 78-88 fps at 11 W.
- **Long pairs, order swapped** (15-20 min): the governor 87.8 and 94.6 fps,
  the vendor 95.2 and 97.6 fps; the governor's runs had the warmer starts, and
  two pairs neither show nor rule out a cause. Matched-start pairs of 8 minutes
  (`fan2`): 96.5 and 94.9 fps for the governor alone.
- **CPU maximums cost it nothing** (`ceil2`, `ceil3`: maximums switched every
  ten seconds on a fixed view): 94, 94, 93, 93, 91, 92, 92 fps, top and low in
  turn.
- **Diablo**: 90.9 fps at 19.3 W, already limited in the first minute.

A game like this wants its own frame cap.

## Game streaming

Artemis and Moonlight from a desktop host (`stream1`-`stream5`). Artemis is a
Game Space app, so it gets a game scene and both frame governors.

| Stream | astra-governor | Vendor alone |
| --- | --- | --- |
| 1080p60, 60 Hz panel | 3.13 W, decode 6.4 ms | 3.13 W, 8.0 ms |
| 1080p120, 120 Hz panel | 3.67 W, 5.8 ms | 3.70 W, 7.3 ms |
| 1440p120, 120 Hz panel | 3.67 W, 7.3 ms | 3.56 W, 8.4 ms |
| A game at 165 Hz, 20 min (Artemis) | 3.6-3.7 W, 4.8 ms, board flat at 31 C | 3.9 W, 5.7 ms |
| The same, Moonlight | 3.9 W, 3.9 ms | 3.8 W, 4.2 ms |

- The tablet drew every frame that arrived, in every arm; the host sent 51 of
  60, 91-94 of 120 and 116-124 of 165. Pacing figures describe the host.
- Balance is enough: Rise changed nothing measurable.
- Before the light rule, the governor sat at the top clocks for 4.0 W and no
  extra frame.

## Apps that are not games

`nongame1`, `nongame-165`, `nongame-auto`, `nongame2`, `chrome1`. Outside Game
Space both frame governors are off.

- **Video** in VLC: a 60 fps clip at 58.7-58.9 fps with the vendor's clocks;
  59.8 fps with a 1152 MHz minimum on the performance cores (749 MHz: 59.3;
  1555: 59.9). At 120 Hz late frames fell from 0.9-1.0 to 0.2-0.4 a second. No
  power cost: 3.36 against 3.32 W over six pairs, 2.35 against 2.40 W idle.
  This is the non-game floor the module runs with.
- **Scrolling** (Settings, a replayed finger): 0.2-0.3% of frames late at
  165 Hz and 0.3-0.8% at 60 Hz, no better with a floor.
- **Chrome**: a web game's animated menu (tibo.dog) at the panel's rate with
  every second clean at 60, 90 and 120 Hz under the vendor's Chrome cap;
  above 120 Hz Chrome needs to be a Game Space app (then 144.7 and 166.1 fps,
  clean, 3.7 and 4.1 W). A 60 fps video: 60.00 fps at every rate, with one
  long hold every 3.7 s at 120 Hz (the panel's real rate; see
  [display.md](display.md)).
- **YouTube**: 41 frames a second of a 60 fps upload in its page player, with
  and without the floor; not the clocks, cause not found.

## Heat and the fan

God of War II for 28 minutes (`heat1`-`heat6`), a synthetic GPU-heavy load for
65-75 minutes (`heat7`, `heat8`), the heavy SuperTuxKart race (`native5`,
`fan2`).

- **Rise, no heat control**: 10 W, the vendor's cut at 49 C after 18-26 minutes.
- **Rise, `-heat 48`**: no cut in 28 minutes, three runs of three; 8.8 W
  against 10.2 W, late frames unchanged (0.5-0.7 a minute). 71 minutes
  standing: no cut.
- **Balance**: cut after 14 minutes at 7 W, and its own ceilings already cost
  2.7-3.3 late frames a minute in this game's fights.
- **Heat from the GPU** (10.5 W, 65 minutes): the board levelled off at 49.7 C,
  the GPU was never limited and every frame was on time.
- **The fan**, three ways, no effect on any sensor, power or frame rate:
  - level 5 (20,000 rpm) against the vendor's level 2 (9,000) in God of
    War II;
  - an hour of one steady 10 W load with the fan switched between off, level 2
    and level 5 every six minutes (`fan1`): the GPU's junction stayed 35-36 C
    above the board in every block, the fan off included;
  - four matched-start runs of the heavy race (`fan2`): GPU capped from 2.4-3.1
    minutes, 94.5-96.5 fps, either level.
- **The GPU's top level given up first** (branch `gpu-first-heat`,
  `gpufirst1`-`gpufirst3`, 2026-11-18): the heavy race for 30 minutes at
  165 Hz, the heat control taking the GPU from 1100 to 967 MHz before any CPU
  step. One valid pair (`gpufirst2`); the other runs were spoilt by the
  vendor's drop to 120 Hz or a system-update notice over the game.

  | | Installed build | GPU-first |
  | --- | --- | --- |
  | Minutes 0-7 | ~99 fps, 15-17 W, GPU 1100 MHz | ~96 fps, 13-13.5 W, GPU 967 MHz |
  | Vendor's GPU cut (832 MHz) | from minute 4, 74% of the run | from minute 7, 74% of the run |
  | Whole run | 91.5 fps, 12.5 W | 88.6 fps, 11.6 W |

  The race is too hot to hold under the cut even at 967 MHz. The cut comes
  later because the GPU was already slower, so the step trades 2-3% of frames
  for 1-2.5 W, which a frame cap would also do. Not adopted. Starting skin
  temperatures were 26.8 and 31.2 C: weak evidence. The cap itself works:
  `devfreq/max_freq` held the GPU at 967 MHz throughout (`gpucap0`).
- **Unexplained cooling spells**: several times the board, skin and battery
  fell 2-5 C over 5-12 minutes at an unchanged load, fan speed and clocks.
  Room air moving across the tablet's back is a guess.

## Giving back unused GPU clock from the start

`thrifty1`, `thrifty2` (2026-11-16), each build from a fresh launch:

| Game | Without | With |
| --- | --- | --- |
| Soulcalibur | 6.84 / 3.77 W (GPU sometimes at 1060 MHz, 4% busy) | 4.26 / 3.93 W, GPU 250-260 MHz, 96.5% clean |
| God of War II | 9.58 / 8.64 W | 8.44 / 8.38 W |
| F-Zero GX | 5.22 / 5.50 W | 5.58 / 5.26 W |
| Xenoblade, Mario Galaxy, Mario Odyssey, Harry Potter | | 0.1-0.3 W less each |
| Shadow of the Colossus (30 fps, outside the rule) | 10.85 W | 10.87 W |

46.3 W against 50.4 W summed over eight games; no game's frames worse beyond
its usual spread. Installed 2026-11-17.

## Not measured

- A commercial 3D game (Real Racing 3 and Bullet Force need the Play Store
  signed in).
- Play on battery, unplugged, over Wi-Fi; every run so far was on the USB cable.
- A game at 120-165 Hz for longer than 30 minutes; a stream longer than 20.
- Input latency; a controller; Wi-Fi under load for streaming.
- Eco mode beyond one synthetic run.
