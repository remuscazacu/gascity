package runtime

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// usageLimitFooterFallbackLines bounds the tail searched when a pane has no
// horizontal rule to delimit the provider's status footer.
const usageLimitFooterFallbackLines = 4

// ContainsProviderUsageLimitHold reports whether a LIVE provider pane is
// parked on a usage-limit hold: Claude Code, on hitting a session/weekly limit,
// no longer exits — it stays alive and renders "⚠ Usage limit reached ·
// continuing automatically at 6am" in its status footer until the window
// resets (sr-p8se). The exited-session lane (ContainsProviderRateLimitScreen,
// checkRateLimitStability) only ever classifies dead panes, so it cannot see
// this state.
//
// Only the status footer — the lines below the last horizontal rule — is
// searched, and both anchors must share a line (see lineContainsAll). The same
// sentence is also printed into scrollback as "● Usage limit reached ·
// continuing automatically ..." and stays there after the reset; matching the
// whole pane would keep a recovered session classified as held.
func ContainsProviderUsageLimitHold(content string) bool {
	return lineContainsAll(paneStatusFooter(content), "Usage limit reached", "continuing automatically")
}

// paneStatusFooter returns the lines below the last horizontal rule (a line
// made of box-drawing "─"), or the last few lines when the pane has none.
func paneStatusFooter(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if isHorizontalRule(lines[i]) {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	if len(lines) > usageLimitFooterFallbackLines {
		lines = lines[len(lines)-usageLimitFooterFallbackLines:]
	}
	return strings.Join(lines, "\n")
}

func isHorizontalRule(line string) bool {
	line = strings.TrimSpace(line)
	return len(line) > 0 && strings.Trim(line, "─") == ""
}

// providerLimitResetRe matches the reset time the provider prints with a
// limit: "resets 6am (Europe/Bucharest)", "resets 7:30pm", "Resets Oct 20 at
// 11pm (America/Los_Angeles)", "continuing automatically at 6am".
var providerLimitResetRe = regexp.MustCompile(`(?i)(?:resets|continuing automatically at)\s+(?:([a-z]{3})\s+(\d{1,2})\s+at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)(?:\s*\(([^)]+)\))?`)

// ParseProviderLimitReset extracts the next reset instant from a provider
// limit message, relative to now. A match carrying an IANA zone is preferred
// over one without; without a zone, now's location is used. A time with no
// date resolves to its next occurrence after now. It returns ok=false when no
// reset time is present or the zone is unknown — callers must still treat the
// session as held, just without a known end.
func ParseProviderLimitReset(content string, now time.Time) (time.Time, bool) {
	matches := providerLimitResetRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return time.Time{}, false
	}
	best := matches[0]
	for _, m := range matches {
		if m[6] != "" {
			best = m
			break
		}
	}
	return resolveProviderLimitReset(best, now)
}

func resolveProviderLimitReset(m []string, now time.Time) (time.Time, bool) {
	loc := now.Location()
	if zone := strings.TrimSpace(m[6]); zone != "" {
		l, err := time.LoadLocation(zone)
		if err != nil {
			return time.Time{}, false
		}
		loc = l
	}
	hour, _ := strconv.Atoi(m[3])
	minute := 0
	if m[4] != "" {
		minute, _ = strconv.Atoi(m[4])
	}
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}, false
	}
	hour %= 12
	if strings.EqualFold(m[5], "pm") {
		hour += 12
	}
	local := now.In(loc)
	if m[1] != "" {
		month, err := time.Parse("Jan", strings.ToUpper(m[1][:1])+strings.ToLower(m[1][1:]))
		if err != nil {
			return time.Time{}, false
		}
		day, _ := strconv.Atoi(m[2])
		t := time.Date(local.Year(), month.Month(), day, hour, minute, 0, 0, loc)
		if t.Before(local) {
			t = t.AddDate(1, 0, 0)
		}
		return t, true
	}
	t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !t.After(local) {
		t = t.AddDate(0, 0, 1)
	}
	return t, true
}
