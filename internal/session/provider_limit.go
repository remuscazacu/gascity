package session

import (
	"strings"
	"time"
)

// Provider usage-limit hold metadata (sr-p8se). Claude Code no longer exits
// when it hits a session/weekly limit: it stays alive and waits for the reset.
// The reconciler records that hold on the live session's bead so the API and
// operators can tell a seat that is working from one that is parked until the
// reset. Unlike RateLimitQuarantinePatch (for a provider that EXITED into a
// limit screen) these keys are purely additive: they never touch state,
// last_woke_at or sleep_reason, so the reconciler keeps treating the session as
// the live session it is.
const (
	ProviderLimitSeenAtKey = "provider_limit_seen_at"
	ProviderLimitUntilKey  = "provider_limit_until"
)

// ProviderLimitHoldPatch records a hold first seen at seenAt. A zero until
// (reset time not parseable) is stored as empty: the session is still held.
func ProviderLimitHoldPatch(seenAt, until time.Time) MetadataPatch {
	untilRaw := ""
	if !until.IsZero() {
		untilRaw = until.UTC().Format(time.RFC3339)
	}
	return MetadataPatch{
		ProviderLimitSeenAtKey: seenAt.UTC().Format(time.RFC3339),
		ProviderLimitUntilKey:  untilRaw,
	}
}

// ProviderLimitClearPatch removes a recorded hold.
func ProviderLimitClearPatch() MetadataPatch {
	return MetadataPatch{ProviderLimitSeenAtKey: "", ProviderLimitUntilKey: ""}
}

// ProviderLimited reports whether a provider usage-limit hold is recorded.
func (info Info) ProviderLimited() bool {
	return strings.TrimSpace(info.ProviderLimitSeenAt) != ""
}
