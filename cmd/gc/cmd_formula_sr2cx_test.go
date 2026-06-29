package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/graphroute"
)

// TestFormulaCookStandaloneGraphV2RoutesControlBeadToDispatcher is the red->green
// test for the standalone `gc formula cook` half of sr-2cx. A standalone cook
// (no --attach) is exactly what the intake-poller runs to create the
// ticket-lifecycle molecule. molecule.Cook does compile+Instantiate with NO
// routing decoration, so the cooked molecule was UNDECORATED: its control bead
// (workflow-finalize) was not routed to the control-dispatcher, so the
// dispatcher never got demand, never woke, and the molecule never drove.
//
// The fix inlines compile->decorate->Instantiate in the standalone cook path.
// Assertions are on the MATERIALIZED (stored) beads, since instantiation
// defers-and-restores gc.routed_to.
func TestFormulaCookStandaloneGraphV2RoutesControlBeadToDispatcher(t *testing.T) {
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
formula  = "graph-cook-nopool"
version  = 2
contract = "graph.v2"

[[steps]]
id    = "poll"
title = "Poll step"
metadata = { "gc.run_target" = "worker" }
`
	if err := os.WriteFile(filepath.Join(formulaDir, "graph-cook-nopool.toml"), []byte(graphFormula), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--city", cityDir, "formula", "cook", "graph-cook-nopool"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("gc formula cook = %d, want 0; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}

	store, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
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
				t.Errorf("standalone-cook root %s gc.routed_to = %q, want empty (unrouted container)", b.ID, routedTo)
			}
		case graphroute.IsControlDispatcherKind(kind):
			sawControl = true
			if routedTo != config.ControlDispatcherAgentName {
				t.Errorf("standalone-cook control bead %s (kind=%s) gc.routed_to = %q, want %q (sr-2cx: dispatcher never wakes without this)",
					b.ID, kind, routedTo, config.ControlDispatcherAgentName)
			}
		case b.Metadata["gc.run_target"] != "":
			sawWorker = true
			if routedTo != "worker" {
				t.Errorf("standalone-cook worker step %s gc.routed_to = %q, want %q (run_target fallback under empty default route)",
					b.ID, routedTo, "worker")
			}
		}
	}
	if !sawRoot {
		t.Error("no workflow root bead found")
	}
	if !sawControl {
		t.Fatal("no control-dispatcher bead found in the cooked molecule")
	}
	if !sawWorker {
		t.Fatal("no worker step found in the cooked molecule")
	}
}
