# Astra governor

Smooth frame rates on the RedMagic Astra without Diablo mode.

The Astra's own frame governor saves power by letting games run right on the
edge of their deadline, so emulators in Balance or Rise stutter and drop
frames, and Diablo fixes it only by pinning every clock at the top. This
Magisk module watches every frame a game shows and gives it the lowest CPU and
GPU clocks that keep it on time: Diablo's smoothness at a fraction of its
power and heat.

| Super Mario Galaxy, heaviest scene | Smooth seconds | Speed | Power |
| --- | --- | --- | --- |
| Balance, stock | 0% | 71% | 3.7 W |
| Diablo | 100% | 100% | 7.5 W |
| Balance, with Astra governor | 100% | 100% | 4.5 W |

## Is it for you?

You need:

- a **RedMagic Astra (NP05J)** on **RedMagicOS 11.0.7** (global build); it was
  built and measured on one tablet with that firmware;
- an **unlocked bootloader and root with Magisk** (20.4 or later).

It does nothing on any other device: the daemon checks for the Astra's
kernel interfaces and refuses to start without them.

## Install

1. Get `astra-governor-<version>.zip` (build it with `sh module/build-zip.sh`;
   no releases are published yet).
2. In the Magisk app: **Modules → Install from storage**, pick the zip.
3. Reboot. The governor starts about 15 seconds after the tablet has booted.

To update, install the new zip the same way. To remove it, remove **Astra
governor** in Magisk's Modules list and reboot; to pause it, switch the
module off there.

## Using it

- **Games must be in Game Space.** The governor only takes over in apps that
  Game Space treats as games, outside Diablo. Add an emulator or game to Game
  Space if it isn't there.
- **Pick Balance or Rise per game, not Diablo.** Balance is enough for most
  emulators, streaming and lighter games. Use Rise for the heaviest games (PS2,
  demanding GameCube and Wii scenes): it keeps more headroom and the vendor's
  heat limits start later. In Diablo the governor stands aside.
- **Refresh rate per game.** Game Space starts each game at 60 Hz unless you
  change it with the refresh button in the in-game overlay. The governor holds
  120, 144 and 165 Hz games at full rate too.
- **Streaming** (Moonlight, Artemis): add the app to Game Space and use
  Balance. The governor recognises a stream and keeps the clocks low.
- **Outside games** it only keeps the performance cores from idling so low
  that video drops frames. No setup needed.

## What to expect

Measured on the tablet against the stock behaviour in the same mode
(details in [docs/results.md](docs/results.md)):

| | Stock | Astra governor |
| --- | --- | --- |
| Emulator scenes at 60 Hz, Balance | 7 of 16 scenes below 90% smooth seconds | 15 of 16 at 88% or better |
| SuperTuxKart at 165 Hz, 30 minutes | 1.35 late frames a second | 99.9% smooth seconds |
| Games at 120 and 144 Hz | occasional late frames | every second smooth |
| Game streaming | the same frames | the same frames, decoded up to 1.6 ms faster, same power |
| 60 fps video outside games | 58.8 fps | 59.8 fps, no measurable power cost |

Against Diablo, the same smoothness in nearly every game at half to two thirds
of the power.

## Good to know

- **The first two or three minutes after starting a game** use 1.5-2 W more
  than later, while the governor works down from ample clocks.
- **In Rise it stays under the vendor's heat cut** where it can: God of War II
  ran 28 minutes without the cut, three times out of three, at 1.4 W less than
  without this heat control (which reached the cut after 18-26 minutes).
- **A game too heavy for the GPU at your refresh rate** (an Android game at
  maximum settings at 165 Hz) still hits the tablet's heat limit; the governor
  cannot make the tablet cooler. Cap the game's frame rate or lower its
  settings.
- **The screen's refresh rates are slightly fast**: 60.27 Hz instead of 60,
  166.06 instead of 165. A 59.94 fps emulator therefore repeats one frame about
  every three seconds in any mode. That is the panel, not the governor; see
  [docs/display.md](docs/display.md).
- **Faster fan speeds don't help**: running the fan at full speed changed no
  temperature in three tests, so the governor leaves the fan to the system.

## Safety

The governor only sets clock limits that the kernel already exposes, and only
at runtime: a reboot clears every value. It never raises a clock above the
vendor's own limits, never touches thermal protection, the bootloader, the
firmware or the display, and hands every setting back when a game ends, when
you switch to Diablo, or if it stops. Removing the module returns the tablet
to stock behaviour after a reboot.

## More

[docs/](docs/README.md) explains how it works, how the RedMagic system decides
clocks and heat limits, the display, every measurement, and how to develop
and test changes.
