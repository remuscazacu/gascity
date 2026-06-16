---
title: Idle Controller Call-Rate Reduction
description: Cut the controller's steady-state bd/Dolt call *rate* (Layer 3 of #2463) so an idle or quiescent city does not saturate the host, independent of per-call cost.
status: Proposed
issues: [3543, 2463]
---

# Idle Controller Call-Rate Reduction

> **Status: Proposed.** Layer-3 (Gas City scheduling) companion to the already
> merged/in-flight store-layer work. Tracks #3543 (the idle-saturation data
> point) and the "Gas City Layer" the reporter of #2463 called *"the most
> immediately beneficial option."*

## Summary

An idle Gas City issues a high, roughly fixed rate of `bd`/Dolt reads — ~7 `bd`
subprocesses/sec → ~463 Dolt `Com_select`/sec on a single idle city (#2463),
and enough to push a small **multi-tenant** host to load 8→14 even with a tiny,
retention-bounded store (#3543). The rate comes from the controller running a
**full per-pass sweep** — every session, every order, every rig — on a fixed
tick, regardless of whether anything changed.

The store layer is being fixed in parallel: the native in-process store + the
`CachingStore` (re-enabled by #3505/#3270 for #3248), `beads#4107`'s read
optimisation, and the DoltLite backend all reduce **per-call cost**. This
design is deliberately orthogonal: it reduces the controller's **logical call
rate**. Both are required. As @coffeegoddd noted on #2463, cheaper queries
*raise throughput and refill the CPU* unless the call rate also drops; as
@mmlac demonstrated empirically (pruned 543k rows, "CPU did not move"), the
saturation is **rate-bound, not size-bound**.

The proposal is four pillars, ordered by confidence:

1. **Demand-gated ticking** *(lead, high-confidence, store-layer-independent)* —
   back off the controller tick when a pass observes no demand delta; wake
   immediately on a real signal.
2. **Single-snapshot per-pass evaluation** *(contingent on measurement)* —
   collapse residual per-order / per-session read fan-out within a pass into one
   snapshot read, reusing #3492's `ListQuery.LabelPrefix` primitive.
3. **Quiescent-scope skipping + cursor sanity** — stop sweeping suspended rigs
   and fix the phantom event-cursor backlog they report.
4. **Snapshot-served hot hooks** *(secondary)* — serve `gc hook` / `gc mail
   check` / `gc rig list --json` from a cached controller snapshot instead of a
   fresh per-call fan-out.

A cross-cutting **bd-call-rate budget + meter** makes the win measurable and
guards against regression — directly implementing @coffeegoddd's "GC/GT should
rate limit usage of bd commands."

## Decision Log / Status

- **Proposed** (this revision). No code landed. Phase 0 (instrumentation +
  post-#3505 baseline) **gates** the rest: pillar ordering and the Phase-3 go/no-go
  depend on measured residual rate, not on the pre-#3505 CLI-fallback numbers.

## Problem Statement

### Observed behaviour

`#2463` (idle, single city, default Gas Town pack), 10-minute aggregate:

| subcmd | calls | /sec | % |
|---|--:|--:|--:|
| list | 1,597 | 2.83 | 38.7% |
| query | 1,118 | 1.98 | 27.1% |
| show | 645 | 1.14 | 15.6% |
| update | 358 | 0.63 | 8.7% |
| dep | 191 | 0.34 | 4.6% |
| create / close | 74 / 74 | 0.13 / 0.13 | 1.8% / 1.8% |
| gate | 34 | 0.06 | 0.8% |

Reads (`list`+`query`+`show`) are **81%** of calls. Corroborating data points:

- **#3543 (our case):** a small **multi-tenant** host (4 cores, two cities under
  one systemd supervisor) reached load 8→14 with a *retention-bounded* store
  (~1,083 wisps total; 15 of 16 rig DBs held **0** wisps yet were still
  patrolled), ~36–40 `order:*` events/min. Suspending the active agent did **not**
  relieve it — the idle controller sweep continued. Only stopping the city did.
- **@mmlac (#2463):** ~442–462 `Com_select`/sec, Dolt 250–450% CPU; **rate-bound
  not size-bound** (pruned 543k orphaned rows, CPU unchanged).
- **@Cdfghglz (#2463):** 37-rig city; `gc rig list --json` pegs a core on per-rig
  runtime probes; a 60 s-TTL flock cache "noticeably reduced sustained load."
- **@julianknutsen (#2463):** the tmux status line shells out **every 5 s per
  session** to `gc hook` + `gc mail check`.

### Where the rate comes from (code)

- **Fixed tick.** Supervisor patrol ticker (`cmd/gc/cmd_supervisor.go:1344-1370`)
  → per-city `CityRuntime.tick()` (`cmd/gc/city_runtime.go:900-1054`) →
  `dispatchOrders()` (`:1256-1268`). The city patrol default is ~30 s
  (`internal/config/config.go`, `PatrolIntervalDuration`). Every tick runs the
  **full sweep** whether or not anything changed.
- **Per-order read fan-out.** `order_dispatch.go` (~`:460-640`) already loads a
  `trackingIndex` snapshot once, but still issues residual **per-order** reads:
  `bdCursorAcrossStores()` for each event order, and `CheckTriggerWithOptions` →
  `checkEvent` → `events.Provider.List(...)` per event order
  (`internal/orders/triggers.go:238-258`).
- **Per-session reads/writes.** The session reconciler iterates every session
  each pass, reading/writing session metadata (e.g. the restart-handoff and
  idle-timeout paths around `cmd/gc/session_reconciler.go` — flagged by
  @sjarmak on #2463 as the *top* idle-read candidates; line numbers drift across
  `main`).
- **Subprocess multiplier.** Each read is `exec.CommandContext(ctx, "bd", …)`
  (`internal/beads/bdstore.go:381`) when the native store is unavailable — spawn
  + connect + query per call.

### The #3248 multiplier (and why #3543's own diagnosis was incomplete)

`#3543` attributed our saturation to call volume but did **not** identify that
our binary was running with the **native store disabled**. It was: build
`721a42f0d` (2026-06-14 16:41 Z) is *behind* #3505's merge commit (2026-06-14
23:57 Z), so it predates the #3248 fix. The `WARN native_store_unavailable
gate=bd_context_agreement` we observed is the exact #3248 signature — every bead
op fell back to a CLI `bd` subprocess. **CLI-fallback was plausibly the dominant
multiplier for our specific numbers**, and it is already fixed upstream
(#3505/#3270). This document completes #3543's diagnosis and then designs for the
**residual** rate that remains *after* the store layer is healthy — see Goals.

## Goals

- Reduce the controller's steady-state **logical** `bd`/Dolt call rate on an
  idle or quiescent city, measured **on a post-#3505 binary** (native store +
  cache active), not on CLI-fallback numbers.
- Make the rate **scale with demand**, not with `tick_count × orders × rigs`;
  in particular, suspended/empty rigs must contribute ~0 reads.
- Make the call rate **observable** and add a regression guard.
- Preserve order-firing and event-order latency guarantees
  (`order-firing-current` doctor stays green; event-order p95 bounded).

## Non-goals (fenced off — owned elsewhere)

- **Native-store re-enablement / hook gating (#3248).** Done in #3505 + #3270.
  This design *assumes* the native store + `CachingStore` are active and
  optimises the rate on top of them.
- **Per-call query cost / Beads read optimisation.** `beads#4107`, the hot
  6-LEFT-JOIN aggregation, covering indexes, and using `ready_issues` are the
  **Beads/Dolt layers (1 & 2)** of #2463 — not this doc.
- **DoltLite embedded backend** (#2989/#3147/#3233/#3449) — store backend, not
  scheduling.
- **`bd`↔lib version-skew flood** (@mmlac on #2463) — a separate fail-loud /
  auto-pin issue.
- **The bd+Dolt contract** — owned by the Accepted `beads-dolt-contract-redesign`.
- **Wisp history cascade-delete / orphan prune** — retention/GC, already shipped
  for accumulation (#3424) and tracked separately for `wisp_events`/`wisp_labels`.

## Upstream Alignment

- **`beads-dolt-contract-redesign` (Accepted):** this is the Layer-3 consumer of
  that contract; no contract changes proposed.
- **`idle-session-sleep` (Accepted, implemented):** Pillar 1 lifts the same
  demand-driven principle from the *session* level to the *controller-tick*
  level. The session reconciler already sleeps/wakes individual sessions on
  demand; the controller still sweeps them all on a fixed tick.
- **#3492 (draft):** introduces `ListQuery.LabelPrefix` and fixes the orders-
  *feed* N+1. Pillar 2 **reuses that primitive** for the dispatch path, which
  #3492 does not touch. **#3511 (draft):** indexed order-run lookups in doctor —
  complementary.

## Design

### Pillar 1 — Demand-gated ticking *(lead)*

Replace the fixed-cadence full sweep with a **demand-gated** one. Each pass
computes a cheap **demand signal**; if nothing changed, the next city tick backs
off; any real signal wakes it immediately.

- **No-demand predicate (all true ⇒ idle):** event cursor unchanged since last
  pass (one tail read, see Pillar 2); no order due (cooldown/cron not elapsed,
  no matching event); no session-state delta requiring action; no pending
  poke / mail / `gc reload`.
- **Backoff:** exponential from the configured base (~30 s) to a capped ceiling
  (e.g. 5 min), reset to base on any wake signal. A **heartbeat floor** (one
  cheap liveness pass at the ceiling) guarantees forward progress even if a
  wake signal is missed.
- **Wake triggers (immediate):** new event appended (`events.Provider`),
  controller poke (existing `tick("poke")` path, `city_runtime.go`), inbound
  mail, config reload, session lifecycle request.

Why lead: this pillar is **robust to every store-layer win**. Even with a
perfect in-process cache, a fixed tick still re-evaluates orders, polls events,
and runs the cache reconciler at cadence; backing off when idle removes the
*number of passes*, which is the multiplier on all per-pass reads.

### Pillar 2 — Single-snapshot per-pass evaluation *(contingent)*

Within a pass, collapse residual per-order/per-session fan-out into **one**
snapshot read:

- Extend the existing `trackingIndex` to also carry, per scope: latest
  `seq:` cursors for all event orders and the relevant event tail — loaded with
  **one** `ListQuery.LabelPrefix` scan (the #3492 primitive) instead of
  `bdCursorAcrossStores()` per order.
- Evaluate every order's trigger against the in-memory snapshot; evaluate
  session decisions against a single session-metadata snapshot.

**Contingent:** with the `CachingStore` active post-#3505, some of these reads
are already cache hits. Phase 0 must measure the residual per-pass fan-out
*after* #3505 before investing here; if the cache already serves them, Pillar 2
is low-value and is dropped.

### Pillar 3 — Quiescent-scope skipping + cursor sanity

- **Skip suspended rigs.** Do not evaluate orders or run runtime probes for a
  suspended rig. (#3543: 15 of 16 rig DBs were empty/idle yet swept; @mmlac:
  suspended rigs are probed and report phantom backlogs.)
- **Cursor sanity.** Fix the phantom event-cursor backlog where a suspended,
  0-issue rig reports hundreds of thousands of "pending `bead.updated`" events
  (cursor 0 vs a growing sequence). Initialise/clamp cursors so a quiescent
  scope yields an empty event match without a scan.

### Pillar 4 — Snapshot-served hot hooks *(secondary)*

Serve the per-session-per-5 s status-line calls (`gc hook`, `gc mail check`) and
`gc rig list --json` from a controller-maintained snapshot with a short TTL,
rather than a fresh `bd`/runtime fan-out each call (validated informally by
@Cdfghglz's 60 s-TTL flock cache). Bounded staleness is acceptable for a status
line.

### Cross-cutting — bd-call-rate budget + meter

- A process-level **calls/sec meter** (by subcommand + caller), reusing the
  trace approach from PR #2485.
- A **doctor check** (`bd-call-rate`) that fails when idle rate exceeds a
  threshold — the regression guard implementing @coffeegoddd's "rate limit"
  directive.
- *(Optional, later)* a soft budget that defers non-critical reads when over
  budget.

## Implementation Plan

TDD / red-green; each phase independently shippable behind a flag, default-off
until validated, then defaults flipped.

- **Phase 0 — Instrument & baseline (gating).**
  Land the calls/sec meter + a reproducible idle harness. Measure idle rate on a
  **post-#3505** single-rig and multi-rig city. *Output:* the residual-rate
  numbers that set thresholds and the Phase-3 go/no-go. *Nothing else proceeds
  without these.*

- **Phase 1 — Demand-gated ticking (Pillar 1).**
  Tests: a pass with no demand schedules a backed-off next tick; each wake
  trigger (event/poke/mail/reload/session-request) resets to base within one
  pass; the heartbeat floor fires at the ceiling. Flag `controller.idle_backoff`.

- **Phase 2 — Quiescent-scope skipping + cursor sanity (Pillar 3).**
  Tests: suspended rig ⇒ 0 order-eval reads, 0 runtime probes; a 0-issue
  quiescent scope reports 0 pending events (no phantom backlog).

- **Phase 3 — Per-pass snapshot coalescing (Pillar 2).** *Only if Phase 0 shows
  residual per-pass fan-out matters.* Extend `trackingIndex` with cursors/event
  tail via `ListQuery.LabelPrefix`; remove per-order `bdCursorAcrossStores`.
  Test: N event orders ⇒ O(1) snapshot reads per pass (regression test in the
  spirit of #3492's 500-order test).

- **Phase 4 — Snapshot-served hot hooks (Pillar 4).**
  Tests: status-line calls served from snapshot within TTL ⇒ 0 store reads;
  correctness within bounded staleness.

- **Phase 5 — Rate-budget doctor check + telemetry; flip defaults.**
  Ship the `bd-call-rate` doctor check + telemetry; enable Phase 1/2 defaults
  after soak.

## Risks & Mitigations

- **Missed wake ⇒ stalled dispatch (Pillar 1).** Conservative, comprehensive
  wake triggers; a capped backoff ceiling; a heartbeat-floor pass guarantees
  eventual progress. Validate `order-firing-current` stays green under backoff.
- **Event-order latency regression (Pillars 1–3).** Event append is an explicit
  immediate wake trigger; assert an event-order p95 latency bound in tests.
- **Interaction with `idle-session-sleep`.** Pillar 1 gates the *tick*, not
  session wake demand; ensure an on-demand session wake is a controller wake
  trigger so a backed-off controller still services it promptly.
- **Stale hot-hook snapshot (Pillar 4).** Bounded TTL; mail/hook *writes* still
  invalidate; status-line reads only.
- **Optimising the wrong baseline.** Mitigated by Phase 0 gating on post-#3505
  measurements (the central lesson from #3543's incomplete diagnosis).

## Acceptance / Metrics

- Idle single-rig city (post-#3505): steady-state `bd`/sec drops from the Phase-0
  baseline to a target set by that baseline (goal: an idle city trends toward
  near-zero reads between real events, not a fixed floor).
- Multi-rig idle: total rate scales with *active* rigs, ~flat in suspended-rig
  count (our 16-rig case ⇒ ≈ single active-rig cost).
- No regression: `order-firing-current` doctor green; event-order p95 within the
  asserted bound; `bd-call-rate` doctor check green at the new threshold.

## References

- Issues: #3543 (this design's trigger), #2463 (umbrella; three-layer framing),
  #3248 (native-store gating — fixed by **#3505**/#3270), `beads#4107` (read opt).
- PRs: #3492 (`LabelPrefix` primitive + orders-feed N+1; reused by Pillar 2),
  #3511 (indexed order-run lookups), #2485 (`bd` trace instrumentation).
- Design: `idle-session-sleep` (Accepted), `beads-dolt-contract-redesign`
  (Accepted), `architecture/health-patrol.md`.
- Code anchors: `cmd/gc/cmd_supervisor.go:1344-1370`,
  `cmd/gc/city_runtime.go:900-1054,1256-1268`, `cmd/gc/order_dispatch.go:~460-640`,
  `internal/orders/triggers.go:238-258`, `cmd/gc/session_reconciler.go` (idle/
  restart paths), `internal/beads/bdstore.go:381`,
  `internal/beads/caching_store_reconcile.go`, `internal/beads/factory.go:42-150`.
</content>
</invoke>
