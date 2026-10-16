package session

import (
	"testing"
	"time"
)

func TestProviderLimitHoldPatchProjectsOntoInfo(t *testing.T) {
	seen := time.Date(2026, 10, 16, 15, 50, 0, 0, time.UTC)
	until := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)

	info := Info{ID: "sr-wisp-x"}.ApplyPatch(ProviderLimitHoldPatch(seen, until))
	if info.ProviderLimitSeenAt != "2026-10-16T15:50:00Z" {
		t.Errorf("ProviderLimitSeenAt = %q", info.ProviderLimitSeenAt)
	}
	if info.ProviderLimitUntil != "2026-10-17T03:00:00Z" {
		t.Errorf("ProviderLimitUntil = %q", info.ProviderLimitUntil)
	}
	if !info.ProviderLimited() {
		t.Error("ProviderLimited() = false after a hold patch")
	}

	cleared := info.ApplyPatch(ProviderLimitClearPatch())
	if cleared.ProviderLimited() || cleared.ProviderLimitUntil != "" {
		t.Errorf("after clear: limited=%v until=%q", cleared.ProviderLimited(), cleared.ProviderLimitUntil)
	}
}

// An unparseable reset time must still record the hold: the session is
// limited, gc just does not know until when.
func TestProviderLimitHoldPatchWithoutKnownReset(t *testing.T) {
	patch := ProviderLimitHoldPatch(time.Date(2026, 10, 16, 15, 50, 0, 0, time.UTC), time.Time{})
	if v, ok := patch[ProviderLimitUntilKey]; !ok || v != "" {
		t.Errorf("until = %q, present=%v; want present and empty", v, ok)
	}
	if !(Info{}).ApplyPatch(patch).ProviderLimited() {
		t.Error("ProviderLimited() = false for a hold with unknown reset")
	}
}

func TestProviderLimitKeysProjectFromPersistedBead(t *testing.T) {
	b := oracleBaseBeads()[0]
	b.Metadata = map[string]string{ProviderLimitSeenAtKey: "2026-10-16T15:50:00Z", ProviderLimitUntilKey: "2026-10-17T03:00:00Z"}
	info := infoFromPersistedBead(b)
	if !info.ProviderLimited() || info.ProviderLimitUntil != "2026-10-17T03:00:00Z" {
		t.Errorf("projection: limited=%v until=%q", info.ProviderLimited(), info.ProviderLimitUntil)
	}
}
