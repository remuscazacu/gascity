package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestBuildDoctorChecksRegistersOrderOutcomeHealthy(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	// No GC_DOLT=skip here on purpose. The Dolt-touching checks this assertion
	// must not depend on are suppressed through buildDoctorChecksOpts below,
	// and cfg carries no rigs, so the only gcDoltSkip() reads inside
	// buildDoctorChecks (the per-rig Dolt server/ops registrations) are
	// unreachable. Setting the process environment would add a counted call to
	// the untagged Small cmd/gc environment ledger, whose invariant forbids
	// growth (TESTING.md, CHECKED TEST RESOURCE LEDGER; sr-qppw).
	cfg := &config.City{Workspace: config.Workspace{Name: "demo"}}

	names := doctorCheckNames(buildDoctorChecks(cityDir, cfg, nil, buildDoctorChecksOpts{
		ControllerRunning:    false,
		SkipCityDoltCheck:    true,
		SkipManagedDoltCheck: true,
	}))

	firing := doctorCheckIndex(names, "order-firing-current")
	if firing < 0 {
		t.Fatalf("order-firing-current missing: %v", names)
	}
	outcome := doctorCheckIndex(names, "order-outcome-healthy")
	if outcome != firing+1 {
		t.Fatalf("order-outcome-healthy index = %d, want immediately after order-firing-current at %d; names=%v", outcome, firing, names)
	}
}
