# Development

Building, installing, checking and measuring a change.

## Build and test

```sh
go test ./...                                   # the control law, heat control and fan decision against models
go vet ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/astrad2 .
```

The tests are the control law's specification: each encodes a behaviour that
was measured on the tablet (a starved game found and economised, a stream not
held at ample clocks, bursts met by the floor...) and names it. A change to
`control.go` that fails one needs a reason, not a new expected value. Most were
checked by breaking the rule they cover and seeing them fail.

## Install on the tablet

```sh
sh module/install.sh            # build, copy into the installed module, restart; no reboot
sh module/install.sh --first    # back to the first governor (its files are outside this repository)
sh module/build-zip.sh          # build/astra-governor-<version>.zip, for a fresh install from the Magisk app
SERIAL=... sh module/install.sh # another device
```

`install.sh` needs the module to be installed already; a fresh device takes
the zip. On the tablet:

```sh
adb shell su -c 'curl -s 127.0.0.1:7878/status'    # live state: action, target, floors, temperatures
adb shell su -c 'curl -s 127.0.0.1:7878/disable'   # off until /enable or a restart
adb shell su -c 'tail -f /data/adb/astra/astrad2.log'
```

The log names every action with the sample that caused it, for example
`down-gpu607: 60.3 fps (target 60.3) cpu floor 1785 MHz gpu floor 607 MHz,
busy 0.36+0.29 at 1996/525 MHz`.

## Checking a build on the tablet

The test bench lives outside this repository, in
`~/workspace/astra-root/lab` on the development Mac (git-tracked, local only;
about 8 GB with its staged games and raw recordings). It finds this
repository at `~/code/astra-governor`, or wherever `$ASTRA_GOVERNOR` points.

After any change to the daemon, before installing it:

```sh
python3 recorder/handover.py
```

runs the new build from a temporary path and checks, with a game in front:
minimums held in Rise; a heat ceiling in place; Diablo's pins surviving a
switch to Diablo; minimums back after returning to Rise; a new instance
restoring floors and ceilings left by a killed one; the non-game floor going
with the screen and coming back. It restores everything afterwards.

To compare a change against the installed build on real games:

```sh
ARMS=v2rise+heat=48,v2rise+heat=48+bin=astrad2-new CAMPAIGN=name sh recorder/sweeps/builds.sh [scene ...]
```

builds.sh runs each arm from a fresh launch of each scene (14 by default),
150 s from 15 s after the start, from a skin temperature of 33 C or less.
`+bin=NAME` runs `build/NAME` instead of `build/astrad2`; other `+flag=value`
parts become daemon flags.

## How a run is measured

`recorder/astrarec` (Go, on the tablet, as root) only observes:

- every presented frame of the app's layer, from SurfaceFlinger's per-layer
  history, polled four times a second;
- clocks, limits and GPU levels ten times a second;
- temperatures, power (battery and USB current and voltage), fan, busiest
  threads and per-core load once a second.

`recorder/analyze.py` turns a recording into the figures in
[results.md](results.md):

| Figure | Meaning |
| --- | --- |
| fps, % of target | throughput against the content's own rate (or the panel's) |
| clean | share of seconds with no late frame beyond what the panel forces |
| long / late frames | extra one-refresh delays per second |
| hitch | delays of two refreshes or more, per minute |
| worst | the longest gap between frames |
| W | the whole tablet: USB input less the battery's charge, or the battery's draw |

A 59.94 fps game on a 60.27 Hz panel must repeat a frame every three seconds;
`analyze.py` works out what the panel forces and does not count it.

### What confounds a comparison

- **Heat.** The vendor's cuts change everything. Compare runs that start
  equally warm, judged by the battery temperature: skin and board fall five to
  ten degrees within three minutes of a load stopping and say little about how
  warm the tablet is inside. Cool down with the game closed.
- **Time in the scene.** God of War II drops frames at the same moments on
  every run. Restart such scenes before every arm (`--fresh`).
- **Launch.** The first two or three minutes after a launch cost more than the
  settled state.
- **25 and 30 fps content** shows short-long frame pairs as its clock drifts
  past the panel's, in every arm.
- **Charging.** A run on the cable that also charges the battery is warmer.
  The harness switches on the vendor's charge separation before each arm.
- **One run each.** The spread between two runs of the same thing is up to
  half a watt and several points of clean seconds; Soulcalibur's attract loop
  varies by much more between launches.
- **The vendor's surprises.** The system refresh setting has switched itself to
  60 Hz twice, and the vendor's charging page has come to the front during a
  game; the harness puts the setting back and stops scripted touches when the
  game loses the screen.

### Limits of the measurement

Present times come from SurfaceFlinger, not a light sensor on the panel.
Polling SurfaceFlinger costs a `dumpsys` fork four times a second (the daemon
does it twice). Power on the cable is the tablet's draw only while the battery
is not charging.

## Releasing

1. `go test ./...` and `go vet ./...`.
2. `recorder/handover.py` on the tablet.
3. A `builds.sh` comparison against the installed build on the scenes the
   change could affect, and on the default set if it touches the control law.
4. Bump `version` and `versionCode` in `module/module.prop`, commit, then
   `sh module/install.sh`.
5. Record what was measured in [results.md](results.md) and, if a rule
   changed, in [control-law.md](control-law.md).
6. Publish from a clean, pushed tree: `sh module/build-zip.sh`, then
   `gh release create v<version> build/astra-governor-<version>.zip` with notes
   on what changed and what was measured. Both scripts build with `-trimpath`
   from the same commit, so the release's `astrad2` is byte for byte the one
   installed in step 4 (compare `sha256sum /data/adb/modules/astra/astrad2`).
