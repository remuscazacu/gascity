package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/formula"
)

var _ api.FormulaCooker = (*controllerState)(nil)

// CookFormula serves POST /v0/city/{city}/formulas/{name}/cook: the HTTP form
// of `gc formula cook <name> [--rig R] [--title T] [--var k=v ...]` without
// --attach or --meta. It runs the CLI's own standalone arm
// (cookFormulaStandalone) with the scope the CLI's explicit --rig (or explicit
// --city, for an empty rig) resolves: the rig's store as the work store, the
// rig's formula layers as search paths, and resolveGraphStore over that store
// as the graph store. Nothing is routed, promoted, attached, or poked.
//
// The one deliberate difference from the CLI is the store HANDLE: the CLI
// opens a fresh store at the scope root, while this uses the controller's
// long-lived (caching) handle for the same scope, so the API's own reads see
// the new beads immediately. Both persist to the same ledger.
func (cs *controllerState) CookFormula(ctx context.Context, req api.FormulaCookRequest) (api.FormulaCookResult, error) {
	cfg := cs.Config()
	if cfg == nil {
		return api.FormulaCookResult{}, errors.New("formula cook: city config unavailable")
	}
	cityPath := cs.CityPath()

	var (
		scope formulaScope
		store beads.Store
	)
	if rigName := strings.TrimSpace(req.Rig); rigName != "" {
		rig, ok := rigByName(cfg, rigName)
		if !ok {
			return api.FormulaCookResult{}, fmt.Errorf("rig %q not found: %w", rigName, api.ErrFormulaCookNotFound)
		}
		if strings.TrimSpace(rig.Path) == "" {
			return api.FormulaCookResult{}, fmt.Errorf("rig %q is declared but has no path binding: %w", rig.Name, api.ErrFormulaCookNotFound)
		}
		scope = rigFormulaScope(cfg, cityPath, rig)
		store = cs.BeadStore(rig.Name)
	} else {
		scope = formulaScope{storeRoot: cityPath, searchPaths: cfg.FormulaLayers.City}
		store = cs.CityBeadStore()
	}
	if store == nil {
		return api.FormulaCookResult{}, fmt.Errorf("formula cook: no bead store for scope %q", scope.storeRoot)
	}
	graphStore := resolveGraphStore(cliStorageRoutes(cityPath), store, cfg, cityPath, nil)

	cooked, err := cookFormulaStandalone(ctx, cfg, cityPath, scope, store, graphStore, req.Name, req.Title, maps.Clone(req.Vars), os.Stderr)
	if err != nil {
		return api.FormulaCookResult{}, formulaCookAPIError(err)
	}
	cooked.release()
	return api.FormulaCookResult{
		RootID:    cooked.result.RootID,
		Created:   cooked.result.Created,
		IDMapping: cooked.result.IDMapping,
	}, nil
}

// formulaCookAPIError classifies a standalone-cook failure for the HTTP
// layer: a formula that cannot be loaded is not-found, a variable the
// invocation got wrong is invalid, everything else stays internal.
func formulaCookAPIError(err error) error {
	var stageErr *formulaCookStageError
	if errors.As(err, &stageErr) {
		switch stageErr.stage {
		case formulaCookStageLoad:
			return fmt.Errorf("%w: %w", api.ErrFormulaCookNotFound, err)
		case formulaCookStageInvalid:
			return fmt.Errorf("%w: %w", api.ErrFormulaCookInvalid, err)
		}
	}
	if errors.Is(err, formula.ErrVarValidation) {
		return fmt.Errorf("%w: %w", api.ErrFormulaCookInvalid, err)
	}
	return err
}
