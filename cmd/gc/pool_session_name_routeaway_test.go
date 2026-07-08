package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// TestReleaseOrphanedPoolAssignments_ReleasesRoutedAwayFromLiveSourceSession is
// the regression guard for sr-wz8.3. When L1 escalates a ticket it re-routes the
// bead to l2-<family> but the bead stays ASSIGNED to the live L1 source session.
// The live-session ownership guard (openSessionOwnsWork / liveOpenSessionAssignmentExists)
// is meant for DEAD-session orphans and must NOT protect an assignment that has
// been routed AWAY to a different agent than the owning session's own agent —
// otherwise the l2 pool never gains demand and the escalation deadlocks until the
// source session is closed (the live repro's only working recipe).
func TestReleaseOrphanedPoolAssignments_ReleasesRoutedAwayFromLiveSourceSession(t *testing.T) {
	store := beads.NewMemStore()
	l1Session, err := store.Create(beads.Bead{
		Title:  "l1 source",
		Type:   sessionBeadType,
		Status: "open",
		Metadata: map[string]string{
			"session_name":         "l1-live",
			"template":             "l1-support",
			"agent_name":           "l1-support",
			poolManagedMetadataKey: boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatalf("Create l1 session bead: %v", err)
	}
	work, err := store.Create(beads.Bead{
		Title:    "escalated ticket",
		Assignee: "l1-live",
		Metadata: map[string]string{"gc.routed_to": "l2-erp"},
	})
	if err != nil {
		t.Fatalf("Create work bead: %v", err)
	}
	if err := store.Update(work.ID, beads.UpdateOpts{Status: stringPtr("in_progress")}); err != nil {
		t.Fatalf("Set work status: %v", err)
	}
	work, err = store.Get(work.ID)
	if err != nil {
		t.Fatalf("Reload work bead: %v", err)
	}

	released := releaseOrphanedPoolAssignments(
		store,
		&config.City{Agents: []config.Agent{
			{Name: "l1-support", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(5)},
			{Name: "l2-erp", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(2)},
		}},
		"",
		[]beads.Bead{l1Session},
		[]beads.Bead{work},
		nil,
		nil,
		nil,
	)
	if len(released) != 1 || released[0].ID != work.ID {
		t.Fatalf("released = %v, want [%s] (routed-away bead must be released even though the L1 source session is live)", released, work.ID)
	}

	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("Get work bead: %v", err)
	}
	if got.Status != "open" {
		t.Fatalf("status = %q, want open", got.Status)
	}
	if got.Assignee != "" {
		t.Fatalf("assignee = %q, want empty (released so l2-erp pool can claim)", got.Assignee)
	}
}

// TestReleaseOrphanedPoolAssignments_KeepsRoutedToOwnAgentLiveSession guards that
// the sr-wz8.3 route-away release does NOT steal a bead a live session legitimately
// owns: when routed_to points at the owning session's OWN agent, the live-session
// ownership guard must still keep the assignment untouched.
func TestReleaseOrphanedPoolAssignments_KeepsRoutedToOwnAgentLiveSession(t *testing.T) {
	store := beads.NewMemStore()
	session, err := store.Create(beads.Bead{
		Title:  "l2 owner",
		Type:   sessionBeadType,
		Status: "open",
		Metadata: map[string]string{
			"session_name":         "l2-live",
			"template":             "l2-erp",
			"agent_name":           "l2-erp",
			poolManagedMetadataKey: boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	work, err := store.Create(beads.Bead{
		Title:    "l2 in-progress work",
		Assignee: "l2-live",
		Metadata: map[string]string{"gc.routed_to": "l2-erp"},
	})
	if err != nil {
		t.Fatalf("Create work bead: %v", err)
	}
	if err := store.Update(work.ID, beads.UpdateOpts{Status: stringPtr("in_progress")}); err != nil {
		t.Fatalf("Set work status: %v", err)
	}
	work, err = store.Get(work.ID)
	if err != nil {
		t.Fatalf("Reload work bead: %v", err)
	}

	released := releaseOrphanedPoolAssignments(
		store,
		&config.City{Agents: []config.Agent{
			{Name: "l1-support", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(5)},
			{Name: "l2-erp", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(2)},
		}},
		"",
		[]beads.Bead{session},
		[]beads.Bead{work},
		nil,
		nil,
		nil,
	)
	if len(released) != 0 {
		t.Fatalf("released = %v, want none (live session owns work routed to its own agent)", released)
	}
	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("Get work bead: %v", err)
	}
	if got.Assignee != "l2-live" {
		t.Fatalf("assignee = %q, want l2-live (must not steal legitimately-owned work)", got.Assignee)
	}
}

// qualifiedSupportAgents mirrors the real srv fleet naming: rig-scoped,
// pack-bound agents whose QualifiedName() is the dotted "st/support.<name>"
// form (Dir="st", BindingName="support"). The flat-name tests above cannot
// catch a divergence between the routed-target lookup (findAgentByTemplate on
// gc.routed_to) and the owning-session lookup (normalizedSessionTemplate on the
// session bead's template metadata) under this production naming — these two
// tests do (sr-wz8.3 guard #1: never over-release a legitimately-owned bead).
func qualifiedSupportAgents() []config.Agent {
	return []config.Agent{
		{Name: "l1-support", Dir: "st", BindingName: "support", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(5)},
		{Name: "l2-erp", Dir: "st", BindingName: "support", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(2)},
	}
}

func TestReleaseOrphanedPoolAssignments_KeepsQualifiedOwnAgentLiveSession(t *testing.T) {
	store := beads.NewMemStore()
	session, err := store.Create(beads.Bead{
		Title:  "l2 owner (qualified)",
		Type:   sessionBeadType,
		Status: "open",
		Metadata: map[string]string{
			"session_name":         "l2-erp-live",
			"template":             "st/support.l2-erp",
			"agent_name":           "st/support.l2-erp",
			poolManagedMetadataKey: boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	work, err := store.Create(beads.Bead{
		Title:    "l2 in-progress work (qualified)",
		Assignee: "l2-erp-live",
		Metadata: map[string]string{"gc.routed_to": "st/support.l2-erp"},
	})
	if err != nil {
		t.Fatalf("Create work bead: %v", err)
	}
	if err := store.Update(work.ID, beads.UpdateOpts{Status: stringPtr("in_progress")}); err != nil {
		t.Fatalf("Set work status: %v", err)
	}
	work, err = store.Get(work.ID)
	if err != nil {
		t.Fatalf("Reload work bead: %v", err)
	}

	released := releaseOrphanedPoolAssignments(
		store,
		&config.City{Agents: qualifiedSupportAgents()},
		"",
		[]beads.Bead{session},
		[]beads.Bead{work},
		nil,
		nil,
		nil,
	)
	if len(released) != 0 {
		t.Fatalf("released = %v, want none (live session owns work routed to its OWN qualified agent st/support.l2-erp — must not over-release)", released)
	}
	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("Get work bead: %v", err)
	}
	if got.Assignee != "l2-erp-live" {
		t.Fatalf("assignee = %q, want l2-erp-live", got.Assignee)
	}
}

func TestReleaseOrphanedPoolAssignments_ReleasesQualifiedRoutedAwayFromLiveSource(t *testing.T) {
	store := beads.NewMemStore()
	l1Session, err := store.Create(beads.Bead{
		Title:  "l1 source (qualified)",
		Type:   sessionBeadType,
		Status: "open",
		Metadata: map[string]string{
			"session_name":         "l1-support-live",
			"template":             "st/support.l1-support",
			"agent_name":           "st/support.l1-support",
			poolManagedMetadataKey: boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatalf("Create l1 session bead: %v", err)
	}
	work, err := store.Create(beads.Bead{
		Title:    "escalated ticket (qualified)",
		Assignee: "l1-support-live",
		Metadata: map[string]string{"gc.routed_to": "st/support.l2-erp"},
	})
	if err != nil {
		t.Fatalf("Create work bead: %v", err)
	}
	if err := store.Update(work.ID, beads.UpdateOpts{Status: stringPtr("in_progress")}); err != nil {
		t.Fatalf("Set work status: %v", err)
	}
	work, err = store.Get(work.ID)
	if err != nil {
		t.Fatalf("Reload work bead: %v", err)
	}

	released := releaseOrphanedPoolAssignments(
		store,
		&config.City{Agents: qualifiedSupportAgents()},
		"",
		[]beads.Bead{l1Session},
		[]beads.Bead{work},
		nil,
		nil,
		nil,
	)
	if len(released) != 1 || released[0].ID != work.ID {
		t.Fatalf("released = %v, want [%s] (bead routed away to st/support.l2-erp while assigned to live st/support.l1-support source must release)", released, work.ID)
	}
	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("Get work bead: %v", err)
	}
	if got.Assignee != "" {
		t.Fatalf("assignee = %q, want empty", got.Assignee)
	}
}
