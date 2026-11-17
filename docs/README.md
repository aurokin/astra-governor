# Documentation

For using the module, the [README](../README.md) is all you need. These pages
are for understanding it and changing it, roughly in the order to read them.

| Read | When you want to know |
| --- | --- |
| [How it works](how-it-works.md) | **start here**: the vendor's system and where astra-governor fits, in a minute and then in detail; what it does, where it sits among the vendor's software, what it writes and how it hands everything back; the code map |
| [The vendor's system](vendor-system.md) | how RedMagicOS decides clocks: Game Space modes, the vendor's frame governor (IFAS), the heat ladder, the fan, refresh-rate settings, and quirks that affect measurements |
| [The control law](control-law.md) | every rule in `control.go` and `heat.go`, the measurement that motivated it, the tuning constants, known limits |
| [The display](display.md) | the panel's real refresh rates, what they mean for games, video and streams, and what was tried to change them |
| [Results](results.md) | everything measured: emulators, high refresh rates, native games, streaming, apps outside games, heat and the fan |
| [Development](development.md) | building, installing, the on-device hand-over check, comparing builds, how a run is measured and what confounds it, releasing |
| [History](history.md) | how the design got here, faults fixed, what was tried and dropped, open problems |
| [Measured runs](../data/README.md) | the per-run data behind every number, and how to print tables from it |

## Outside this repository

On the development Mac:

- `~/workspace/astra-root/lab`: the test bench (recorder, scripted game
  scenes, synthetic load app, sweeps, raw recordings).
- `~/workspace/astra-root/governor`: the first governor and the display
  investigation's full record and tools.
- `~/workspace/astra-root/README.md`, `root-magisk/`, `backups/`: how the
  tablet was unlocked and rooted, and its stock partition backup.
