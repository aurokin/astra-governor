# How it works

The vendor's system, where astra-governor fits in it, and how it works, in
three depths: the first section in a minute, this page in ten, and
[the vendor's system](vendor-system.md) and [the control law](control-law.md)
for everything measured about each.

## In one minute

- **The vendor's system.** A game's mode in Game Space (Eco, Balance, Rise,
  Diablo) sets the highest clocks the CPU and GPU may run at. Under those,
  the vendor's frame governor, IFAS, lowers a CPU cap for as long as frames
  are on time, and the stock governors pick the actual clock from how busy
  the CPU and GPU are. The thermal engine cuts the maximums as the board
  heats up.
- **The trouble.** IFAS raises its cap only once frames are already late, and
  busy-based governors misjudge emulators, whose busiest thread and GPU take
  turns within each frame, so neither looks busy while the frame is late.
  Games end up on the edge of their deadline: Super Mario Galaxy's heavy scene
  at 71% of its speed in Balance, the GPU at its lowest clock. Diablo fixes it
  by pinning every clock at the top, at twice the power or more.
- **What astra-governor does.** It takes over one decision: how *low* the
  clocks may go during a game. It watches every frame the game shows, starts
  with ample minimums and lowers them a step at a time while the game stays on
  time; a late frame sends them back up. During games it holds IFAS's cap at
  the mode's ceiling. It leaves the mode's maximums and the vendor's heat limits
  alone, and hands everything back when the game ends.

## The stack, with astra-governor in it

```
Game Space mode           sets the maximums per game ............ untouched
  |
IFAS (kernel module)      lowers a CPU cap while frames are ..... during games: four
  |                       on time                                 parameters rewritten,
  |                                                               cap stays at the ceiling
  |
walt (CPU),               pick the clock between the minimum .... astra-governor sets
KGSL devfreq (GPU)        and the maximum they are given          the minimums
  |
thermal-engine            cuts the maximums on board heat ....... untouched; in Rise the
                                                                  heat control stays
                                                                  under its first cut
```

On the CPU every limit is a separate request on the same cpufreq policy: the
lowest maximum wins, and the highest minimum up to it. astra-governor adds
requests of its own and cannot remove the vendor's; a minimum it sets is never
above the vendor's current maximum.

## In each situation

| Situation | What it does |
| --- | --- |
| A Game Space game in front, Eco, Balance or Rise | the control law runs: frames read twice a second, minimums set |
| Rise, board heading for the vendor's cut | the heat control takes the GPU from 1100 to 967 MHz, then lowers CPU maximums a step at a time, no further than the vendor's own first cut |
| Diablo | stands down; the vendor pins every clock |
| Anything else, screen on | holds the six performance cores' minimum at 1152 MHz (the vendor's is 384); watches no frames |
| Screen off | nothing |

## Each vendor part, and what astra-governor does with it

| Vendor part | What astra-governor does |
| --- | --- |
| Game Space modes and their ceilings | nothing; it works beneath them |
| Whether a game scene is active | reads `/sys/kernel/ifas/ifas_enable`, never writes it |
| IFAS lowering its CPU cap | neutralised during games: four of its parameters are rewritten so the cap goes to the mode's ceiling and stays there |
| IFAS's own GPU policy | held at the 0 the vendor ships, during games, in case an update switches it on |
| How low clocks may go | decided here, through `scaling_min_freq` of both clusters and `kgsl-3d0/min_pwrlevel` |
| The stock CPU and GPU governors | nothing; they choose the clock above the minimum |
| Thermal limits | nothing. A minimum is never set above the vendor's current maximum; a maximum of its own (heat control) is only ever lower than the vendor's |
| Fan | nothing unless `-fan` or `-fanhot` is given |
| Panel | nothing |

IFAS stays loaded and switched on. Its other parameters, its frame
measurement and its GPU behaviour are left alone, and its four values go back
the moment the game scene ends.

The IFAS parameters rewritten during games, and what each is set to:

| Parameter | Stock | During games | Effect |
| --- | --- | --- | --- |
| `normal_frame_keep_count` | 8 | 100000 | on-time frames before the cap is lowered: in effect never |
| `rescue_freq_level_up` | 0 | 15 | when the cap is raised, straight to the top |
| `scaling_a` | 310 | 5 | part of the same cap calculation |
| `cpu_loading_enable` | 1 | 0 | its load-based cap adjustment off |
| `enable_gpu_policy` | 0 | 0 | held off |

The combined effect (the cap rises to the mode's ceiling and stays) is
measured; the meaning of each parameter on its own is read from its name.

## The control loop, in brief

Every half second, while a game scene is active:

1. **Sense.** Read the game's present times from SurfaceFlinger
   (`dumpsys SurfaceFlinger --latency <layer>`), choosing the layer that
   presented the most frames in the last second; the panel's measured refresh
   period; the GPU's busy share (sampled ten times a second); the busy share
   and core of the game's busiest thread (`/proc/<pid>/task/*/stat`); and how
   high the stock CPU governor has been reaching (`stats/time_in_state`).
2. **Decide.** The control law (`control.go`) learns the rate the game reaches
   at ample clocks, then lowers the CPU and GPU minimums a step at a time while
   that rate holds, and goes straight back to ample clocks on a late frame it
   cannot explain. Special cases cover content with almost no work (streams,
   menus), games the GPU cannot carry to the panel's rate, and GPU clock held
   on suspicion. [Control law](control-law.md) has the whole of it.
3. **Apply.** Write the minimums that changed. The CPU minimum goes to the
   cluster the busiest thread runs on; the other cluster gets what its own
   busiest game thread needs for the same headroom.

## Safety and hand-back

- Everything written is runtime sysfs state; a reboot clears it.
- When the game scene ends, Diablo is switched on, or the daemon exits on a
  signal, it puts the minimums back at the bottom of each table, the GPU at its
  base level, its own maximums at the top, and the IFAS parameters at their
  stock values.
- A new instance undoes floors a killed one left behind (outside Diablo,
  nothing else on the tablet raises them), and for five seconds after a game
  scene ends it puts back Diablo's pins if the vendor's switch to Diablo landed
  under its own restore.
- `/enable` and `/disable` on `127.0.0.1:7878` switch control on and off
  without stopping the daemon.

These hand-overs are what `recorder/handover.py` in the test bench checks on
the tablet; see [development.md](development.md#checking-a-build-on-the-tablet).

## Code map

| File | What is in it |
| --- | --- |
| `control.go` | the control law: one sample in, two minimums out; no I/O. Tested by `control_test.go` against a model game |
| `heat.go` | the heat control: projects the board sensor 90 s ahead and decides how many steps of CPU ceiling to give up. Tested by `heat_test.go` |
| `main.go` | sensing, sysfs, game-scene tracking, hand-back, fan, the non-game floor, HTTP status and flags |
| `fan_test.go` | the fan level decision |
| `clock_linux.go`, `clock_other.go` | `CLOCK_MONOTONIC`, to match SurfaceFlinger's timestamps |
| `module/` | the Magisk module: `service.sh` (start at boot, restart if it dies), `module.prop`, `install.sh` and `switch.sh` (update from a computer) |
| `tools/results.py` | tables from the measured runs in `data/` |
