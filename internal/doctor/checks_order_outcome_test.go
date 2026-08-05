package doctor

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

// outcomeEvent builds one order.completed / order.failed event. seq is what the
// counter orders by, so tests control sequence explicitly.
func outcomeEvent(seq uint64, subject, eventType string, ts time.Time, message string) events.Event {
	return events.Event{Seq: seq, Type: eventType, Ts: ts, Subject: subject, Message: message}
}

func TestConsecutiveOrderFailuresCountsTrailingFailures(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	outcomes := []events.Event{
		outcomeEvent(1, "refresh-family-clones:rig:st", events.OrderCompleted, base, ""),
		outcomeEvent(2, "refresh-family-clones:rig:st", events.OrderFailed, base.Add(6*time.Hour), "exit status 128"),
		outcomeEvent(3, "refresh-family-clones:rig:st", events.OrderFailed, base.Add(12*time.Hour), "exit status 128"),
		outcomeEvent(4, "refresh-family-clones:rig:st", events.OrderFailed, base.Add(18*time.Hour), "exit status 128"),
	}

	streak, lastMessage, sawOutcome := consecutiveOrderFailures(outcomes, "refresh-family-clones:rig:st", nil, 10*time.Minute)

	if streak != 3 {
		t.Fatalf("streak = %d, want 3", streak)
	}
	if lastMessage != "exit status 128" {
		t.Fatalf("lastMessage = %q, want %q", lastMessage, "exit status 128")
	}
	if !sawOutcome {
		t.Fatal("sawOutcome = false, want true")
	}
}

func TestConsecutiveOrderFailuresStopsAtSuccess(t *testing.T) {
	base := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	// The dolt-remotes-patrol shape: many lifetime failures, currently healthy.
	outcomes := []events.Event{
		outcomeEvent(1, "dolt-remotes-patrol", events.OrderFailed, base, "exit status 1"),
		outcomeEvent(2, "dolt-remotes-patrol", events.OrderFailed, base.Add(15*time.Minute), "exit status 1"),
		outcomeEvent(3, "dolt-remotes-patrol", events.OrderFailed, base.Add(30*time.Minute), "exit status 1"),
		outcomeEvent(4, "dolt-remotes-patrol", events.OrderCompleted, base.Add(45*time.Minute), ""),
	}

	streak, _, sawOutcome := consecutiveOrderFailures(outcomes, "dolt-remotes-patrol", nil, 10*time.Minute)

	if streak != 0 {
		t.Fatalf("streak = %d, want 0", streak)
	}
	if !sawOutcome {
		t.Fatal("sawOutcome = false, want true")
	}
}

func TestConsecutiveOrderFailuresIgnoresOtherOrders(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	outcomes := []events.Event{
		outcomeEvent(1, "beads-health", events.OrderFailed, base, "context canceled"),
		outcomeEvent(2, "dolt-health", events.OrderFailed, base.Add(time.Minute), "exit status 1"),
		outcomeEvent(3, "beads-health", events.OrderCompleted, base.Add(2*time.Minute), ""),
		outcomeEvent(4, "dolt-health", events.OrderFailed, base.Add(3*time.Minute), "exit status 1"),
	}

	streak, _, _ := consecutiveOrderFailures(outcomes, "dolt-health", nil, 10*time.Minute)

	if streak != 2 {
		t.Fatalf("streak = %d, want 2 (beads-health events must not interleave)", streak)
	}
}

func TestConsecutiveOrderFailuresReportsNoOutcomes(t *testing.T) {
	streak, lastMessage, sawOutcome := consecutiveOrderFailures(nil, "never-run", nil, 10*time.Minute)

	if streak != 0 || lastMessage != "" {
		t.Fatalf("streak/lastMessage = %d/%q, want 0/\"\"", streak, lastMessage)
	}
	if sawOutcome {
		t.Fatal("sawOutcome = true, want false for an order with no outcome events")
	}
}

func TestConsecutiveOrderFailuresSkipsPostStartBurst(t *testing.T) {
	// gastownhall/gascity#3898: for ~5 min after supervisor start, exec orders
	// fail spuriously. A 30s-cooldown order logs ~10 consecutive such failures.
	start := time.Date(2026, 8, 4, 23, 23, 0, 0, time.UTC)
	outcomes := []events.Event{}
	for i := 0; i < 10; i++ {
		outcomes = append(outcomes, outcomeEvent(uint64(i+1), "dolt-health", events.OrderFailed,
			start.Add(time.Duration(i+1)*30*time.Second), "gc: unknown command \"dolt\""))
	}

	streak, _, sawOutcome := consecutiveOrderFailures(outcomes, "dolt-health", []time.Time{start}, 10*time.Minute)

	if streak != 0 {
		t.Fatalf("streak = %d, want 0 — every failure is inside the grace window", streak)
	}
	if !sawOutcome {
		t.Fatal("sawOutcome = false, want true")
	}
}

func TestConsecutiveOrderFailuresSkipsWithoutResetting(t *testing.T) {
	// A skipped in-window failure must neither count nor break the streak:
	// resetting would let a frequently-restarting city zero a broken order forever.
	start := time.Date(2026, 8, 4, 23, 23, 0, 0, time.UTC)
	outcomes := []events.Event{
		outcomeEvent(1, "dolt-health", events.OrderCompleted, start.Add(-time.Hour), ""),
		outcomeEvent(2, "dolt-health", events.OrderFailed, start.Add(-30*time.Minute), "exit status 1"),
		outcomeEvent(3, "dolt-health", events.OrderFailed, start.Add(2*time.Minute), "context canceled"),
		outcomeEvent(4, "dolt-health", events.OrderFailed, start.Add(30*time.Minute), "exit status 1"),
	}

	streak, _, _ := consecutiveOrderFailures(outcomes, "dolt-health", []time.Time{start}, 10*time.Minute)

	if streak != 2 {
		t.Fatalf("streak = %d, want 2 — the in-window failure is skipped, not counted, and must not reset", streak)
	}
}

func TestConsecutiveOrderFailuresChecksEveryStart(t *testing.T) {
	// Two restarts with no successful run between them. Checking only the LATEST
	// start would count the older burst and manufacture a false positive.
	first := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	second := time.Date(2026, 8, 4, 23, 0, 0, 0, time.UTC)
	outcomes := []events.Event{
		outcomeEvent(1, "gate-sweep", events.OrderFailed, first.Add(time.Minute), "context canceled"),
		outcomeEvent(2, "gate-sweep", events.OrderFailed, first.Add(2*time.Minute), "context canceled"),
		outcomeEvent(3, "gate-sweep", events.OrderFailed, second.Add(time.Minute), "context canceled"),
		outcomeEvent(4, "gate-sweep", events.OrderFailed, second.Add(2*time.Minute), "context canceled"),
	}

	streak, _, _ := consecutiveOrderFailures(outcomes, "gate-sweep", []time.Time{first, second}, 10*time.Minute)

	if streak != 0 {
		t.Fatalf("streak = %d, want 0 — all four failures sit inside one grace window or the other", streak)
	}
}

func TestConsecutiveOrderFailuresUndercountsAcrossGraceWindow(t *testing.T) {
	// The dolt-remotes-patrol shape: a genuine 27-failure run with exactly one
	// failure landing near a controller start. "Skip" means neither count nor
	// break, so the answer is 26 — NOT 1 (which is what breaking would give) and
	// not 27. The undercount is accepted: counting in-window failures would
	// reintroduce the #3898 false positive, and a run long enough to span a
	// restart is already far past a threshold of 3.
	base := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	start := base.Add(13 * time.Hour)
	outcomes := []events.Event{
		outcomeEvent(0, "dolt-remotes-patrol", events.OrderCompleted, base.Add(-time.Hour), ""),
	}
	seq := uint64(1)
	for i := 0; i < 27; i++ {
		ts := base.Add(time.Duration(i) * time.Hour)
		if i == 13 {
			ts = start.Add(2 * time.Minute) // the one inside the grace window
		}
		outcomes = append(outcomes, outcomeEvent(seq, "dolt-remotes-patrol", events.OrderFailed, ts, "exit status 1"))
		seq++
	}

	streak, _, _ := consecutiveOrderFailures(outcomes, "dolt-remotes-patrol", []time.Time{start}, 10*time.Minute)

	if streak != 26 {
		t.Fatalf("streak = %d, want 26 (27 failures, one skipped inside the grace window, run not broken)", streak)
	}
}

func TestConsecutiveOrderFailuresStopsAtSuccessInsideGraceWindow(t *testing.T) {
	// A success is proof the order works and ends the streak, even inside the
	// grace window. Skipping it would let the walk run past onto stale failures
	// from before the success — the exact false positive this check prevents.
	start := time.Date(2026, 8, 4, 23, 23, 0, 0, time.UTC)
	outcomes := []events.Event{
		outcomeEvent(1, "probe-order", events.OrderFailed, start.Add(-100*time.Hour), "exit status 1"),
		outcomeEvent(2, "probe-order", events.OrderFailed, start.Add(-99*time.Hour), "exit status 1"),
		outcomeEvent(3, "probe-order", events.OrderFailed, start.Add(-98*time.Hour), "exit status 1"),
		outcomeEvent(4, "probe-order", events.OrderCompleted, start.Add(2*time.Minute), ""),
	}

	streak, _, sawOutcome := consecutiveOrderFailures(outcomes, "probe-order", []time.Time{start}, 10*time.Minute)

	if streak != 0 {
		t.Fatalf("streak = %d, want 0 — the success inside the grace window must end the walk", streak)
	}
	if !sawOutcome {
		t.Fatal("sawOutcome = false, want true")
	}
}

func TestConsecutiveOrderFailuresPrefersNewestMessageEvenWhenEmpty(t *testing.T) {
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	outcomes := []events.Event{
		outcomeEvent(1, "probe-order", events.OrderFailed, base, "real reason"),
		outcomeEvent(2, "probe-order", events.OrderFailed, base.Add(time.Hour), ""),
	}

	streak, lastMessage, _ := consecutiveOrderFailures(outcomes, "probe-order", nil, 10*time.Minute)

	if streak != 2 {
		t.Fatalf("streak = %d, want 2", streak)
	}
	if lastMessage != "" {
		t.Fatalf("lastMessage = %q, want \"\" — the newest failure's message wins even when empty", lastMessage)
	}
}

func TestNearControllerStartBoundaries(t *testing.T) {
	start := time.Date(2026, 8, 4, 23, 0, 0, 0, time.UTC)
	grace := 10 * time.Minute

	cases := []struct {
		name string
		ts   time.Time
		want bool
	}{
		{"before start", start.Add(-time.Second), false},
		{"at start", start, true},
		{"inside window", start.Add(5 * time.Minute), true},
		{"at boundary", start.Add(grace), true},
		{"past boundary", start.Add(grace + time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nearControllerStart(tc.ts, []time.Time{start}, grace); got != tc.want {
				t.Fatalf("nearControllerStart(%v) = %v, want %v", tc.ts, got, tc.want)
			}
		})
	}
}

func TestNearControllerStartWithNoStarts(t *testing.T) {
	ts := time.Date(2026, 8, 4, 23, 0, 0, 0, time.UTC)
	if nearControllerStart(ts, nil, 10*time.Minute) {
		t.Fatal("nearControllerStart = true with no starts, want false")
	}
}
