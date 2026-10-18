package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

var ledgerNow = time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)

// A recorded hold only ever widens; a reset that has passed is pruned and
// gates nothing; a reset beyond the 8-day span is a misparse and gates nothing.
func TestProviderLimitLedger_RecordWidensAndBounds(t *testing.T) {
	city := t.TempDir()
	fs := fsys.OSFS{}
	weekly := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

	if err := recordProviderLimitHold(fs, city, "claude", weekly, ledgerNow, "s1", ledgerNow); err != nil {
		t.Fatal(err)
	}
	// An earlier (5h-window) reset does not narrow the weekly one.
	if err := recordProviderLimitHold(fs, city, "claude", ledgerNow.Add(2*time.Hour), ledgerNow, "s2", ledgerNow); err != nil {
		t.Fatal(err)
	}
	holds := durableProviderLimitHolds(fs, city, ledgerNow)
	if !holds["claude"].Equal(weekly) {
		t.Fatalf("claude hold = %v, want the weekly reset %v", holds["claude"], weekly)
	}
	if got := durableProviderLimitHolds(fs, city, weekly.Add(time.Second)); len(got) != 0 {
		t.Errorf("holds after the reset = %v, want none", got)
	}
	if err := recordProviderLimitHold(fs, city, "codex", ledgerNow.Add(9*24*time.Hour), ledgerNow, "s3", ledgerNow); err != nil {
		t.Fatal(err)
	}
	if _, ok := durableProviderLimitHolds(fs, city, ledgerNow)["codex"]; ok {
		t.Error("a reset 9 days out gated starts; it must be treated as a misparse")
	}
}

func TestProviderLimitLedger_ClearOneOrAll(t *testing.T) {
	city := t.TempDir()
	fs := fsys.OSFS{}
	until := ledgerNow.Add(24 * time.Hour)
	for _, p := range []string{"claude", "codex"} {
		if err := recordProviderLimitHold(fs, city, p, until, ledgerNow, "s", ledgerNow); err != nil {
			t.Fatal(err)
		}
	}
	if dropped, err := clearProviderLimitHold(fs, city, "claude"); err != nil || len(dropped) != 1 || dropped[0] != "claude" {
		t.Fatalf("clear claude = %v, %v", dropped, err)
	}
	if holds := durableProviderLimitHolds(fs, city, ledgerNow); len(holds) != 1 || holds["codex"].IsZero() {
		t.Fatalf("holds after clearing claude = %v, want codex only", holds)
	}
	if dropped, err := clearProviderLimitHold(fs, city, ""); err != nil || len(dropped) != 1 {
		t.Fatalf("clear all = %v, %v", dropped, err)
	}
	if _, err := os.Stat(providerLimitLedgerFile(city)); !os.IsNotExist(err) {
		t.Errorf("empty ledger file left behind: %v", err)
	}
}

// A corrupt ledger gates nothing and is replaced on the next record.
func TestProviderLimitLedger_CorruptFileFailsOpen(t *testing.T) {
	city := t.TempDir()
	p := providerLimitLedgerFile(city)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if holds := durableProviderLimitHolds(fsys.OSFS{}, city, ledgerNow); len(holds) != 0 {
		t.Fatalf("corrupt ledger gated %v", holds)
	}
	if err := recordProviderLimitHold(fsys.OSFS{}, city, "claude", ledgerNow.Add(time.Hour), ledgerNow, "s", ledgerNow); err != nil {
		t.Fatal(err)
	}
	if holds := durableProviderLimitHolds(fsys.OSFS{}, city, ledgerNow); holds["claude"].IsZero() {
		t.Fatal("record did not replace the corrupt ledger")
	}
}

func TestProviderLimitLedger_NoCityPathWritesNothing(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	l := cityProviderLimitLedger{fs: fsys.OSFS{}, cityPath: "", now: func() time.Time { return ledgerNow }, stderr: &bytes.Buffer{}}
	l.held("claude", ledgerNow.Add(time.Hour), ledgerNow, "s")
	if _, err := os.Stat(filepath.Join(wd, ".gc")); !os.IsNotExist(err) {
		t.Fatalf("an empty city path wrote under the working directory: %v", err)
	}
}

func TestMergeProviderLimitHolds_KeepsLaterReset(t *testing.T) {
	a, b := ledgerNow.Add(time.Hour), ledgerNow.Add(2*time.Hour)
	got := mergeProviderLimitHolds(map[string]time.Time{"claude": b}, map[string]time.Time{"claude": a, "codex": a})
	if !got["claude"].Equal(b) || !got["codex"].Equal(a) {
		t.Fatalf("merged = %v", got)
	}
}

// fakeLedger records what the observer told it.
type fakeLedger struct{ heldCalls, liftedCalls []string }

func (f *fakeLedger) held(provider string, until, _ time.Time, _ string) {
	f.heldCalls = append(f.heldCalls, provider+"@"+until.UTC().Format(time.RFC3339))
}
func (f *fakeLedger) lifted(provider string) { f.liftedCalls = append(f.liftedCalls, provider) }

// The observer feeds the ledger on the two transitions that say something
// about the provider — a hold with a known reset, and a live resume — and
// not when a parked session merely stops.
func TestObserveProviderUsageLimit_FeedsLedger(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 10, 18, 9, 5, 0, 0, time.UTC)}
	store := newTestStore()
	led := &fakeLedger{}

	session := makeBead("b1", map[string]string{"provider": "claude"})
	info := observeProviderUsageLimit(seedSessionInfo(session), true, staticPeek(readRuntimeUsageLimitFixture(t, "usage_limit_hold_claude_opus.txt"), nil),
		newDrainTracker(), sessionFrontDoor(store), nil, led, clk, &bytes.Buffer{})
	if len(led.heldCalls) != 1 || !strings.HasPrefix(led.heldCalls[0], "claude@") || led.heldCalls[0] != "claude@"+info.ProviderLimitUntil {
		t.Fatalf("held = %v, want one claude hold at %q", led.heldCalls, info.ProviderLimitUntil)
	}

	gone := makeBead("b2", map[string]string{"provider": "claude", sessionpkg.ProviderLimitSeenAtKey: "2026-10-18T09:02:00Z", sessionpkg.ProviderLimitUntilKey: "2026-10-24T03:00:00Z"})
	observeProviderUsageLimit(seedSessionInfo(gone), false, staticPeek("", nil), newDrainTracker(), sessionFrontDoor(store), nil, led, clk, &bytes.Buffer{})
	if len(led.liftedCalls) != 0 {
		t.Fatalf("a parked session stopping lifted the provider hold: %v", led.liftedCalls)
	}

	resumed := makeBead("b3", map[string]string{"provider": "claude", sessionpkg.ProviderLimitSeenAtKey: "2026-10-18T09:02:00Z", sessionpkg.ProviderLimitUntilKey: "2026-10-24T03:00:00Z"})
	observeProviderUsageLimit(seedSessionInfo(resumed), true, staticPeek(readRuntimeUsageLimitFixture(t, "usage_limit_cleared_claude_scrollback.txt"), nil),
		newDrainTracker(), sessionFrontDoor(store), nil, led, clk, &bytes.Buffer{})
	if len(led.liftedCalls) != 1 || led.liftedCalls[0] != "claude" {
		t.Fatalf("lifted = %v, want claude once after a live resume", led.liftedCalls)
	}
}

// The case sr-896w and sr-pkh5 describe: every parked session is gone, but the
// city's recorded hold still keeps a dead seat from being started into the
// limit, until the reset.
func TestReconcileSessionBeads_DurableHoldDefersRespawnWithNoParkedSession(t *testing.T) {
	until := time.Date(2026, 3, 8, 13, 0, 0, 0, time.UTC)
	env, sessions, work := providerLimitGateEnv(t, until)
	// Drop the live parked session: only the dead worker remains.
	env.sp.PeekOutput = nil
	if err := env.store.Close(sessions[1].ID); err != nil {
		t.Fatalf("Close(parked): %v", err)
	}
	sessions = sessions[:1]
	if err := recordProviderLimitHold(fsys.OSFS{}, env.city, "claude", until, env.clk.Now(), "parked", env.clk.Now()); err != nil {
		t.Fatal(err)
	}

	if woken := providerLimitGateTick(t, env, sessions, work); woken != 0 {
		t.Fatalf("woken = %d, want 0 while the city's recorded hold runs to 13:00; stderr=%s", woken, env.stderr.String())
	}
	env.clk.Time = until.Add(time.Second)
	if woken := providerLimitGateTick(t, env, sessions, work); woken != 1 {
		t.Fatalf("woken = %d after the recorded reset, want 1; stderr=%s", woken, env.stderr.String())
	}
}

// gc resume drops every recorded hold: the operator has confirmed the limit,
// and a suspended city has no live session that could see it lift.
func TestResumeClearsProviderLimitLedger(t *testing.T) {
	city := t.TempDir()
	if err := os.WriteFile(filepath.Join(city, "city.toml"), []byte("[workspace]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordProviderLimitHold(fsys.OSFS{}, city, "claude", time.Now().Add(48*time.Hour), time.Now(), "s", time.Now()); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := doSuspendCity(fsys.OSFS{}, city, false, false, &stdout, &stderr); code != 0 {
		t.Fatalf("resume code = %d; stderr=%s", code, stderr.String())
	}
	if holds := durableProviderLimitHolds(fsys.OSFS{}, city, time.Now()); len(holds) != 0 {
		t.Fatalf("holds after gc resume = %v, want none", holds)
	}
	if !strings.Contains(stderr.String(), "cleared recorded provider usage-limit hold(s): claude") {
		t.Errorf("resume did not say it cleared the hold: %q", stderr.String())
	}
}

// With no live seat parked the doctor check must still report a recorded hold
// that gates starts — otherwise it reads OK while gc refuses every start.
func TestProviderUsageLimitCheck_ReportsRecordedHoldWithNoLiveSeat(t *testing.T) {
	city := t.TempDir()
	until := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)
	if err := recordProviderLimitHold(fsys.OSFS{}, city, "claude", until, now, "s", now); err != nil {
		t.Fatal(err)
	}
	mem := beads.NewMemStore()
	c := newProviderUsageLimitCheck(&config.City{}, city, func(string) (beads.Store, error) { return mem, nil })
	c.now = func() time.Time { return now }
	r := c.Run(&doctor.CheckContext{})
	if r.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning for a recorded hold: %#v", r.Status, r)
	}
	if len(r.Details) != 1 || !strings.Contains(r.Details[0], "claude") || !strings.Contains(r.Details[0], "2026-10-24T03:00:00Z") {
		t.Errorf("details = %v, want the claude hold", r.Details)
	}
	if !strings.Contains(r.FixHint, "gc resume") {
		t.Errorf("hint = %q, want the gc resume escape", r.FixHint)
	}
}
