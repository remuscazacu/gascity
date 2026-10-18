package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// usageLimitProbeInterval bounds how often a live session's pane is captured
// to look for a provider usage-limit hold. A hold lasts hours; a minute of
// detection latency costs nothing, a capture per session per tick does.
const usageLimitProbeInterval = time.Minute

// observeProviderUsageLimit records, on the session bead, whether a LIVE
// session is parked on a provider usage-limit hold (sr-p8se).
//
// Claude Code no longer exits on a session/weekly limit: it stays alive and
// shows "Usage limit reached · continuing automatically at 6am" until the
// window resets. checkRateLimitStability only classifies dead panes, so such a
// session read as healthy everywhere — on 2026-10-16 13 of 14 srvcity seats sat
// on the weekly limit for hours while the API reported them working/idle.
//
// The record is advisory and additive (sessionpkg.ProviderLimitHoldPatch): the
// session is NOT slept, quarantined or restarted — the provider resumes on its
// own at the reset, which is the best recovery available. It is written only
// on a transition, and captures are throttled per session to
// usageLimitProbeInterval. A dead session's record is cleared without a
// capture. A failed or empty capture leaves everything as it was.
//
// Each transition that reaches the bead is also announced on rec as
// session.provider_limited / session.provider_limit_cleared (sr-bylv), so the
// stall reaches the event stream instead of only ~/.gc/supervisor.log: on
// 2026-10-18 the weekly limit was recorded on every seat within minutes and
// nobody noticed for 7 hours. The event follows the write, never precedes it:
// a failed write leaves the bead as it was, the next probe retries the
// transition, and that retry is the one that announces it.
func observeProviderUsageLimit(info sessionpkg.Info, alive bool, peek func(lines int) (string, error), dt *drainTracker, sessFront *sessionpkg.Store, rec events.Recorder, clk clock.Clock, stderr io.Writer) sessionpkg.Info {
	if sessFront == nil {
		return info
	}
	now := clk.Now()
	if !alive {
		if !info.ProviderLimited() {
			return info
		}
		return clearProviderLimit(info, sessFront, rec, api.ProviderLimitClearedSessionGone, stderr)
	}
	if peek == nil || !dt.usageLimitProbeDue(info.ID, now) {
		return info
	}
	content, err := peek(rateLimitPeekLines)
	if err != nil || strings.TrimSpace(content) == "" {
		// An empty capture (the session vanishing mid-tick) proves nothing.
		return info
	}
	held := runtime.ContainsProviderUsageLimitHold(content)
	switch {
	case held && !info.ProviderLimited():
		until, ok := runtime.ParseProviderLimitReset(content, now)
		resets := "unknown"
		if ok {
			resets = until.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(stderr, "session reconciler: %s (%s) is parked on a provider usage limit; resets %s\n", info.SessionNameMetadata, info.ID, resets) //nolint:errcheck
		next, err := sessFront.ApplyPatchInfo(info, sessionpkg.ProviderLimitHoldPatch(now, until))
		if err != nil {
			return providerLimitWriteFailed(info, err, stderr)
		}
		recordProviderLimitEvent(rec, events.SessionProviderLimited, next, api.SessionProviderLimitedPayload{
			SessionID:   next.ID,
			SessionName: next.SessionNameMetadata,
			Template:    next.Template,
			Provider:    next.Provider,
			Until:       strings.TrimSpace(next.ProviderLimitUntil),
		}, fmt.Sprintf("%s is parked on a provider usage limit; resets %s", providerLimitEventName(next), resets), stderr)
		return next
	case !held && info.ProviderLimited():
		fmt.Fprintf(stderr, "session reconciler: %s (%s) is no longer parked on a provider usage limit\n", info.SessionNameMetadata, info.ID) //nolint:errcheck
		return clearProviderLimit(info, sessFront, rec, api.ProviderLimitClearedResumed, stderr)
	}
	return info
}

// clearProviderLimit drops a recorded hold and, once the write landed,
// announces session.provider_limit_cleared with the given reason.
func clearProviderLimit(info sessionpkg.Info, sessFront *sessionpkg.Store, rec events.Recorder, reason string, stderr io.Writer) sessionpkg.Info {
	next, err := sessFront.ApplyPatchInfo(info, sessionpkg.ProviderLimitClearPatch())
	if err != nil {
		return providerLimitWriteFailed(info, err, stderr)
	}
	msg := providerLimitEventName(info) + " is no longer parked on a provider usage limit"
	if reason == api.ProviderLimitClearedSessionGone {
		msg = providerLimitEventName(info) + " stopped while parked on a provider usage limit"
	}
	recordProviderLimitEvent(rec, events.SessionProviderLimitCleared, info, api.SessionProviderLimitClearedPayload{
		SessionID:   info.ID,
		SessionName: info.SessionNameMetadata,
		Template:    info.Template,
		Provider:    info.Provider,
		Until:       strings.TrimSpace(info.ProviderLimitUntil),
		Reason:      reason,
	}, msg, stderr)
	return next
}

// recordProviderLimitEvent records one provider-limit transition. A nil
// recorder (tests, callers without an event log) records nothing.
func recordProviderLimitEvent(rec events.Recorder, typ string, info sessionpkg.Info, payload events.Payload, msg string, stderr io.Writer) {
	if rec == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: marshal %s for %s: %v\n", typ, info.ID, err) //nolint:errcheck
		return
	}
	rec.Record(events.Event{
		Type:      typ,
		Actor:     "gc",
		Subject:   info.ID,
		SessionID: info.ID,
		Message:   msg,
		Payload:   raw,
	})
}

// providerLimitEventName is the session's runtime name, or its bead ID when
// none is recorded.
func providerLimitEventName(info sessionpkg.Info) string {
	if name := strings.TrimSpace(info.SessionNameMetadata); name != "" {
		return name
	}
	return info.ID
}

// providerLimitWriteFailed reports a failed hold write and returns info
// unchanged (ApplyPatchInfo folds nothing on error), so the next probe retries
// the transition.
func providerLimitWriteFailed(info sessionpkg.Info, err error, stderr io.Writer) sessionpkg.Info {
	fmt.Fprintf(stderr, "session reconciler: recording provider usage-limit hold on %s: %v\n", info.ID, err) //nolint:errcheck
	return info
}

// providerLimitHoldMaxSpan bounds how far out a recorded reset may gate a
// provider. The longest provider window is a week; a reset beyond this is a
// misparse and must not freeze the provider's starts.
const providerLimitHoldMaxSpan = 8 * 24 * time.Hour

// providerLimitHolds maps each provider to the latest reset recorded by any of
// its sessions' usage-limit holds that is still ahead of now (sr-olfu). A
// provider limit is account-wide — on 2026-10-16 every model was limited at
// once — so while it holds, starting ANY session of that provider only lands
// it in the same wall: the reconciler respawned named seats hourly and
// replaced pool seats within minutes, each into the weekly limit.
//
// Holds without a parseable reset are left out: the gate must end on its own.
// The evidence is the live parked sessions' records; a dead session's record is
// cleared by observeProviderUsageLimit, so if every parked session goes away
// the gate lifts until a replacement is observed held again.
func providerLimitHolds(infos []sessionpkg.Info, now time.Time) map[string]time.Time {
	var holds map[string]time.Time
	for _, info := range infos {
		provider := strings.TrimSpace(info.Provider)
		if provider == "" || !info.ProviderLimited() {
			continue
		}
		until, err := time.Parse(time.RFC3339, strings.TrimSpace(info.ProviderLimitUntil))
		if err != nil || !until.After(now) || until.Sub(now) > providerLimitHoldMaxSpan {
			continue
		}
		if holds == nil {
			holds = make(map[string]time.Time)
		}
		if until.After(holds[provider]) {
			holds[provider] = until
		}
	}
	return holds
}

// providerLimitGatedProvider is the provider whose usage-limit hold gates
// starting this session: its recorded provider, else the template's resolved
// one. The deterministic control-dispatcher runs gc rather than the provider
// (though its bead records the workspace provider), so nothing gates it.
func providerLimitGatedProvider(cfg *config.City, info sessionpkg.Info, tp TemplateParams) string {
	if cfg != nil && config.IsDeterministicControlDispatcher(findAgentByTemplate(cfg, tp.TemplateName)) {
		return ""
	}
	if p := strings.TrimSpace(info.Provider); p != "" {
		return p
	}
	if tp.ResolvedProvider != nil {
		return strings.TrimSpace(tp.ResolvedProvider.Name)
	}
	return ""
}

// usageLimitProbeDue reports whether the session's pane may be captured now,
// and if so stamps the capture time. A nil tracker never throttles.
func (dt *drainTracker) usageLimitProbeDue(beadID string, now time.Time) bool {
	if dt == nil {
		return true
	}
	dt.mu.Lock()
	defer dt.mu.Unlock()
	if last, ok := dt.usageLimitProbedAt[beadID]; ok && now.Sub(last) < usageLimitProbeInterval {
		return false
	}
	if dt.usageLimitProbedAt == nil {
		dt.usageLimitProbedAt = make(map[string]time.Time)
	}
	dt.usageLimitProbedAt[beadID] = now
	return true
}
