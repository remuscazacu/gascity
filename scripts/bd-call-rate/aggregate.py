#!/usr/bin/env python3
"""Aggregate a GC_BD_TRACE_JSON trace into a bd-call-rate report.

Phase 0 of the `idle-controller-call-rate` design (engdocs/design/). The
instrumentation is gc's own JSONL tracer (internal/beads/bdtrace.go, #2485),
enabled by setting GC_BD_TRACE_JSON=<path> before `gc start`. This script turns
that JSONL into:

  1. by-subcommand  — calls, /sec, % (reproduces the #2463 table)
  2. by-scope       — order-dispatch / tick-body / bead-event-watcher / hook:* /
                      cli-command / unknown (the attribution #2463 lacked)
  3. by-tick-trigger — patrol vs poke vs ...
  4. scope x subcommand cross-tab

Rate is computed over the observed window (max ts - min ts). For an idle-rate
baseline, run the city idle (no agent work) for several minutes, then point this
at the trace file.

Usage:
    python3 aggregate.py TRACE.jsonl
    cat TRACE.jsonl | python3 aggregate.py
    python3 aggregate.py --self-test
"""
import json
import sys
from collections import Counter
from datetime import datetime


def _parse_ts(ts):
    # RFC3339Nano, UTC ("...Z"). Trim to microseconds for fromisoformat.
    ts = ts.replace("Z", "+00:00")
    if "." in ts:
        head, frac = ts.split(".", 1)
        off = ""
        for sep in ("+", "-"):
            if sep in frac:
                frac, off = frac.split(sep, 1)
                off = sep + off
                break
        frac = (frac + "000000")[:6]
        ts = f"{head}.{frac}{off}"
    return datetime.fromisoformat(ts)


def aggregate(records):
    """Pure aggregation over a list of trace dicts. Returns a report dict."""
    by_sub, by_scope, by_trigger = Counter(), Counter(), Counter()
    cross = Counter()
    dur_by_sub = Counter()
    times = []
    for r in records:
        args = r.get("args") or []
        sub = args[0] if args else "(none)"
        scope = r.get("scope") or "unknown"
        by_sub[sub] += 1
        by_scope[scope] += 1
        by_trigger[r.get("tick_trigger") or "(none)"] += 1
        cross[(scope, sub)] += 1
        dur_by_sub[sub] += int(r.get("dur_ms") or 0)
        ts = r.get("ts")
        if ts:
            try:
                times.append(_parse_ts(ts))
            except ValueError:
                pass
    total = sum(by_sub.values())
    window_s = 0.0
    if len(times) >= 2:
        window_s = (max(times) - min(times)).total_seconds()
    return {
        "total": total,
        "window_s": window_s,
        "by_sub": by_sub,
        "by_scope": by_scope,
        "by_trigger": by_trigger,
        "cross": cross,
        "dur_by_sub": dur_by_sub,
    }


def _fmt_table(title, counter, total, window_s, extra=None):
    lines = [f"\n### {title}", f"{'key':<28}{'calls':>9}{'/sec':>10}{'%':>8}"]
    for key, n in counter.most_common():
        rate = n / window_s if window_s else 0.0
        pct = 100.0 * n / total if total else 0.0
        suffix = ""
        if extra and key in extra:
            suffix = extra[key]
        lines.append(f"{str(key):<28}{n:>9}{rate:>10.2f}{pct:>7.1f}%{suffix}")
    return "\n".join(lines)


def render(report):
    total, window_s = report["total"], report["window_s"]
    out = [
        "# bd-call-rate report",
        f"total calls: {total}   window: {window_s:.1f}s   "
        f"overall rate: {(total / window_s if window_s else 0.0):.2f} bd/sec",
    ]
    dur = report["dur_by_sub"]
    out.append(_fmt_table(
        "by subcommand", report["by_sub"], total, window_s,
        extra={k: f"   {dur[k]}ms total" for k in dur},
    ))
    out.append(_fmt_table("by scope", report["by_scope"], total, window_s))
    out.append(_fmt_table("by tick_trigger", report["by_trigger"], total, window_s))
    out.append("\n### scope x subcommand (top 20)")
    for (scope, sub), n in report["cross"].most_common(20):
        out.append(f"  {scope:<24} {sub:<10} {n:>8}")
    return "\n".join(out)


def _self_test():
    recs = [
        {"ts": "2026-06-16T10:00:00.000000Z", "args": ["list"], "scope": "order-dispatch", "tick_trigger": "patrol", "dur_ms": 5},
        {"ts": "2026-06-16T10:00:05.000000Z", "args": ["query"], "scope": "tick-body", "tick_trigger": "patrol", "dur_ms": 7},
        {"ts": "2026-06-16T10:00:10.000000Z", "args": ["list"], "scope": "order-dispatch", "tick_trigger": "poke", "dur_ms": 3},
    ]
    rep = aggregate(recs)
    assert rep["total"] == 3, rep["total"]
    assert abs(rep["window_s"] - 10.0) < 1e-6, rep["window_s"]
    assert rep["by_sub"]["list"] == 2 and rep["by_sub"]["query"] == 1, rep["by_sub"]
    assert rep["by_scope"]["order-dispatch"] == 2, rep["by_scope"]
    assert rep["by_trigger"]["patrol"] == 2 and rep["by_trigger"]["poke"] == 1
    assert rep["cross"][("order-dispatch", "list")] == 2
    assert rep["dur_by_sub"]["list"] == 8
    # empty input must not divide by zero
    empty = aggregate([])
    assert empty["total"] == 0 and empty["window_s"] == 0.0
    print("self-test OK")


def main(argv):
    if "--self-test" in argv:
        _self_test()
        return 0
    src = open(argv[1]) if len(argv) > 1 else sys.stdin
    records = []
    with src as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    print(render(aggregate(records)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
