package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/graphroute"
	"github.com/gastownhall/gascity/internal/orders"
)

// TestOrderDispatchNonPoolGraphV2DecoratesAndRoutesControlBead is the red->green
// test for sr-2cx. A graph.v2 order with NO pool must still have its molecule
// decorated at instantiation so the control bead (workflow-finalize) is routed
// to the control-dispatcher — which is what gives the reconciler demand to wake
// it — and worker steps are routed via their own gc.run_target.
//
// Before the fix, dispatchWisp gated routing decoration behind `a.Pool != ""`,
// so a non-pool order (one whose formula routes its steps via gc.run_target and
// whose order "only supplies the trigger", e.g. ticket-intake) instantiated an
// UNDECORATED molecule: no bead routed to the control-dispatcher -> zero
// reconciler demand -> the dispatcher never woke -> the molecule never drove.
//
// The assertions are deliberately on the MATERIALIZED (stored) beads, not the
// recipe: molecule instantiation defers-and-restores gc.routed_to, so a
// recipe-level assertion would bypass that mechanism and could pass while the
// stored beads stayed unrouted.
func TestOrderDispatchNonPoolGraphV2DecoratesAndRoutesControlBead(t *testing.T) {
	formulaDir := t.TempDir()
	writeFile(t, filepath.Join(formulaDir, "test-intake-nopool.toml"), `
formula  = "test-intake-nopool"
version  = 2
contract = "graph.v2"

[[steps]]
id    = "poll"
title = "Poll step"
description = "A single worker step routed via its gc.run_target (the order supplies no pool)."
metadata = { "gc.run_target" = "worker" }
`)

	store := beads.NewMemStore()
	cfg := &config.City{
		Daemon:    config.DaemonConfig{FormulaV2: boolPtr(true)},
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{
			{Name: "worker", MaxActiveSessions: intPtr(1)},
		},
	}
	addTestControlDispatcherAgents(cfg, "")
	applyFeatureFlags(cfg)
	t.Cleanup(func() { applyFeatureFlags(&config.City{}) })

	m := &memoryOrderDispatcher{
		aa: []orders.Order{{
			Name:         "test-intake-nopool",
			Trigger:      "cooldown",
			Interval:     "5m",
			Formula:      "test-intake-nopool",
			FormulaLayer: formulaDir,
			// No Pool on purpose: routing comes from each step's gc.run_target.
		}},
		storeFn: func(_ execStoreTarget) (beads.Store, error) { return store, nil },
		execRun: shellExecRunner,
		rec:     events.Discard,
		stderr:  &bytes.Buffer{},
		cfg:     cfg,
	}

	m.dispatch(context.Background(), t.TempDir(), time.Now())
	m.drain(context.Background())

	root := workBeadByOrderLabel(t, store, "order-run:test-intake-nopool")
	if got := root.Metadata["gc.routed_to"]; got != "" {
		t.Errorf("non-pool workflow root gc.routed_to = %q, want empty (a non-pool root is an unrouted container)", got)
	}

	children, err := store.List(beads.ListQuery{
		Metadata:      map[string]string{"gc.root_bead_id": root.ID},
		IncludeClosed: true,
		TierMode:      beads.TierBoth,
	})
	if err != nil {
		t.Fatalf("List(molecule children): %v", err)
	}

	var sawControl, sawWorker bool
	for _, b := range children {
		kind := b.Metadata["gc.kind"]
		routedTo := b.Metadata["gc.routed_to"]
		switch {
		case graphroute.IsControlDispatcherKind(kind):
			// The fix: control beads must reach the dispatcher so it gets demand.
			sawControl = true
			if routedTo != config.ControlDispatcherAgentName {
				t.Errorf("control bead %s (kind=%s) gc.routed_to = %q, want %q (sr-2cx: dispatcher never wakes without this)",
					b.ID, kind, routedTo, config.ControlDispatcherAgentName)
			}
			if b.Assignee != "" {
				t.Errorf("control bead %s assignee = %q, want empty routed control-dispatcher queue", b.ID, b.Assignee)
			}
		case b.Metadata["gc.run_target"] != "":
			// The inferred-but-untested path: under an empty default route, a
			// worker step must fall back to its own run_target binding.
			sawWorker = true
			if routedTo != "worker" {
				t.Errorf("worker step %s gc.routed_to = %q, want %q (run_target fallback under empty default route)",
					b.ID, routedTo, "worker")
			}
		}
	}
	if !sawControl {
		t.Fatal("no control-dispatcher bead found in the molecule")
	}
	if !sawWorker {
		t.Fatal("no worker step found in the molecule")
	}
}
