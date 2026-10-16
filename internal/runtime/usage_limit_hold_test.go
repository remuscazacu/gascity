package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readUsageLimitFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// The fixtures are real tmux captures from srvcity on 2026-10-16 (sr-p8se):
// Claude Code hit the account's weekly limit and, instead of exiting, stayed
// alive on a "continuing automatically at 6am" hold. The exited-session lane
// (ContainsProviderRateLimitScreen via checkRateLimitStability) never sees a
// live pane, so nothing classified these sessions.
func TestContainsProviderUsageLimitHold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "live hold, opus pane", content: readUsageLimitFixture(t, "usage_limit_hold_claude_opus.txt"), want: true},
		{name: "live hold, sonnet pane", content: readUsageLimitFixture(t, "usage_limit_hold_claude_sonnet.txt"), want: true},
		// Captured through gc session peek — the reconciler's own peek path.
		{name: "live hold, via gc peek", content: readUsageLimitFixture(t, "usage_limit_hold_claude_gcpeek.txt"), want: true},
		{name: "healthy idle pane", content: readUsageLimitFixture(t, "usage_limit_none_claude_idle.txt"), want: false},
		// After the reset the footer clears but the "● Usage limit reached ·
		// continuing automatically" line stays in scrollback; that must not
		// keep the session classified as held.
		{name: "cleared hold, banner only in scrollback", content: readUsageLimitFixture(t, "usage_limit_cleared_claude_scrollback.txt"), want: false},
		{name: "anchors on separate lines", content: "Usage limit reached\ncontinuing automatically at 6am", want: false},
		{name: "empty", content: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ContainsProviderUsageLimitHold(tt.content); got != tt.want {
				t.Errorf("ContainsProviderUsageLimitHold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseProviderLimitReset(t *testing.T) {
	t.Parallel()
	bucharest, err := time.LoadLocation("Europe/Bucharest")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 18:50 EEST on the day of the outage.
	now := time.Date(2026, 10, 16, 18, 50, 0, 0, bucharest)
	tests := []struct {
		name    string
		content string
		want    time.Time
		ok      bool
	}{
		{name: "real pane, reset tomorrow morning", content: readUsageLimitFixture(t, "usage_limit_hold_claude_opus.txt"), want: time.Date(2026, 10, 17, 6, 0, 0, 0, bucharest), ok: true},
		{name: "gc peek capture", content: readUsageLimitFixture(t, "usage_limit_hold_claude_gcpeek.txt"), want: time.Date(2026, 10, 17, 6, 0, 0, 0, bucharest), ok: true},
		{name: "later today", content: "You've hit your weekly limit · resets 11pm (Europe/Bucharest)", want: time.Date(2026, 10, 16, 23, 0, 0, 0, bucharest), ok: true},
		{name: "minutes", content: "You've hit your session limit · resets 7:30pm (Europe/Bucharest)", want: time.Date(2026, 10, 16, 19, 30, 0, 0, bucharest), ok: true},
		{name: "12am is midnight", content: "resets 12am (Europe/Bucharest)", want: time.Date(2026, 10, 17, 0, 0, 0, 0, bucharest), ok: true},
		{name: "explicit date and other zone", content: "Resets Oct 20 at 11pm (America/Los_Angeles)", want: time.Date(2026, 10, 20, 23, 0, 0, 0, la), ok: true},
		{name: "no zone uses now's location", content: "Continuing automatically at 6am · esc to cancel", want: time.Date(2026, 10, 17, 6, 0, 0, 0, bucharest), ok: true},
		{name: "unknown zone", content: "resets 6am (Mars/Olympus)", ok: false},
		{name: "no reset text", content: "Usage limit reached", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseProviderLimitReset(tt.content, now)
			if ok != tt.ok {
				t.Fatalf("ParseProviderLimitReset() ok = %v, want %v (got %v)", ok, tt.ok, got)
			}
			if ok && !got.Equal(tt.want) {
				t.Errorf("ParseProviderLimitReset() = %v, want %v", got, tt.want)
			}
		})
	}
}
