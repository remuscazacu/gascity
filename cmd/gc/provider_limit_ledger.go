package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/fsys"
)

// The provider usage-limit ledger (sr-896w) is the city's durable memory of a
// provider limit. The sr-olfu start gate used to derive its evidence only from
// LIVE parked sessions (providerLimitHolds), so the gate lifted the moment the
// last parked session went away — suspended, idle-timed-out or killed — and
// the next start landed straight in the same wall. On 2026-10-18 a config
// reload in a suspended city restarted the intake poller into a weekly limit
// that every drained seat had already recorded (sr-pkh5).
//
// The ledger keeps, per provider, the latest stated reset any of its sessions
// recorded. A hold ends on its own at that reset, or earlier when a LIVE
// session of the provider is seen leaving its hold (the limit lifted — e.g.
// the plan was upgraded), or when `gc resume` resumes the city: the operator
// confirms the limit by hand before resuming, and with no live session
// nothing else could observe an early lift.

// providerLimitLedgerFile is the ledger's path under the city's runtime dir.
func providerLimitLedgerFile(cityPath string) string {
	return citylayout.RuntimePath(cityPath, "runtime", "provider-limits.json")
}

// providerLimitLedgerEntry is one provider's durable hold.
type providerLimitLedgerEntry struct {
	Until     string `json:"until"`                // RFC3339 UTC: the latest stated reset
	SeenAt    string `json:"seen_at,omitempty"`    // when the hold carrying Until was first seen
	SessionID string `json:"session_id,omitempty"` // the session bead that recorded it
}

type providerLimitLedgerFormat struct {
	Providers map[string]providerLimitLedgerEntry `json:"providers"`
}

// loadProviderLimitLedger reads the ledger. A missing file is an empty ledger.
func loadProviderLimitLedger(fs fsys.FS, cityPath string) (map[string]providerLimitLedgerEntry, error) {
	data, err := fs.ReadFile(providerLimitLedgerFile(cityPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]providerLimitLedgerEntry{}, nil
		}
		return nil, err
	}
	var f providerLimitLedgerFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", providerLimitLedgerFile(cityPath), err)
	}
	if f.Providers == nil {
		f.Providers = map[string]providerLimitLedgerEntry{}
	}
	return f.Providers, nil
}

func saveProviderLimitLedger(fs fsys.FS, cityPath string, providers map[string]providerLimitLedgerEntry) error {
	p := providerLimitLedgerFile(cityPath)
	if len(providers) == 0 {
		if err := fs.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := fs.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(providerLimitLedgerFormat{Providers: providers}, "", "  ")
	if err != nil {
		return err
	}
	return fsys.WriteFileAtomic(fs, p, append(data, '\n'), 0o644)
}

// recordProviderLimitHold widens provider's durable hold to until (never
// narrows it) and prunes holds whose reset has passed.
func recordProviderLimitHold(fs fsys.FS, cityPath, provider string, until, seenAt time.Time, sessionID string, now time.Time) error {
	provider = strings.TrimSpace(provider)
	if provider == "" || until.IsZero() {
		return nil
	}
	providers, err := loadProviderLimitLedger(fs, cityPath)
	if err != nil {
		providers = map[string]providerLimitLedgerEntry{} // a corrupt ledger is rewritten, not trusted
	}
	pruneProviderLimitLedger(providers, now)
	if cur, ok := providers[provider]; ok {
		if t, err := time.Parse(time.RFC3339, cur.Until); err == nil && !until.After(t) {
			return saveProviderLimitLedger(fs, cityPath, providers)
		}
	}
	providers[provider] = providerLimitLedgerEntry{
		Until:     until.UTC().Format(time.RFC3339),
		SeenAt:    seenAt.UTC().Format(time.RFC3339),
		SessionID: sessionID,
	}
	return saveProviderLimitLedger(fs, cityPath, providers)
}

// clearProviderLimitHold drops provider's durable hold; an empty provider
// drops every hold. It reports which providers were dropped.
func clearProviderLimitHold(fs fsys.FS, cityPath, provider string) ([]string, error) {
	providers, err := loadProviderLimitLedger(fs, cityPath)
	if err != nil {
		// Clearing must not fail on a ledger nobody can read: drop it.
		return nil, saveProviderLimitLedger(fs, cityPath, nil)
	}
	var dropped []string
	for name := range providers {
		if provider == "" || name == strings.TrimSpace(provider) {
			dropped = append(dropped, name)
			delete(providers, name)
		}
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	sort.Strings(dropped)
	return dropped, saveProviderLimitLedger(fs, cityPath, providers)
}

func pruneProviderLimitLedger(providers map[string]providerLimitLedgerEntry, now time.Time) {
	for name, e := range providers {
		if t, err := time.Parse(time.RFC3339, e.Until); err != nil || !t.After(now) {
			delete(providers, name)
		}
	}
}

// durableProviderLimitHolds is the ledger in the shape the start gates use,
// with the same bounds providerLimitHolds applies to a bead record: a reset
// that has passed gates nothing, and one beyond providerLimitHoldMaxSpan is a
// misparse that must not freeze the provider's starts. Read errors gate
// nothing (the gate fails open, as the provider-health gate does).
func durableProviderLimitHolds(fs fsys.FS, cityPath string, now time.Time) map[string]time.Time {
	if strings.TrimSpace(cityPath) == "" {
		return nil
	}
	providers, err := loadProviderLimitLedger(fs, cityPath)
	if err != nil {
		return nil
	}
	var holds map[string]time.Time
	for name, e := range providers {
		until, err := time.Parse(time.RFC3339, e.Until)
		if err != nil || !until.After(now) || until.Sub(now) > providerLimitHoldMaxSpan {
			continue
		}
		if holds == nil {
			holds = make(map[string]time.Time)
		}
		holds[name] = until
	}
	return holds
}

// mergeProviderLimitHolds folds the durable ledger into the live-session
// holds, keeping the later reset per provider.
func mergeProviderLimitHolds(live, durable map[string]time.Time) map[string]time.Time {
	if len(durable) == 0 {
		return live
	}
	out := make(map[string]time.Time, len(live)+len(durable))
	for p, t := range live {
		out[p] = t
	}
	for p, t := range durable {
		if t.After(out[p]) {
			out[p] = t
		}
	}
	return out
}

// providerLimitLedgerSink is how the usage-limit observer reaches the ledger.
// A nil sink records nothing (tests, callers with no city).
type providerLimitLedgerSink interface {
	held(provider string, until, seenAt time.Time, sessionID string)
	lifted(provider string)
}

// cityProviderLimitLedger is the OS-filesystem sink for one city. With no city
// path it records nothing, so a caller without a city never writes a relative
// .gc/ into its working directory.
type cityProviderLimitLedger struct {
	fs       fsys.FS
	cityPath string
	now      func() time.Time
	stderr   io.Writer
}

func (l cityProviderLimitLedger) held(provider string, until, seenAt time.Time, sessionID string) {
	if strings.TrimSpace(l.cityPath) == "" {
		return
	}
	if err := recordProviderLimitHold(l.fs, l.cityPath, provider, until, seenAt, sessionID, l.now()); err != nil {
		fmt.Fprintf(l.stderr, "session reconciler: recording provider %q usage-limit hold in %s: %v\n", provider, providerLimitLedgerFile(l.cityPath), err) //nolint:errcheck
	}
}

func (l cityProviderLimitLedger) lifted(provider string) {
	if strings.TrimSpace(l.cityPath) == "" {
		return
	}
	dropped, err := clearProviderLimitHold(l.fs, l.cityPath, provider)
	if err != nil {
		fmt.Fprintf(l.stderr, "session reconciler: clearing provider %q usage-limit hold in %s: %v\n", provider, providerLimitLedgerFile(l.cityPath), err) //nolint:errcheck
		return
	}
	if len(dropped) > 0 {
		fmt.Fprintf(l.stderr, "session reconciler: provider %q usage limit lifted (a live session resumed); recorded hold cleared\n", provider) //nolint:errcheck
	}
}
