package main

import (
	"context"
	"fmt"
	"io"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/formula"
	"github.com/gastownhall/gascity/internal/graphv2"
	"github.com/gastownhall/gascity/internal/molecule"
)

// formulaCookStage names the step of a standalone cook that failed, so the CLI
// keeps its exact error prefixes and the API can map a failure to a status.
type formulaCookStage int

const (
	// formulaCookStageLoad: the formula could not be loaded from the scope's
	// search paths.
	formulaCookStageLoad formulaCookStage = iota
	// formulaCookStageInvalid: the invocation itself is invalid (runtime
	// variable validation).
	formulaCookStageInvalid
	// formulaCookStageCompile: the formula failed to compile.
	formulaCookStageCompile
	// formulaCookStageMaterialize: preparing, decorating or instantiating the
	// molecule failed.
	formulaCookStageMaterialize
)

// formulaCookStageError wraps a standalone-cook failure with its stage.
type formulaCookStageError struct {
	stage formulaCookStage
	err   error
}

func (e *formulaCookStageError) Error() string { return e.err.Error() }
func (e *formulaCookStageError) Unwrap() error { return e.err }

// cliCommand is the command label `gc formula cook` has always printed for a
// failure at this stage.
func (e *formulaCookStageError) cliCommand() string {
	if e.stage == formulaCookStageCompile {
		return "gc formula cook: compile"
	}
	return "gc formula cook"
}

func formulaCookFailure(stage formulaCookStage, err error) error {
	return &formulaCookStageError{stage: stage, err: err}
}

// formulaCookStandaloneResult is the outcome of cookFormulaStandalone.
type formulaCookStandaloneResult struct {
	result *molecule.Result
	// rootStore is the store the cooked root ended up in.
	rootStore beads.Store
	// release drops the graph lock a v2 cook may hold. It is always non-nil;
	// the caller must call it once it is done with the root (the CLI holds it
	// across its --meta stamp).
	release func()
}

// cookFormulaStandalone is the standalone (no --attach) arm of
// `gc formula cook`: compile the named formula in scope and instantiate it as
// real beads, without routing, promoting, attaching, or poking anything. Both
// the CLI and the API's POST /formulas/{name}/cook (controllerState.CookFormula)
// run exactly this sequence, so the two cannot drift.
//
// store is the scope's work store and graphStore what resolveGraphStore
// returns for it; the molecule lands in moleculeClassStore(recipe, store,
// graphStore). Graph.v2 deprecation warnings and execution-fact projection
// warnings go to stderr.
func cookFormulaStandalone(ctx context.Context, cfg *config.City, cityPath string, scope formulaScope, store, graphStore beads.Store, name, title string, cookVars map[string]string, stderr io.Writer) (formulaCookStandaloneResult, error) {
	out := formulaCookStandaloneResult{release: func() {}}
	isGraphFormula, _, err := graphv2.IsGraphV2Formula(name, scope.searchPaths)
	if err != nil {
		return out, formulaCookFailure(formulaCookStageLoad, fmt.Errorf("load formula %q: %w", name, err))
	}
	inv, err := graphv2.PrepareInvocation(ctx, store, name, scope.searchPaths, "", cookVars)
	if err != nil {
		return out, formulaCookFailure(formulaCookStageMaterialize, fmt.Errorf("prepare formulas v2 invocation: %w", err))
	}
	printGraphV2Deprecations(stderr, inv.Deprecations)
	cookVars = inv.Vars

	// rootStore is the store the cooked root ended up in, so a --meta stamp
	// lands on the bead it just created rather than on a store that has never
	// held it.
	out.rootStore = store
	if isGraphFormula {
		// This arm writes molecule.Cook's sequence out rather than calling
		// molecule.CookChoosingStore, and it needs all three capabilities that
		// function's doc comment names as absent: the idempotency key is
		// derived from the compiled recipe (stampFormulaCookGraphV2Root), the
		// graph lock must stay held past the instantiate for the --meta stamp,
		// and the recipe is decorated through the store that will own it.
		//
		// Stamp the run root with its store/scope identity before
		// instantiating, exactly as the --attach branch does via
		// decorateFormulaCookGraphV2Recipe. Without it a standalone-cooked
		// graph.v2 run root carries no gc.root_store_ref/gc.scope_kind, and the
		// dashboard run-detail projection rejects the whole run with 422
		// invalid_snapshot (sr-xz9f).
		storeRef := workflowStoreRefForDir(scope.storeRoot, cityPath, loadedCityName(cfg, cityPath), cfg)
		recipe, err := formula.CompileWithoutRuntimeVarValidation(ctx, name, scope.searchPaths, cookVars)
		if err != nil {
			return out, formulaCookFailure(formulaCookStageCompile, err)
		}
		if err := molecule.ValidateRecipeRuntimeVars(recipe, molecule.Options{Title: title, Vars: cookVars}); err != nil {
			return out, formulaCookFailure(formulaCookStageInvalid, err)
		}
		// One rule for both arms: the classifier picks the store, and it
		// answers ClassGraph for every graph.v2 pour (the compiler stamps
		// gc.kind=workflow on the root). The recipe is decorated through the
		// store that will own it, so the root's identity metadata is written
		// where the root lands.
		out.rootStore = moleculeClassStore(recipe, store, graphStore)
		graphRootKey := stampFormulaCookGraphV2Root(recipe, name, inv.InputConvoy, cookVars)
		if err := decorateFormulaCookGraphV2Recipe(recipe, cookVars, storeRef, scope.rig, out.rootStore, loadedCityName(cfg, cityPath), cityPath, cfg); err != nil {
			return out, formulaCookFailure(formulaCookStageMaterialize, fmt.Errorf("decorate formulas v2 recipe: %w", err))
		}
		if graphRootKey != "" {
			out.release = graphv2.LockKey(graphRootKey)
		}
		result, err := molecule.Instantiate(ctx, out.rootStore, recipe, molecule.Options{
			Title:          title,
			Vars:           cookVars,
			IdempotencyKey: graphRootKey,
		})
		if err != nil {
			out.release()
			out.release = func() {}
			return out, formulaCookFailure(formulaCookStageMaterialize, err)
		}
		out.result = result
		emitFormulaCookExecutionFacts(out.rootStore, store, cityPath, result, stderr)
		return out, nil
	}
	// The legacy compiler still emits a graph-class root for a root-only
	// formula: `phase = "vapor"` (or no [[steps]]) compiles to a root stamped
	// gc.kind=wisp, which is coordclass.Classify's first arm. Asking the
	// classifier instead of asking which compiler ran is what keeps such a
	// wisp out of the work ledger, where it would read as a stranded
	// infrastructure bead and stop boot. A v1 POURED molecule classifies as
	// work and stays exactly where it always was.
	opts := molecule.Options{Title: title, Vars: cookVars}
	result, rootStore, err := molecule.CookChoosingStore(ctx, name, scope.searchPaths, opts, func(recipe *formula.Recipe) beads.Store {
		return moleculeClassStore(recipe, store, graphStore)
	})
	if err != nil {
		return out, formulaCookFailure(formulaCookStageMaterialize, err)
	}
	out.result = result
	out.rootStore = rootStore
	return out, nil
}
