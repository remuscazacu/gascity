package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// readRuntimeUsageLimitFixture loads a real srvcity pane capture (sr-p8se).
func readRuntimeUsageLimitFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "runtime", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func staticPeek(content string, calls *int) func(int) (string, error) {
	return func(int) (string, error) {
		if calls != nil {
			*calls++
		}
		return content, nil
	}
}

// A live session parked on Claude's "continuing automatically at 6am" hold is
// recorded on its bead — and nothing else changes: it stays awake, keeps its
// wake stamp, and is not quarantined (sr-p8se).
func TestObserveProviderUsageLimit_RecordsHoldOnLiveSession(t *testing.T) {
	bucharest, err := time.LoadLocation("Europe/Bucharest")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Date(2026, 10, 16, 18, 50, 0, 0, bucharest)
	clk := &clock.Fake{Time: now}
	store := newTestStore()
	session := makeBead("b1", map[string]string{
		"state":        "awake",
		"last_woke_at": now.Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	var log bytes.Buffer

	got := observeProviderUsageLimit(seedSessionInfo(session), true, staticPeek(readRuntimeUsageLimitFixture(t, "usage_limit_hold_claude_opus.txt"), nil),
		newDrainTracker(), sessionFrontDoor(store), nil, clk, &log)
	syncBeadFromStore(&session, store)

	if !got.ProviderLimited() {
		t.Fatal("returned Info is not ProviderLimited")
	}
	if v := session.Metadata[sessionpkg.ProviderLimitSeenAtKey]; v != "2026-10-16T15:50:00Z" {
		t.Errorf("provider_limit_seen_at = %q", v)
	}
	if v := session.Metadata[sessionpkg.ProviderLimitUntilKey]; v != "2026-10-17T03:00:00Z" {
		t.Errorf("provider_limit_until = %q, want the 06:00 Bucharest reset", v)
	}
	if session.Metadata["state"] != "awake" || session.Metadata["last_woke_at"] == "" || session.Metadata["quarantined_until"] != "" {
		t.Errorf("hold must be additive; got state=%q last_woke_at=%q quarantined_until=%q",
			session.Metadata["state"], session.Metadata["last_woke_at"], session.Metadata["quarantined_until"])
	}
	if log.Len() == 0 {
		t.Error("no log line on entering the hold")
	}
}

func TestObserveProviderUsageLimit_ClearsWhenHoldEnds(t *testing.T) {
	now := time.Date(2026, 10, 17, 3, 5, 0, 0, time.UTC)
	clk := &clock.Fake{Time: now}
	store := newTestStore()
	session := makeBead("b1", map[string]string{
		sessionpkg.ProviderLimitSeenAtKey: "2026-10-16T15:50:00Z",
		sessionpkg.ProviderLimitUntilKey:  "2026-10-17T03:00:00Z",
	})
	var log bytes.Buffer

	got := observeProviderUsageLimit(seedSessionInfo(session), true, staticPeek(readRuntimeUsageLimitFixture(t, "usage_limit_cleared_claude_scrollback.txt"), nil),
		newDrainTracker(), sessionFrontDoor(store), nil, clk, &log)
	syncBeadFromStore(&session, store)

	if got.ProviderLimited() || session.Metadata[sessionpkg.ProviderLimitSeenAtKey] != "" {
		t.Errorf("hold not cleared: info=%v bead=%q", got.ProviderLimited(), session.Metadata[sessionpkg.ProviderLimitSeenAtKey])
	}
	if log.Len() == 0 {
		t.Error("no log line on leaving the hold")
	}
}

// A dead session cannot be on a live hold; its record is cleared without a
// pane capture (the exited-session lane owns dead panes).
func TestObserveProviderUsageLimit_DeadSessionClearsWithoutPeek(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC)}
	store := newTestStore()
	session := makeBead("b1", map[string]string{sessionpkg.ProviderLimitSeenAtKey: "2026-10-16T15:50:00Z"})
	calls := 0

	observeProviderUsageLimit(seedSessionInfo(session), false, staticPeek("", &calls), newDrainTracker(), sessionFrontDoor(store), nil, clk, &bytes.Buffer{})
	syncBeadFromStore(&session, store)

	if calls != 0 {
		t.Errorf("peeked a dead session %d times", calls)
	}
	if session.Metadata[sessionpkg.ProviderLimitSeenAtKey] != "" {
		t.Error("dead session kept its hold record")
	}
}

// Captures are throttled per session, and an unchanged verdict writes nothing.
func TestObserveProviderUsageLimit_ThrottlesAndWritesOnlyOnTransition(t *testing.T) {
	now := time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC)
	clk := &clock.Fake{Time: now}
	store := newTestStore()
	session := makeBead("b1", nil)
	dt := newDrainTracker()
	calls := 0
	healthy := staticPeek(readRuntimeUsageLimitFixture(t, "usage_limit_none_claude_idle.txt"), &calls)

	info := observeProviderUsageLimit(seedSessionInfo(session), true, healthy, dt, sessionFrontDoor(store), nil, clk, &bytes.Buffer{})
	info = observeProviderUsageLimit(info, true, healthy, dt, sessionFrontDoor(store), nil, clk, &bytes.Buffer{})
	if calls != 1 {
		t.Fatalf("captures within one interval = %d, want 1", calls)
	}
	clk.Time = now.Add(usageLimitProbeInterval)
	observeProviderUsageLimit(info, true, healthy, dt, sessionFrontDoor(store), nil, clk, &bytes.Buffer{})
	if calls != 2 {
		t.Fatalf("captures after the interval = %d, want 2", calls)
	}
	syncBeadFromStore(&session, store)
	if _, ok := session.Metadata[sessionpkg.ProviderLimitSeenAtKey]; ok {
		t.Error("a healthy session with no prior hold got a hold key written")
	}
}

func TestObserveProviderUsageLimit_PeekErrorChangesNothing(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC)}
	store := newTestStore()
	session := makeBead("b1", map[string]string{sessionpkg.ProviderLimitSeenAtKey: "2026-10-16T15:50:00Z"})
	failing := func(int) (string, error) { return "", errors.New("capture failed") }

	got := observeProviderUsageLimit(seedSessionInfo(session), true, failing, newDrainTracker(), sessionFrontDoor(store), nil, clk, &bytes.Buffer{})
	if !got.ProviderLimited() {
		t.Error("a failed capture cleared a recorded hold")
	}
}

// An empty capture (the session vanishing mid-tick) proves nothing; it must
// not clear a recorded hold and re-set it a minute later.
func TestObserveProviderUsageLimit_EmptyCaptureChangesNothing(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC)}
	store := newTestStore()
	session := makeBead("b1", map[string]string{sessionpkg.ProviderLimitSeenAtKey: "2026-10-16T15:50:00Z"})
	var log bytes.Buffer

	got := observeProviderUsageLimit(seedSessionInfo(session), true, staticPeek("  \n\n", nil), newDrainTracker(), sessionFrontDoor(store), nil, clk, &log)
	syncBeadFromStore(&session, store)
	if !got.ProviderLimited() || session.Metadata[sessionpkg.ProviderLimitSeenAtKey] == "" {
		t.Error("an empty capture cleared a recorded hold")
	}
	if log.Len() != 0 {
		t.Errorf("unexpected log: %q", log.String())
	}
}

// providerLimitEvents returns the session.provider_limit* events rec holds.
func providerLimitEvents(rec *events.Fake) []events.Event {
	var out []events.Event
	for _, e := range rec.Events {
		if e.Type == events.SessionProviderLimited || e.Type == events.SessionProviderLimitCleared {
			out = append(out, e)
		}
	}
	return out
}

// Entering the hold announces session.provider_limited once, with the reset
// the provider stated; further probes of the same hold announce nothing
// (sr-bylv).
func TestObserveProviderUsageLimit_EmitsLimitedOncePerTransition(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 5, 0, 0, time.UTC)
	clk := &clock.Fake{Time: now}
	store := newTestStore()
	session := makeBead("b1", map[string]string{"state": "awake", "session_name": "support__dispatch-sr-wisp-6bbwc", "template": "st/support.dispatch", "provider": "claude"})
	dt := newDrainTracker()
	rec := events.NewFake()
	held := staticPeek(readRuntimeUsageLimitFixture(t, "usage_limit_hold_claude_opus.txt"), nil)

	info := observeProviderUsageLimit(seedSessionInfo(session), true, held, dt, sessionFrontDoor(store), rec, clk, &bytes.Buffer{})
	for i := 1; i <= 3; i++ {
		clk.Time = now.Add(time.Duration(i) * usageLimitProbeInterval)
		info = observeProviderUsageLimit(info, true, held, dt, sessionFrontDoor(store), rec, clk, &bytes.Buffer{})
	}

	got := providerLimitEvents(rec)
	if len(got) != 1 || got[0].Type != events.SessionProviderLimited {
		t.Fatalf("events = %+v, want exactly one %s", got, events.SessionProviderLimited)
	}
	e := got[0]
	if e.Subject != "b1" || e.SessionID != "b1" {
		t.Errorf("subject/session_id = %q/%q, want b1", e.Subject, e.SessionID)
	}
	var p api.SessionProviderLimitedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.SessionID != "b1" || p.SessionName != "support__dispatch-sr-wisp-6bbwc" || p.Template != "st/support.dispatch" || p.Provider != "claude" {
		t.Errorf("payload identity = %+v", p)
	}
	if p.Until != info.ProviderLimitUntil || p.Until == "" {
		t.Errorf("payload until = %q, want the recorded reset %q", p.Until, info.ProviderLimitUntil)
	}
}

// Leaving the hold on a live session announces provider_limit_cleared with
// reason resumed and the reset the hold had recorded.
func TestObserveProviderUsageLimit_EmitsClearedResumed(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 10, 17, 3, 5, 0, 0, time.UTC)}
	store := newTestStore()
	session := makeBead("b1", map[string]string{
		sessionpkg.ProviderLimitSeenAtKey: "2026-10-16T15:50:00Z",
		sessionpkg.ProviderLimitUntilKey:  "2026-10-17T03:00:00Z",
	})
	rec := events.NewFake()

	observeProviderUsageLimit(seedSessionInfo(session), true, staticPeek(readRuntimeUsageLimitFixture(t, "usage_limit_cleared_claude_scrollback.txt"), nil),
		newDrainTracker(), sessionFrontDoor(store), rec, clk, &bytes.Buffer{})

	got := providerLimitEvents(rec)
	if len(got) != 1 || got[0].Type != events.SessionProviderLimitCleared {
		t.Fatalf("events = %+v, want exactly one %s", got, events.SessionProviderLimitCleared)
	}
	var p api.SessionProviderLimitClearedPayload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.Reason != api.ProviderLimitClearedResumed || p.Until != "2026-10-17T03:00:00Z" || p.SessionID != "b1" {
		t.Errorf("payload = %+v, want reason=resumed until=2026-10-17T03:00:00Z", p)
	}
}

// A session that stops while parked has its record dropped without a capture;
// that is announced as session_gone, not resumed — it says nothing about the
// provider's limit.
func TestObserveProviderUsageLimit_EmitsClearedSessionGone(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 10, 18, 16, 30, 0, 0, time.UTC)}
	store := newTestStore()
	session := makeBead("b1", map[string]string{
		sessionpkg.ProviderLimitSeenAtKey: "2026-10-18T09:02:00Z",
		sessionpkg.ProviderLimitUntilKey:  "2026-10-24T03:00:00Z",
	})
	rec := events.NewFake()

	observeProviderUsageLimit(seedSessionInfo(session), false, staticPeek("", nil), newDrainTracker(), sessionFrontDoor(store), rec, clk, &bytes.Buffer{})

	got := providerLimitEvents(rec)
	if len(got) != 1 || got[0].Type != events.SessionProviderLimitCleared {
		t.Fatalf("events = %+v, want exactly one %s", got, events.SessionProviderLimitCleared)
	}
	var p api.SessionProviderLimitClearedPayload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.Reason != api.ProviderLimitClearedSessionGone || p.Until != "2026-10-24T03:00:00Z" {
		t.Errorf("payload = %+v, want reason=session_gone until=2026-10-24T03:00:00Z", p)
	}
}

// A transition whose bead write fails is not announced: the bead still says
// what it said, the next probe retries, and that retry announces it. An
// event ahead of the write would announce the same transition twice.
func TestObserveProviderUsageLimit_FailedWriteEmitsNothing(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 5, 0, 0, time.UTC)
	clk := &clock.Fake{Time: now}
	store := newTestStore()
	store.metadataBatchErr = errors.New("store down")
	session := makeBead("b1", nil)
	dt := newDrainTracker()
	rec := events.NewFake()
	held := staticPeek(readRuntimeUsageLimitFixture(t, "usage_limit_hold_claude_opus.txt"), nil)

	info := observeProviderUsageLimit(seedSessionInfo(session), true, held, dt, sessionFrontDoor(store), rec, clk, &bytes.Buffer{})
	if info.ProviderLimited() {
		t.Fatal("a failed write advanced the snapshot")
	}
	if got := providerLimitEvents(rec); len(got) != 0 {
		t.Fatalf("failed write emitted %+v", got)
	}

	store.metadataBatchErr = nil
	clk.Time = now.Add(usageLimitProbeInterval)
	observeProviderUsageLimit(info, true, held, dt, sessionFrontDoor(store), rec, clk, &bytes.Buffer{})
	if got := providerLimitEvents(rec); len(got) != 1 || got[0].Type != events.SessionProviderLimited {
		t.Fatalf("retry events = %+v, want one %s", got, events.SessionProviderLimited)
	}
}
