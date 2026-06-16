---
title: "idle-controller-call-rate — Phase 0: Baseline measurement"
description: Reproducible procedure to measure the idle controller bd-call rate on a native-store build, gating the optimisation pillars.
status: In progress
design: ../design/idle-controller-call-rate.md
issues: [3543, 2463]
---

# Phase 0 — Baseline measurement

Phase 0 of [`idle-controller-call-rate`](../design/idle-controller-call-rate.md).
It produces the numbers that **gate** every optimisation pillar. No optimisation
code is written until this baseline exists, because the headline #2463 figures
(~7 `bd`/sec, ~463 Dolt q/sec) are stale: they predate #3097 (suspended-rig
reconcile skip + pack-hash memoization), #3270 (native store with hooks), and
the order-dispatcher's suspended-skip. We must measure the **residual** on a
current native-store build, not re-quote pre-fix numbers.

## What's already done

- **Instrumentation exists** — `internal/beads/bdtrace.go` (#2485) writes one
  JSONL record per `bd` subprocess to `GC_BD_TRACE_JSON`, scope-classified
  (`order-dispatch`, `tick-body`, `bead-event-watcher`, `hook:*`, `cli-command`,
  `unknown`) and tick-reason attributed (`patrol`/`poke`/…). No new tracer needed.
- **Aggregator** — [`scripts/bd-call-rate/aggregate.py`](../../scripts/bd-call-rate/aggregate.py)
  turns that JSONL into by-subcommand (the #2463 table), by-scope, by-tick-trigger,
  and a scope×subcommand cross-tab. `python3 scripts/bd-call-rate/aggregate.py --self-test`.
- **Baseline binary** — builds clean from `origin/main` (post-#3097/#3270):
  `go build -o /tmp/gc-baseline ./cmd/gc`.

## Procedure

> ⚠️ **Measure the *idle controller*, not paid agents.** The target is the
> controller's steady-state read fan-out, so the city must tick **without
> spawning provider agents** (no Claude/API cost, and agent work would pollute
> the idle signal). Use a disposable city and keep agents suspended.

```bash
GC=/tmp/gc-baseline
CITY=$(mktemp -d /tmp/bd-rate-city.XXXX)
TRACE=$(mktemp /tmp/bd-rate.XXXX.jsonl)

# 1. Disposable city. git-init the scope so the native store is eligible
#    (avoids the #3248 bd_context_agreement gate → real native-store baseline).
"$GC" init --template gastown --default-provider claude "$CITY"
git -C "$CITY" init -q
"$GC" doctor --city "$CITY" 2>&1 | grep -i native_store || echo "native store eligible"

# 2. Keep the controller running but spawn no agents.
"$GC" suspend "$CITY"          # city suspended ⇒ reconciler skips agent spawn…
#    …but we still want order-dispatch/tick reads. If suspend also halts the
#    tick loop, instead leave the city active with every agent suspended
#    (gc agent suspend <name> for each) so the controller ticks with zero agents.

# 3. Idle run with tracing. 10 min mirrors the #2463 methodology; 3 min is a
#    quick first read.
GC_BD_TRACE_JSON="$TRACE" "$GC" start "$CITY"
sleep 600
"$GC" stop "$CITY"

# 4. Aggregate.
python3 scripts/bd-call-rate/aggregate.py "$TRACE"
```

Repeat for three shapes to characterise the rate's drivers:

| Shape | Why |
|---|---|
| **single-rig, idle** | the floor: order-dispatch + tick-body + event-watcher reads |
| **multi-rig (e.g. 8), all active** | does the rate scale with rig count? |
| **multi-rig, mostly suspended** | confirms #3097 + dispatch suspended-skip (#3543's 15-of-16 case ⇒ should ≈ single-rig) |

## Results (to fill in)

| Shape | window | total bd | bd/sec | top scope (/sec) | top subcmd (/sec) |
|---|---|---|---|---|---|
| single-rig idle | | | | | |
| 8-rig active | | | | | |
| 8-rig, 7 suspended | | | | | |

## What the numbers decide

- **Overall idle bd/sec on the native-store baseline** — if already near-zero,
  the design narrows to Pillar 1 (tick backoff) only, or closes.
- **`order-dispatch` vs `tick-body` vs `bead-event-watcher` split** — picks
  whether Pillar 2 (per-pass snapshot coalescing) is worth it, and which path.
- **Suspended-rig shape ≈ single-rig?** — confirms Pillar 3 is already covered
  by #3097/dispatch-skip; any excess isolates the residual (e.g. `gc rig list
  --json` runtime probes, phantom event cursors).
- **`patrol` vs `poke` trigger split** — sizes the Pillar 1 (demand-gated tick)
  win: a high `patrol` share with low `poke` means most reads are the fixed
  cadence sweeping with nothing to do.

## Exit criteria

A filled-in results table + a one-paragraph readout that (a) states the residual
idle rate post-#3097, (b) confirms or revises the Pillar-3 "already shipped"
claim, and (c) gives a go/no-go for Pillars 1 and 2 with the scope split as
evidence.
