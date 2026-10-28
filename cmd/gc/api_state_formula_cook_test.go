package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/formulatest"
	"github.com/gastownhall/gascity/internal/runtime"
)

// setupFormulaCookParityCity writes a single-store city with one rig and a
// graph.v2 formula whose root takes a required var, and makes the process
// environment the hermetic one the CLI cook tests use.
func setupFormulaCookParityCity(t *testing.T) (cityDir, rigDir string) {
	t.Helper()

	cityDir = t.TempDir()
	rigDir = filepath.Join(cityDir, "myrig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	cityTOML := withBuiltinProviderAliasesTOMLForTest(`
[workspace]
name = "my-city"
provider = "claude"

[daemon]
formula_v2 = true
`, "claude") + testControlDispatcherAgentTOML("myrig") + fmt.Sprintf("\n[[rigs]]\nname = \"myrig\"\npath = %q\n", rigDir)
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityTOML), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	formulaDir := filepath.Join(cityDir, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatalf("mkdir formulas: %v", err)
	}
	if err := os.WriteFile(filepath.Join(formulaDir, "graph-work.formula.toml"), []byte(`
formula = "graph-work"
version = 2
contract = "graph.v2"

[vars.bead]
required = true

[[steps]]
id = "step"
title = "Work {{bead}}"
`), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	// Scoped file stores, so the rig and the city each have their own ledger
	// and "landed in the rig store" is observable (the legacy file layout
	// aliases every scope to one file).
	if err := ensureScopedFileStoreLayout(cityDir); err != nil {
		t.Fatalf("scoped file store layout: %v", err)
	}
	for _, dir := range []string{cityDir, rigDir} {
		if err := ensurePersistedScopeLocalFileStore(dir); err != nil {
			t.Fatalf("init file store %s: %v", dir, err)
		}
	}
	formulatest.SetupHermeticCookEnv(t, rigDir, cityDir)
	return cityDir, rigDir
}

func newFormulaCookParityControllerState(t *testing.T, cityDir string) *controllerState {
	t.Helper()
	cfg, err := loadCityConfig(cityDir, io.Discard)
	if err != nil {
		t.Fatalf("load city config: %v", err)
	}
	cs := newControllerState(context.Background(), cfg, runtime.NewFake(), events.NewFake(), "my-city", cityDir)
	if cs.BeadStore("myrig") == nil {
		t.Fatal("controller state has no store for rig myrig")
	}
	return cs
}

// cookParityView is the part of a cooked bead that must be identical between
// the CLI and the API: everything except ids and timestamps.
func cookParityView(t *testing.T, store beads.Store, id string, idToStep map[string]string) string {
	t.Helper()
	b, err := store.Get(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	meta := map[string]string{}
	for k, v := range b.Metadata {
		if mapped, ok := idToStep[v]; ok {
			v = "<" + mapped + ">"
		}
		meta[k] = v
	}
	parent := b.ParentID
	if mapped, ok := idToStep[parent]; ok {
		parent = "<" + mapped + ">"
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	fmt.Fprintf(&sb, "title=%q status=%s type=%s assignee=%q parent=%s labels=%v\n", b.Title, b.Status, b.Type, b.Assignee, parent, b.Labels)
	for _, k := range keys {
		fmt.Fprintf(&sb, "  %s=%q\n", k, meta[k])
	}
	return sb.String()
}

// TestControllerStateCookFormulaMatchesCLIStandaloneCook pins the API cook
// endpoint's backing to the CLI: `gc formula cook graph-work --var bead=X` in a
// rig scope and CookFormula{Rig: myrig, Vars: {bead: X}} must create the same
// molecule in the same (rig) store — same root metadata (gc.root_store_ref,
// gc.scope_kind, no gc.routed_to), same status, same step set.
func TestControllerStateCookFormulaMatchesCLIStandaloneCook(t *testing.T) {
	cityDir, rigDir := setupFormulaCookParityCity(t)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaCookCmd(&stdout, &stderr)
	cmd.SetArgs([]string{"graph-work", "--var", "bead=st-abc", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("formula cook: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var cli formulaCookJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &cli); err != nil {
		t.Fatalf("parse cook json %q: %v", stdout.String(), err)
	}

	cs := newFormulaCookParityControllerState(t, cityDir)
	got, err := cs.CookFormula(context.Background(), api.FormulaCookRequest{
		Name: "graph-work",
		Rig:  "myrig",
		Vars: map[string]string{"bead": "st-abc"},
	})
	if err != nil {
		t.Fatalf("CookFormula: %v", err)
	}
	if got.RootID == "" || got.RootID == cli.RootID {
		t.Fatalf("CookFormula root = %q (CLI root %q), want a fresh root", got.RootID, cli.RootID)
	}
	if got.Created != cli.Created {
		t.Fatalf("CookFormula created = %d, CLI created = %d", got.Created, cli.Created)
	}

	// Read both molecules from a fresh handle on the rig store: both must be
	// there, i.e. the API cook landed in the rig store, not the city store.
	rigStore, err := openStoreAtForCity(rigDir, cityDir)
	if err != nil {
		t.Fatalf("open rig store: %v", err)
	}
	cliMap := map[string]string{cli.RootID: "root"}
	for step, id := range cli.IDMapping {
		cliMap[id] = step
	}
	apiMap := map[string]string{got.RootID: "root"}
	for step, id := range got.IDMapping {
		apiMap[id] = step
	}
	if want, have := cookParityView(t, rigStore, cli.RootID, cliMap), cookParityView(t, rigStore, got.RootID, apiMap); want != have {
		t.Fatalf("API root differs from CLI root:\nCLI:\n%s\nAPI:\n%s", want, have)
	}
	if len(cli.IDMapping) != len(got.IDMapping) {
		t.Fatalf("step mapping: CLI %v, API %v", cli.IDMapping, got.IDMapping)
	}
	for step, cliID := range cli.IDMapping {
		apiID, ok := got.IDMapping[step]
		if !ok {
			t.Fatalf("API cook missing step %q (CLI %v, API %v)", step, cli.IDMapping, got.IDMapping)
		}
		if want, have := cookParityView(t, rigStore, cliID, cliMap), cookParityView(t, rigStore, apiID, apiMap); want != have {
			t.Fatalf("step %q differs:\nCLI:\n%s\nAPI:\n%s", step, want, have)
		}
	}

	root, err := rigStore.Get(got.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Status != "open" {
		t.Fatalf("API root status = %q, want open (cook never promotes)", root.Status)
	}
	if v := root.Metadata[beadmeta.RoutedToMetadataKey]; v != "" {
		t.Fatalf("API root gc.routed_to = %q, want empty (cook never routes the root)", v)
	}
	if v := root.Metadata[beadmeta.RootStoreRefMetadataKey]; v != "rig:myrig" {
		t.Fatalf("API root gc.root_store_ref = %q, want rig:myrig", v)
	}
	if cityStore := cs.CityBeadStore(); cityStore != nil {
		if _, err := cityStore.Get(got.RootID); err == nil {
			t.Fatalf("API root %s is readable from the city store; want it only in the rig store", got.RootID)
		}
	}
}

func TestControllerStateCookFormulaClassifiesErrors(t *testing.T) {
	cityDir, _ := setupFormulaCookParityCity(t)
	cs := newFormulaCookParityControllerState(t, cityDir)

	_, err := cs.CookFormula(context.Background(), api.FormulaCookRequest{Name: "graph-work", Rig: "nope", Vars: map[string]string{"bead": "x"}})
	if !errors.Is(err, api.ErrFormulaCookNotFound) {
		t.Fatalf("unknown rig: err = %v, want ErrFormulaCookNotFound", err)
	}
	_, err = cs.CookFormula(context.Background(), api.FormulaCookRequest{Name: "no-such-formula", Rig: "myrig"})
	if !errors.Is(err, api.ErrFormulaCookNotFound) {
		t.Fatalf("unknown formula: err = %v, want ErrFormulaCookNotFound", err)
	}
	_, err = cs.CookFormula(context.Background(), api.FormulaCookRequest{Name: "graph-work", Rig: "myrig"})
	if !errors.Is(err, api.ErrFormulaCookInvalid) {
		t.Fatalf("missing required var: err = %v, want ErrFormulaCookInvalid", err)
	}
}
