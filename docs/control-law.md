# The control law

What `control.go` decides every half second, why, and which measurement each
rule came from. The code's comments carry the same reasoning next to each
constant; this is the map.

The law has no I/O. Each tick it gets one `sample` (frame rates over 0.4, 1
and 6 seconds, the panel's period, the GPU's busy share and clock, the busiest
thread's busy share and clock, how high the stock CPU governor has been
reaching, the highest minimums the vendor's current maximums allow) and
leaves two indices behind: the CPU minimum and the GPU minimum.

## The idea: start high, work down

The first governor (2026-11-09) started from the vendor's low clocks and
raised them on late frames. That fails whenever a starved game settles on a
clean fraction of the panel's rate: a 60 fps game held at exactly 30 looks
like a healthy 30 fps game, and nothing raises the clocks. This one works the
other way round:

1. **Learn.** On a new scene, ask for ample clocks (the vendor's current
   maximums) for two seconds and take the rate the game reaches as its target.
2. **Track.** Lower the CPU or GPU minimum one step at a time while the target
   holds, judging each step against the heaviest moment of the last minute or
   more, not the current one.
3. **Rescue.** On a late frame it did not cause by a step, go straight back to
   ample clocks and remember that the setting failed.
4. **Probe.** Content below the panel's rate gets a brief look at ample clocks
   now and then, in case it can do more (a 30 fps cutscene ended).

## Tracking: choosing a step

A step is allowed when the busy shares it would leave at the heaviest recent
moment, if the clocks sat exactly on the new minimums, stay under `slack`
(80%). The stock governors may run above a minimum, but they are what lets a
game starve, so nothing is assumed of them.

**Take turns or work side by side.** Whether the busiest thread and the GPU
take turns within a frame (their busy shares must fit into one frame together)
or work in parallel (each has a whole frame) decides how far clocks can come
down. Busy shares adding up past 110% for a while prove they work side by
side. Until then a step past the take-turns limit is a trial; a trial that
makes the game late is undone, and trials are off for ten minutes, twice as
long each time.

**Pace.** After a look at ample clocks the law comes down one step a second
while every step is safe. Otherwise it waits ten seconds of on-time frames
between steps (`slowDownTicks`). Those ten seconds are what lets it see the
stock CPU governor climbing above a new minimum, which is how bursts the
average busy share does not show become visible (`demand`): God of War II's
bursts need a core near its top clock although its busiest thread is busy a
third of the time.

**Undo.** A late frame within three seconds of a step undoes it, and that
setting is left alone for 30 s, doubling each time it fails.

## When a frame is late

| Situation | Response |
| --- | --- |
| Just after a step | undo it (above) |
| Both minimums already at the vendor's maximums | the game's own late frame: held against nothing (`uneven`). Late for two seconds running means the content slowed down: learn again |
| Anything else | rescue: ample clocks for at least five seconds, the failed setting remembered, the memory of heavy moments made longer |

Lateness is judged against the panel's measured rate, so a 59.94 fps game on
a 60.27 Hz panel repeating a frame every three seconds is not late.

**A target counted high.** The target is first a one-second count, replaced by
the six-second rate after six seconds on time. A count 1-2.5% high (62 frames
for 60.3) made every six-second stretch look late and kept the minimums at the
top. After eight seconds at the top the six-second rate is taken (`roughFor`).

## Special cases

### Content with almost no work: streams and menus (`lightShare`, `driftShare`)

A game stream's frames are made on another machine: the decoder thread is
2-7% busy and the GPU asleep, while the rate wanders with the host and the
network. The law used to take every dip for starvation and sat at the top
clocks for a watt (4.0 W against the vendor's 3.0 W) and no extra frame.

Content whose busiest thread would be busy under 15% of the time at 1152 MHz,
and whose GPU would be at its lowest minimum, for three seconds, is now
"light": it gets 1152 MHz and the GPU's lowest minimum, and its late frames
are held against nothing. Content that has also been late again and again at
ample clocks is judged against 30% instead. Real work (the stock governor
climbing above 1152 MHz, or the shares rising) ends it within half a second,
and content that has left is not called light again for a minute, twice as
long each time, so a game just under the line does not flip in and out.

### A game the GPU limits (`boundBusy`)

A game drawing as fast as the GPU allows, below the panel's rate, changes rate
with every scene. The law took each rise as a new target and each dip as
lateness at ample clocks and never came down: SuperTuxKart with every effect on
drew 19.1 W against the vendor's 16.4 W for the same frames.

Content that is late again and again with both minimums at the top while the
GPU is 97% busy or more at its top clock is now "GPU-bound": the GPU keeps its
top minimum, nothing is judged against a target, and the CPU minimum comes
down one step a second to 1152 MHz for as long as the GPU stays that busy.
GPU time to spare for a second (under 90% busy) sends it back to learning,
and the CPU minimum just under which that happened is kept for a minute,
twice as long each time. A game that holds its own rate with a full GPU is
not affected: it has to be late at the top first.

### GPU clock held on suspicion (`thriftySlack`, `-thrifty`)

Games that drop frames by themselves (God of War II at the same moments of a
scene on every run, in Diablo too) make the law hold the GPU minimum high on
suspicion: 1100 MHz at 31% busy, about a watt. For content at the panel's
rate whose CPU minimum stays at its top for ten seconds, the GPU minimum is the
lowest that keeps the GPU under 70% busy at the heaviest recent moment. If that
leaves the game late for a second, the full GPU minimum comes back for five
minutes, twice as long each time.

It does not apply to content below the panel's rate: there each frame still
has to land on its refresh, and applied to a 25 fps game it cut clean seconds
from 98% to 30%. Nor where the CPU minimum moves: there the law trades one
clock for the other, and a GPU minimum held down makes it pay in CPU clock.

Until 2026-11-17 this applied only within three degrees of the heat target;
the module now runs it from the start (`-thrifty`).

## Heat (`heat.go`, `-heat 48`)

The vendor cuts the CPU maximums in one large step (3532/4320 to 2400/2842 MHz
in Rise) when the board sensor reaches 49 C, and keeps the cut on for minutes
once the case is warm. The heat control projects the board temperature 90
seconds ahead from the last minute's slope. When that passes 48 C it lowers
the GPU's maximum from 1100 to 967 MHz, then the CPU maximums one table entry
every 30 seconds, no lower than the vendor's own first cut. CPU entries come
back two minutes after the board is clear; the GPU's level only once the board
is well clear (under 44 C), so a game it holds under the target runs at one
steady rate instead of switching every few minutes. It acts only in Rise: in
Balance there is one table entry between the mode's ceiling and the vendor's
cut at 41 C.

The GPU goes first because its top level is the expensive one: the device
tree puts 1100 MHz four voltage steps above 967 (`qcom,level` 448 against
320), so the last 12% of clock costs a large share of the GPU's power. A GPU
that has not run above 967 MHz in the last minute has nothing to give, and
its step is taken together with the first CPU step. The GPU's maximum is
written to `devfreq/max_freq`, a request of its own beside the vendor's
`max_pwrlevel` and `thermal_pwrlevel`; the lowest wins. *Not yet measured on
the tablet* (branch `gpu-first-heat`).

In God of War II the CPU steps kept the vendor's cut away for 28 minutes,
three runs out of three, at 8.8 W against 10.2 W and the same late frames. In
a game whose heat is the GPU's, the CPU steps alone lower maximums that cost
nothing (measured: 0.3-0.6 fps above its neighbours, four blocks of four);
that is what the GPU step is for.

## Outside games (`-base 1152`)

While the screen is on and no game scene is active, the six performance cores'
minimum is 1152 MHz instead of 384. Video playback loads its busiest thread 8%,
the cores idle near 570 MHz, and about once a second a frame is not ready: a
60 fps clip played at 58.7-58.9 fps, and at 59.8 with the minimum. No power
cost could be measured. The daemon only takes back a minimum it raised itself.

## Fan (`-fan`, `-fanhot`, both unused)

`-fan N` sets a slowest fan level for games; `-fanhot` runs the fan at its top
level while clocks are being cut for heat or the board is within four degrees
of the vendor's first cut. Both only ever make the fan faster than the vendor
has it, only while it is running, and put the vendor's level back. Three
tests found no effect on temperature, power or frame rate, so the module uses
neither.

## Tuning constants

| Constant | Value | Meaning |
| --- | --- | --- |
| `learnTicks` | 4 | two seconds at ample clocks before trusting the rate |
| `lateNow` / `lateSustained` | 2.5% / 1.1% | one-second / six-second shortfall that counts as late |
| `slowDownTicks` / `fastDownTicks` | 20 / 2 | on-time ticks before a careful / a quick step |
| `slack` | 0.80 | busy share a step must leave at the heaviest recent moment |
| `overlap` | 1.10 | busy shares adding up past this prove side-by-side work |
| `turnsFor` | 10 min (to 80) | no trials after a failed one |
| `firstMemory` / `maxMemory` | 1 / 16 min | how far back "heaviest recent moment" reaches |
| `nearPanel` | 0.97 | content this close to the panel's rate is not probed |
| `restFor` | 5 s | at ample clocks after a rescue |
| `thriftySlack` | 0.70 | GPU busy share the thrifty minimum must leave |
| `lightShare` / `driftShare` | 0.15 / 0.30 | busy share at 1152 MHz under which content is light |
| `boundBusy` / `boundLeave` | 0.97 / 0.90 | GPU busy share that makes / ends a GPU-bound game |
| `restMHz` | 1152 | the resting CPU clock |
| `roughFor` | 8 s | at the top before a high one-second target is replaced |

## Known limits

- **After a launch** the first two to three minutes cost 1.5-2 W more than the
  settled state: loading makes frames late, which ends the quick descent, and
  the rest comes down one step every ten seconds. Three faster descents were
  tried; none helped (see [history.md](history.md#tried-and-not-adopted)).
- **A game that is late at ample clocks again and again, with real but modest
  work** (Soulcalibur's attract loop in some launches) keeps the minimums at
  the top: its target never holds still, so neither the light rule nor the
  thrifty rule reaches it. It still gets far more frames on time than with the
  vendor alone, at 1.5-3.6 W more. A mode for it is sketched in
  [history.md](history.md#open-problems).
