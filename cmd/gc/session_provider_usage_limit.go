package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
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
func observeProviderUsageLimit(info sessionpkg.Info, alive bool, peek func(lines int) (string, error), dt *drainTracker, sessFront *sessionpkg.Store, clk clock.Clock, stderr io.Writer) sessionpkg.Info {
	if sessFront == nil {
		return info
	}
	now := clk.Now()
	if !alive {
		if !info.ProviderLimited() {
			return info
		}
		return clearProviderLimit(info, sessFront, stderr)
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
		return providerLimitWritten(info, next, err, stderr)
	case !held && info.ProviderLimited():
		fmt.Fprintf(stderr, "session reconciler: %s (%s) is no longer parked on a provider usage limit\n", info.SessionNameMetadata, info.ID) //nolint:errcheck
		return clearProviderLimit(info, sessFront, stderr)
	}
	return info
}

func clearProviderLimit(info sessionpkg.Info, sessFront *sessionpkg.Store, stderr io.Writer) sessionpkg.Info {
	next, err := sessFront.ApplyPatchInfo(info, sessionpkg.ProviderLimitClearPatch())
	return providerLimitWritten(info, next, err, stderr)
}

// providerLimitWritten returns the advanced snapshot, or info unchanged when
// the write failed (ApplyPatchInfo folds nothing on error).
func providerLimitWritten(info, next sessionpkg.Info, err error, stderr io.Writer) sessionpkg.Info {
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: recording provider usage-limit hold on %s: %v\n", info.ID, err) //nolint:errcheck
		return info
	}
	return next
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
