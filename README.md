# astra-governor

A frame-rate governor for the RedMagic Astra tablet (NP05J, RedMagicOS 11,
Snapdragon 8 Elite). The daemon, `astrad2`, watches every frame a game presents
and sets minimum CPU and GPU clocks so that the game is not starved, with as
little clock as that takes. It runs as root from a Magisk module.

It only supports this tablet's vendor kernel: it refuses to start without the
vendor's frame governor (IFAS), KGSL and cpufreq sysfs.

## Where it sits

The vendor's stack, top to bottom:

1. **Game Space mode** (Eco, Balance, Rise, Diablo) sets a game's clock
   ceilings and where the thermal ladder starts. GPU ceilings: 1100 MHz in
   Rise, 832 in Balance, 342 in Eco.
2. **IFAS**, the vendor's frame governor, runs while a Game Space game is in
   front outside Diablo. It lowers a CPU clock cap after every 8 on-time
   frames and raises it only once frames are clearly late.
3. **The stock CPU (WALT) and GPU (KGSL) governors** pick the clock between
   the minimum and maximum they are given.
4. **The thermal engine** cuts the maximums on the board sensor `xo-therm`.

`astrad2` adds a layer and replaces one decision:

| Vendor part | What astrad2 does |
| --- | --- |
| Modes and their ceilings | nothing; it works beneath them |
| Whether a game scene is active | reads `ifas_enable`, never writes it |
| IFAS lowering the CPU cap | switched off in games: `normal_frame_keep_count`, `scaling_a`, `rescue_freq_level_up`, `cpu_loading_enable` are rewritten so the cap goes to the mode's ceiling and stays (`enable_gpu_policy` is held at the 0 the vendor ships) |
| How low the clocks may go | decided here: `scaling_min_freq` per cluster, `kgsl-3d0/min_pwrlevel` |
| Stock CPU and GPU governors | nothing; they choose the clock above the minimum |
| Thermal limits | nothing; a minimum never exceeds a vendor cap, a maximum of ours is only ever lower |
| Fan | nothing unless `-fan` or `-fanhot` is given; then only ever faster than the vendor has it, and never when it is switched off |

Everything it writes is runtime sysfs state that a reboot clears. It hands
all of it back when the game leaves the screen, when Diablo is switched on,
and when it exits; a new instance undoes what a killed one left behind.

## What it does

**In a game** (twice a second, `control.go`):

- reads the game's presented frame times from SurfaceFlinger, the GPU's busy
  share, and the busy share and clock of the game's busiest thread;
- on a new scene asks for ample clocks for two seconds and takes the rate the
  game reaches as what the content wants;
- lowers the CPU and GPU minimums a step at a time while that rate holds,
  judging each step against the heaviest recent moment;
- on a late frame it cannot explain, goes back to ample clocks at once and
  leaves the setting that failed alone for a while;
- content with next to no work of its own (a stream, a menu) rests at low
  clocks; a game the GPU limits keeps the GPU's top clock while the CPU comes
  down.

**Heat** (`heat.go`, `-heat 48`, Rise only): from 45 C on the board sensor it
gives up GPU minimum the GPU's busy share does not call for; when the sensor
is heading past 48 C it lowers the CPU maximums a table entry at a time, no
further than the vendor's own first cut at 49 C.

**Outside games** (`-base 1152`): while the screen is on, the six performance
cores' minimum is held at 1152 MHz instead of the vendor's 384. Nothing else.

**In Diablo** it stands down.

## Build, install, undo

    go test ./...
    sh module/install.sh            # builds for the tablet, copies into the Magisk module, restarts; no reboot
    sh module/install.sh --first    # back to the first governor
    adb shell su -c 'curl -s 127.0.0.1:7878/disable'   # off until /enable or a restart
    adb shell su -c 'curl -s 127.0.0.1:7878/status'

`SERIAL=...` selects the device. The module starts the daemon with
`-listen 127.0.0.1:7878 -log /data/adb/astra/astrad2.log -heat 48 -base 1152`
(`module/service.sh`).

Flags: `-heat C`, `-base MHz`, `-fan LEVEL` (slowest fan level to allow in
games), `-fanhot` (top fan level while clocks are being cut for heat or the
board is within four degrees of the vendor's first cut), `-monitor` (observe
only), and for experiments `-hold` and `-thrifty`. Neither fan flag is in the
module's command line yet.

The vendor's fan settings, for reference: `system fan_state_of_mode` is 1 or
absent for "Balanced heat dissipation" (its own choice of speed) and 0 for
"Fast cooling"; `system fan_state_of_manual` is 100 / -100 when the system
switched the fan on / off for a game and 1 / 0 when the user did by hand;
`/sys/kernel/fan/fan_enable`, `fan_speed_level` (1-5) and `fan_speed_count`
(rpm) are what it is doing.

## Measurements

The test bench (frame recorder, scripted game scenes, sweeps, every recorded
run and the notes on them) is not in this repository. It lives in
`~/workspace/astra-root/lab`; its `README.md` has the results, and
`recorder/handover.py` there is the on-device check to run after changing the
daemon. It finds this repository at `~/code/astra-governor` or `$ASTRA_GOVERNOR`.
