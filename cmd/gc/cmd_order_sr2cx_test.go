package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/graphroute"
	"github.com/gastownhall/gascity/internal/orders"
)

// TestOrderRunNonPoolGraphV2RoutesControlBeadToDispatcher is the red->green test
// for the `gc order run` half of sr-2cx. This is the SAME entry point as the
// manual live action (doOrderRunWithJSON) — distinct from the controller's
// dispatchWisp path — so it catches the regression dispatchWisp's test would miss.
//
// doOrderRunWithJSON gated routing decoration behind `a.Pool != ""`, so a non-pool
// order instantiated an UNDECORATED molecule: no control bead routed to the
// control-dispatcher -> the dispatcher never woke -> the molecule never drove.
//
// Assertions are on the MATERIALIZED beads (through Instantiate's defer/restore).
func TestOrderRunNonPoolGraphV2RoutesControlBeadToDispatcher(t *testing.T) {
	configureIsolatedRuntimeEnv(t)
	t.Setenv("GC_SESSION", "fake")
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_DOLT", "skip")
	t.Setenv("GC_BOOTSTRAP", "skip")
	t.Cleanup(func() {
		applyFeatureFlags(&config.City{Daemon: config.DaemonConfig{FormulaV2: boolPtr(true)}})
	})

	cityDir := t.TempDir()
	formulaDir := filepath.Join(cityDir, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cityToml := `[workspace]
name = "test-city"

[daemon]
formula_v2 = true

[[agent]]
name = "worker"
max_active_sessions = 1

[[agent]]
name = "control-dispatcher"
start_command = "gc convoy control --serve --follow {{.Agent}}"
prompt_mode = "none"
process_names = ["gc"]
max_active_sessions = 1
`
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityToml), 0o644); err != nil {
		t.Fatal(err)
	}
	graphFormula := `
formula  = "test-cli-intake"
version  = 2
contract = "graph.v2"

[[steps]]
id    = "poll"
title = "Poll step"
metadata = { "gc.run_target" = "worker" }
`
	if err := os.WriteFile(filepath.Join(formulaDir, "test-cli-intake.toml"), []byte(graphFormula), 0o644); err != nil {
		t.Fatal(err)
	}

	// No Pool: routing comes from the step's gc.run_target; the order only triggers.
	aa := []orders.Order{{Name: "test-cli-intake", Formula: "test-cli-intake", FormulaLayer: formulaDir}}
	store := beads.NewMemStore()

	var stdout, stderr bytes.Buffer
	code := doOrderRunWithJSON(aa, "test-cli-intake", "", cityDir, store, nil, false, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doOrderRunWithJSON = %d, want 0; stderr: %s", code, stderr.String())
	}

	items, err := store.List(beads.ListQuery{AllowScan: true, IncludeClosed: true, TierMode: beads.TierBoth})
	if err != nil {
		t.Fatalf("store.List(): %v", err)
	}

	var sawRoot, sawControl, sawWorker bool
	for _, b := range items {
		kind := b.Metadata["gc.kind"]
		routedTo := b.Metadata["gc.routed_to"]
		switch {
		case kind == "workflow":
			sawRoot = true
			if routedTo != "" {
				t.Errorf("gc order run root %s gc.routed_to = %q, want empty (unrouted container)", b.ID, routedTo)
			}
		case graphroute.IsControlDispatcherKind(kind):
			sawControl = true
			if routedTo != config.ControlDispatcherAgentName {
				t.Errorf("gc order run control bead %s (kind=%s) gc.routed_to = %q, want %q (sr-2cx: dispatcher never wakes without this)",
					b.ID, kind, routedTo, config.ControlDispatcherAgentName)
			}
		case b.Metadata["gc.run_target"] != "":
			sawWorker = true
			if routedTo != "worker" {
				t.Errorf("gc order run worker step %s gc.routed_to = %q, want %q (run_target fallback under empty default route)",
					b.ID, routedTo, "worker")
			}
		}
	}
	if !sawRoot {
		t.Error("no workflow root bead found")
	}
	if !sawControl {
		t.Fatal("no control-dispatcher bead found in the molecule")
	}
	if !sawWorker {
		t.Fatal("no worker step found in the molecule")
	}
}
