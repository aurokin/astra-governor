#!/usr/bin/env python3
"""Tables from the measured runs in data/campaigns.

usage:
  results.py campaigns                 one line per campaign: runs, scenes, arms, dates
  results.py table CAMPAIGN [ARM ...]   per scene and arm: medians of throughput, clean seconds, late frames, power
  results.py runs CAMPAIGN [SCENE]      every run, one line each

Each line of data/campaigns/<name>.jsonl is one recording's summary as the test
bench wrote it (see docs/testing.md for what the fields mean and how they were
measured). Arms: stock / stockrise = the vendor alone in Balance / Rise; astrad =
the installed governor (the first governor before 2026-11-14 11:37 local time,
astrad2 after); v2 / v2rise = astrad2 run as a test build in Balance / Rise, with
its flags after "+"; rise = the installed governor in Rise; diablo = Diablo mode.
"""
import collections, json, pathlib, statistics, sys

DATA = pathlib.Path(__file__).resolve().parent.parent / "data" / "campaigns"


def load(name):
    return [json.loads(l) for l in open(DATA / f"{name}.jsonl") if l.strip()]


def med(rows, *path):
    vals = []
    for r in rows:
        v = r
        for p in path:
            v = v.get(p) if isinstance(v, dict) else None
        if isinstance(v, (int, float)):
            vals.append(v)
    return statistics.median(vals) if vals else None


def fmt(v, spec):
    return format(v, spec) if v is not None else "-"


def campaigns():
    for f in sorted(DATA.glob("*.jsonl")):
        rows = load(f.stem)
        arms = collections.Counter(r.get("arm") for r in rows)
        days = sorted({r.get("run", "")[:8] for r in rows})
        print(f"{f.stem:14s} {len(rows):4d} runs {len({r.get('workload') for r in rows}):3d} scenes  {days[0]}..{days[-1]}  {', '.join(a for a, _ in arms.most_common())}")


def table(name, arms=()):
    rows = load(name)
    groups = collections.defaultdict(list)
    for r in rows:
        if not arms or r.get("arm") in arms:
            groups[(r.get("workload"), r.get("arm"))].append(r)
    print(f"{'scene':34s} {'arm':26s} {'n':>2s} {'% of target':>11s} {'clean %':>8s} {'late/s':>7s} {'hitch/min':>9s} {'W':>6s}")
    for (w, a), g in sorted(groups.items(), key=lambda kv: (kv[0][0] or "", list(dict.fromkeys(r.get('arm') for r in rows)).index(kv[0][1]))):
        print(f"{str(w)[:34]:34s} {str(a)[:26]:26s} {len(g):2d} {fmt(med(g, 'pacing', 'speed_pct'), '11.1f')} "
              f"{fmt(med(g, 'pacing', 'clean_seconds_pct'), '8.1f')} {fmt(med(g, 'pacing', 'excess_long_holds_per_s'), '7.2f')} "
              f"{fmt(med(g, 'pacing', 'hitches_per_min'), '9.1f')} {fmt(med(g, 'power', 'system_w'), '6.2f')}")


def runs(name, scene=None):
    for r in load(name):
        if scene and r.get("workload") != scene:
            continue
        p, w = r.get("pacing", {}), r.get("power", {})
        print(f"{r.get('run', '')[:15]} {str(r.get('workload'))[:32]:32s} {str(r.get('arm'))[:26]:26s} "
              f"{fmt(p.get('fps'), '7.2f')} fps {fmt(p.get('clean_seconds_pct'), '5.1f')}% clean {fmt(w.get('system_w'), '5.2f')} W")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    cmd, *rest = sys.argv[1:]
    {"campaigns": lambda: campaigns(), "table": lambda: table(rest[0], rest[1:]), "runs": lambda: runs(*rest[:2])}.get(cmd, lambda: sys.exit(__doc__))()
