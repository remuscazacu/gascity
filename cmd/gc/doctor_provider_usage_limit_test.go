package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/session"
)

// providerUsageLimitSeat is one session bead as the reconciler leaves it.
type providerUsageLimitSeat struct {
	id, name, template, state string
	parkedUntil               string // "" = not parked
}

// runProviderUsageLimitCheck seeds seats and runs the check at a fixed now.
func runProviderUsageLimitCheck(t *testing.T, cfg *config.City, seats []providerUsageLimitSeat) *doctor.CheckResult {
	t.Helper()
	var rows []beads.Bead
	for _, s := range seats {
		md := map[string]string{"state": s.state, "session_name": s.name, "template": s.template}
		if s.parkedUntil != "" {
			md[session.ProviderLimitSeenAtKey] = "2026-10-18T09:02:00Z"
			md[session.ProviderLimitUntilKey] = s.parkedUntil
		}
		rows = append(rows, beads.Bead{
			ID:       s.id,
			Status:   "open",
			Type:     sessionBeadType,
			Labels:   []string{session.LabelSession},
			Metadata: md,
		})
	}
	mem := beads.NewMemStoreFrom(len(rows), rows, nil)
	if cfg == nil {
		cfg = &config.City{}
	}
	c := newProviderUsageLimitCheck(cfg, t.TempDir(), func(string) (beads.Store, error) { return mem, nil })
	c.now = func() time.Time { return time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC) }
	return c.Run(&doctor.CheckContext{})
}

func TestProviderUsageLimitCheck_NoneParkedIsOK(t *testing.T) {
	r := runProviderUsageLimitCheck(t, nil, []providerUsageLimitSeat{
		{id: "s1", name: "dispatch-1", template: "st/support.dispatch", state: "awake"},
		{id: "s2", name: "l1-1", template: "st/support.l1-support", state: "active"},
	})
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok: %#v", r.Status, r)
	}
	if !strings.Contains(r.Message, "2 live") {
		t.Errorf("message = %q, want the live count", r.Message)
	}
}

// One parked seat in four is a warning naming the seat and its reset.
func TestProviderUsageLimitCheck_MinorityParkedWarns(t *testing.T) {
	r := runProviderUsageLimitCheck(t, nil, []providerUsageLimitSeat{
		{id: "s1", name: "dispatch-1", template: "st/support.dispatch", state: "awake", parkedUntil: "2026-10-24T03:00:00Z"},
		{id: "s2", name: "l1-1", template: "st/support.l1-support", state: "awake"},
		{id: "s3", name: "l1-2", template: "st/support.l1-support", state: "awake"},
		{id: "s4", name: "l2-1", template: "st/support.l2-erp", state: "active"},
	})
	if r.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning: %#v", r.Status, r)
	}
	if !strings.Contains(r.Message, "1 of 4") || !strings.Contains(r.Message, "2026-10-24T03:00:00Z") {
		t.Errorf("message = %q, want 1 of 4 and the reset", r.Message)
	}
	if len(r.Details) != 1 || !strings.Contains(r.Details[0], "dispatch-1") || !strings.Contains(r.Details[0], "s1") {
		t.Errorf("details = %v, want the parked seat", r.Details)
	}
}

// Half or more parked is an error: the 2026-10-16 outage was 13 of 14. A
// draining seat is live and counts.
func TestProviderUsageLimitCheck_HalfParkedIsError(t *testing.T) {
	r := runProviderUsageLimitCheck(t, nil, []providerUsageLimitSeat{
		{id: "s1", name: "dispatch-1", template: "st/support.dispatch", state: "awake", parkedUntil: "2026-10-24T03:00:00Z"},
		{id: "s2", name: "l1-1", template: "st/support.l1-support", state: "draining", parkedUntil: "2026-10-24T03:00:00Z"},
		{id: "s3", name: "l1-2", template: "st/support.l1-support", state: "awake"},
		{id: "s4", name: "l2-1", template: "st/support.l2-erp", state: "awake"},
	})
	if r.Status != doctor.StatusError {
		t.Fatalf("status = %v, want error at 2 of 4: %#v", r.Status, r)
	}
	if !strings.Contains(r.Message, "2 of 4") || !strings.Contains(r.Message, "stalled") {
		t.Errorf("message = %q", r.Message)
	}
	if r.FixHint == "" {
		t.Error("no fix hint")
	}
}

// The deterministic control-dispatcher never runs the provider, so it is out
// of both counts: every provider seat parked reads 2 of 2, not 2 of 3.
func TestProviderUsageLimitCheck_ControlDispatcherNotCounted(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{
		{Name: config.ControlDispatcherAgentName, StartCommand: "gc convoy control --serve"},
	}}
	r := runProviderUsageLimitCheck(t, cfg, []providerUsageLimitSeat{
		{id: "s0", name: "control-dispatcher", template: config.ControlDispatcherAgentName, state: "awake"},
		{id: "s1", name: "dispatch-1", template: "st/support.dispatch", state: "awake", parkedUntil: "2026-10-24T03:00:00Z"},
		{id: "s2", name: "l1-1", template: "st/support.l1-support", state: "awake", parkedUntil: "2026-10-24T03:00:00Z"},
	})
	if !strings.Contains(r.Message, "2 of 2") {
		t.Fatalf("message = %q, want 2 of 2 (control-dispatcher excluded)", r.Message)
	}
}

// A stale record on a session that is not live (asleep, or the city
// suspended before the reconciler cleared it) is not a parked seat.
func TestProviderUsageLimitCheck_NonLiveRecordsIgnored(t *testing.T) {
	r := runProviderUsageLimitCheck(t, nil, []providerUsageLimitSeat{
		{id: "s1", name: "dispatch-1", template: "st/support.dispatch", state: "asleep", parkedUntil: "2026-10-24T03:00:00Z"},
		{id: "s2", name: "l1-1", template: "st/support.l1-support", state: "awake"},
	})
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok (asleep record ignored): %#v", r.Status, r)
	}
}
