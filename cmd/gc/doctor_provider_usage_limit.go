package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/session"
)

// providerUsageLimitCheck counts live provider sessions parked on a provider
// usage-limit hold (sr-bylv). The session reconciler records each hold on the
// session bead (provider_limit_seen_at, sr-p8se) and announces it as
// session.provider_limited, but a provider limit is account-wide: when it
// lands, every seat stops at once and the city looks idle, not broken. On
// 2026-10-16 that was 13 of 14 seats, and on 2026-10-18 the weekly limit sat
// recorded on every seat for 7 hours before anyone looked. This check is the
// city-wide read: any parked seat warns, half or more is an error.
//
// "Live" is an open session bead in state active, awake or draining — a
// draining seat still runs the provider, and the reconciler records its hold
// like any other live one (10-14..16 the limited fleet spent much of its time
// draining). The deterministic
// control-dispatcher runs gc, not the provider, so it is never limited and is
// left out of both counts — counting it would make a total outage read as
// 13/14 instead of 13/13.
//
// It also reads the city's durable provider-limit ledger (sr-896w). With every
// parked seat gone the ledger still gates starts, and a check that counted
// only live seats would read OK while gc refuses to start anything.
type providerUsageLimitCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(string) (beads.Store, error)
	fs       fsys.FS
	now      func() time.Time
}

func newProviderUsageLimitCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *providerUsageLimitCheck {
	return &providerUsageLimitCheck{cfg: cfg, cityPath: cityPath, newStore: newStore, fs: fsys.OSFS{}, now: time.Now}
}

func (c *providerUsageLimitCheck) Name() string { return "provider-usage-limit" }

// CanFix returns false: the limit is the provider account's. Waiting for the
// reset, adding capacity, or suspending the city is the operator's call.
func (c *providerUsageLimitCheck) CanFix() bool { return false }

// Fix is a no-op. Detection only.
func (c *providerUsageLimitCheck) Fix(_ *doctor.CheckContext) error { return nil }

// Run reads every session bead once and compares parked live seats with all
// live provider seats.
func (c *providerUsageLimitCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	store, err := c.newStore(c.cityPath)
	if err != nil {
		return warnCheck(c.Name(),
			fmt.Sprintf("skipping provider usage-limit scan: opening bead store: %v", err),
			"fix bead store access, then rerun gc doctor",
			nil)
	}
	infos, err := cliSessionFrontDoor(store, c.cfg, c.cityPath).ListAll(session.ListAllOptions{})
	if err != nil {
		return warnCheck(c.Name(),
			fmt.Sprintf("skipping provider usage-limit scan: listing sessions: %v", err),
			"fix bead store access, then rerun gc doctor",
			nil)
	}

	now := c.now()
	live := 0
	var details []string
	var latestReset time.Time
	for _, info := range infos {
		if info.Closed || (info.State != session.StateActive && info.State != session.StateAwake && info.State != session.StateDraining) {
			continue
		}
		if config.IsDeterministicControlDispatcher(findAgentByTemplate(c.cfg, info.Template)) {
			continue
		}
		live++
		if !info.ProviderLimited() {
			continue
		}
		until := strings.TrimSpace(info.ProviderLimitUntil)
		if t, err := time.Parse(time.RFC3339, until); err == nil && t.After(latestReset) {
			latestReset = t
		}
		if until == "" {
			until = "unknown"
		}
		details = append(details, fmt.Sprintf("%s (%s, template %s): parked since %s, resets %s",
			providerLimitEventName(info), info.ID, providerUsageLimitOr(info.Template, "unknown"),
			providerUsageLimitOr(strings.TrimSpace(info.ProviderLimitSeenAt), "unknown"), until))
	}

	limited := len(details)
	sort.Strings(details)
	recorded := c.recordedHolds(now)
	if limited == 0 {
		if len(recorded) == 0 {
			return okCheck(c.Name(), fmt.Sprintf("no live provider session is parked on a usage limit (%d live)", live))
		}
		return warnCheck(c.Name(),
			fmt.Sprintf("no live session is parked, but the city's recorded usage limit still holds %d provider(s); gc will not start their sessions until the reset", len(recorded)),
			"the hold lifts on its own at the stated reset, when a live session is seen resuming, or on `gc resume`. "+
				"If the limit is already lifted (plan upgraded), `gc resume` the city or remove "+providerLimitLedgerFile(c.cityPath)+".",
			recorded)
	}
	details = append(details, recorded...)

	msg := fmt.Sprintf("%d of %d live provider session(s) parked on a provider usage limit", limited, live)
	if !latestReset.IsZero() {
		msg += fmt.Sprintf("; latest stated reset %s (in %s)", latestReset.UTC().Format(time.RFC3339),
			latestReset.Sub(now).Round(time.Minute))
	}
	hint := "a provider usage limit is account-wide: parked seats do no work until it resets. " +
		"They resume on their own at the reset; a seat whose turn ended on the limit may need a nudge. " +
		"To stop the city spending wakes into the limit meanwhile, `gc suspend` it and `gc resume` after the reset."
	if limited*2 >= live {
		return errorCheck(c.Name(), msg+" — the city is effectively stalled", hint, details)
	}
	return warnCheck(c.Name(), msg, hint, details)
}

func providerUsageLimitOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// recordedHolds renders the durable ledger's live holds, one detail line each.
func (c *providerUsageLimitCheck) recordedHolds(now time.Time) []string {
	holds := durableProviderLimitHolds(c.fs, c.cityPath, now)
	out := make([]string, 0, len(holds))
	for provider, until := range holds {
		out = append(out, fmt.Sprintf("provider %s: recorded hold until %s (in %s); starts are gated",
			provider, until.UTC().Format(time.RFC3339), until.Sub(now).Round(time.Minute)))
	}
	sort.Strings(out)
	return out
}
