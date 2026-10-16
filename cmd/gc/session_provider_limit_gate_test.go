package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// providerLimitGateEnv builds a city with two "claude" sessions (sr-olfu): a
// "parked" one alive on a recorded usage-limit hold that resets at until, and
// a dead "worker" with assigned work that would ordinarily be respawned.
func providerLimitGateEnv(t *testing.T, until time.Time) (*reconcilerTestEnv, []beads.Bead, []beads.Bead) {
	return providerLimitGateEnvFor(t, config.Agent{Name: "worker"}, until)
}

// providerLimitGateEnvFor is providerLimitGateEnv with the dead session run by
// the given agent, whose template is the agent's name.
func providerLimitGateEnvFor(t *testing.T, workerAgent config.Agent, until time.Time) (*reconcilerTestEnv, []beads.Bead, []beads.Bead) {
	t.Helper()
	env := newReconcilerTestEnv(t)
	env.cfg = &config.City{Agents: []config.Agent{workerAgent, {Name: "parked"}}}
	env.addDesired("worker", workerAgent.Name, false)
	env.addDesired("parked", "parked", true)
	env.sp.PeekOutput = map[string]string{"parked": readRuntimeUsageLimitFixture(t, "usage_limit_hold_claude_opus.txt")}

	worker := env.createSessionBead("worker", workerAgent.Name)
	env.markSessionActive(&worker)
	env.setSessionMetadata(&worker, map[string]string{
		"provider":     "claude",
		"last_woke_at": env.clk.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339),
	})
	parked := env.createSessionBead("parked", "parked")
	env.markSessionActive(&parked)
	env.setSessionMetadata(&parked, map[string]string{
		"provider":                        "claude",
		"last_woke_at":                    env.clk.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339),
		sessionpkg.ProviderLimitSeenAtKey: env.clk.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		sessionpkg.ProviderLimitUntilKey:  until.UTC().Format(time.RFC3339),
	})

	task, err := env.store.Create(beads.Bead{Title: "assigned task", Type: "task"})
	if err != nil {
		t.Fatalf("Create(task): %v", err)
	}
	status, assignee := "in_progress", worker.ID
	if err := env.store.Update(task.ID, beads.UpdateOpts{Status: &status, Assignee: &assignee}); err != nil {
		t.Fatalf("Update(task): %v", err)
	}
	task, err = env.store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get(task): %v", err)
	}
	return env, []beads.Bead{worker, parked}, []beads.Bead{task}
}

func providerLimitGateTick(t *testing.T, env *reconcilerTestEnv, sessions, work []beads.Bead) int {
	t.Helper()
	current := make([]beads.Bead, 0, len(sessions))
	for _, s := range sessions {
		got, err := env.store.Get(s.ID)
		if err != nil {
			t.Fatalf("Get(%s): %v", s.ID, err)
		}
		current = append(current, got)
	}
	return reconcileSessionBeadsAtPath(
		context.Background(), env.city, current, env.desiredState,
		configuredSessionNames(env.cfg, "", env.store), env.cfg, env.sp, env.store,
		nil, work, nil, nil, env.dt, map[string]int{"worker": 1, "parked": 1, config.ControlDispatcherAgentName: 1}, false, nil, "",
		nil, env.clk, env.rec, 0, 0, &env.stdout, &env.stderr, env.startOptions...,
	)
}

// While any session of a provider records a usage-limit hold with a known
// reset, no other session of that provider is started into the same wall: on
// 2026-10-16 srvcity respawned named seats hourly and replaced pool seats
// within minutes, each straight into the weekly limit (sr-olfu).
func TestReconcileSessionBeads_ProviderLimitHoldDefersRespawn(t *testing.T) {
	env, sessions, work := providerLimitGateEnv(t, time.Date(2026, 3, 8, 13, 0, 0, 0, time.UTC))

	if woken := providerLimitGateTick(t, env, sessions, work); woken != 0 {
		t.Fatalf("woken = %d, want 0 while claude is held until 13:00; stderr=%s", woken, env.stderr.String())
	}
	if env.sp.IsRunning("worker") {
		t.Fatal("worker was started into a recorded provider usage-limit hold")
	}
	if !env.sp.IsRunning("parked") {
		t.Fatal("the gate must not touch the live held session")
	}

	env.clk.Time = time.Date(2026, 3, 8, 13, 0, 1, 0, time.UTC)
	if woken := providerLimitGateTick(t, env, sessions, work); woken != 1 {
		t.Fatalf("woken = %d after the reset, want 1; stderr=%s", woken, env.stderr.String())
	}
}

// The deterministic control-dispatcher runs gc, not the provider, so a
// provider's usage limit must not keep it down — even though its session bead
// carries the workspace provider.
func TestReconcileSessionBeads_ProviderLimitHoldExemptsControlDispatcher(t *testing.T) {
	dispatcher := config.Agent{
		Name:         config.ControlDispatcherAgentName,
		StartCommand: config.ControlDispatcherStartCommandFor(config.ControlDispatcherAgentName),
	}
	env, sessions, work := providerLimitGateEnvFor(t, dispatcher, time.Date(2026, 3, 8, 13, 0, 0, 0, time.UTC))

	if woken := providerLimitGateTick(t, env, sessions, work); woken != 1 {
		t.Fatalf("woken = %d, want the control-dispatcher started despite the claude hold; stderr=%s", woken, env.stderr.String())
	}
}

// A hold whose reset is already past, or that is implausibly far out (a
// misparse), gates nothing.
func TestProviderLimitHolds(t *testing.T) {
	now := time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC)
	held := func(provider, until string) sessionpkg.Info {
		return sessionpkg.Info{Provider: provider, ProviderLimitSeenAt: "2026-10-16T15:50:00Z", ProviderLimitUntil: until}
	}
	got := providerLimitHolds([]sessionpkg.Info{
		held("claude", "2026-10-17T03:00:00Z"),
		held("claude", "2026-10-17T01:00:00Z"),
		held("codex", "2026-10-16T17:00:00Z"),                          // already reset
		held("gemini", "2027-01-01T00:00:00Z"),                         // beyond any limit window
		held("", "2026-10-17T03:00:00Z"),                               // no provider to gate
		{Provider: "kimi", ProviderLimitUntil: "2026-10-17T03:00:00Z"}, // until without a hold
		held("glm", ""), // reset unknown
	}, now)
	if len(got) != 1 {
		t.Fatalf("holds = %v, want only claude", got)
	}
	if want := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC); !got["claude"].Equal(want) {
		t.Errorf("claude until = %v, want the latest reset %v", got["claude"], want)
	}
}

// A pool with demand does not mint fresh seats for a provider on a recorded
// usage-limit hold: on 2026-10-16 an L1 slot was replaced twice within minutes,
// each new seat straight into the weekly limit (sr-olfu). The held seat itself
// is still reused.
func TestBuildDesiredState_ProviderLimitHoldBlocksNewPoolSessionCreate(t *testing.T) {
	now := time.Now().UTC()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              "worker",
			Provider:          "claude",
			StartCommand:      "echo",
			ScaleCheck:        "printf 3",
			MinActiveSessions: intPtr(0),
			MaxActiveSessions: intPtr(5),
		}},
	}
	heldSeat := func(until time.Time) beads.Bead {
		return beads.Bead{
			ID:     "session-worker-1",
			Title:  "worker",
			Type:   sessionBeadType,
			Status: "open",
			Labels: []string{sessionBeadLabel, "template:worker"},
			Metadata: map[string]string{
				"session_name":                    "worker-1",
				"template":                        "worker",
				"agent_name":                      "worker",
				"pool_slot":                       "1",
				poolManagedMetadataKey:            boolMetadata(true),
				"state":                           "active",
				"provider":                        "claude",
				sessionpkg.ProviderLimitSeenAtKey: now.Add(-time.Hour).Format(time.RFC3339),
				sessionpkg.ProviderLimitUntilKey:  until.Format(time.RFC3339),
			},
		}
	}
	workers := func(until time.Time) (int, string) {
		var stderr strings.Builder
		result := buildDesiredStateWithSessionBeads(
			"test-city", t.TempDir(), now,
			cfg, runtime.NewFake(), beads.NewMemStore(), nil,
			newSessionBeadSnapshot([]beads.Bead{heldSeat(until)}),
			nil, &stderr,
		)
		n := 0
		for _, tp := range result.State {
			if tp.TemplateName == "worker" {
				n++
			}
		}
		return n, stderr.String()
	}

	if n, stderr := workers(now.Add(8 * time.Hour)); n != 1 {
		t.Fatalf("worker entries = %d while claude is held, want 1 (the held seat reused, no fresh creates); stderr=%s", n, stderr)
	}
	if n, stderr := workers(now.Add(-time.Minute)); n < 2 {
		t.Fatalf("worker entries = %d after the reset, want fresh creates again; stderr=%s", n, stderr)
	}
}
