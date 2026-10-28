package api

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gastownhall/gascity/internal/api/apierr"
)

// FormulaCookInput is the Huma input for POST /v0/city/{cityName}/formulas/{name}/cook.
type FormulaCookInput struct {
	CityScope
	Name           string `path:"name" minLength:"1" pattern:"\\S" doc:"Formula name."`
	IdempotencyKey string `header:"Idempotency-Key" required:"false" doc:"Idempotency key for safe retries: a repeat with the same key and body replays the first cook's result instead of cooking again."`
	Body           struct {
		Rig   string            `json:"rig,omitempty" doc:"Rig whose store receives the molecule (the CLI's --rig). Empty means city scope."`
		Vars  map[string]string `json:"vars,omitempty" doc:"Formula variables (the CLI's repeated --var key=value)."`
		Title string            `json:"title,omitempty" doc:"Override the root bead title (the CLI's --title)."`
	}
}

// FormulaCookOutput is the 201 response for a standalone cook.
type FormulaCookOutput struct {
	Body struct {
		RootID    string            `json:"root_id" doc:"ID of the created molecule root."`
		Created   int               `json:"created" doc:"Number of beads created."`
		IDMapping map[string]string `json:"id_mapping,omitempty" doc:"Formula step ID to created bead ID."`
	}
}

// humaHandleFormulaCook instantiates a formula as a standalone molecule: the
// HTTP equivalent of `gc formula cook <name> [--rig R] [--title T] [--var ...]`
// without --attach or --meta. It does not route, promote, attach, or poke, and
// it does not de-duplicate by formula variables — a caller that needs
// at-most-once per bead must check for an existing root first, as the CLI's
// callers do. An Idempotency-Key only makes a retry of the SAME request safe.
func (s *Server) humaHandleFormulaCook(ctx context.Context, input *FormulaCookInput) (*FormulaCookOutput, error) {
	cooker, ok := s.state.(FormulaCooker)
	if !ok {
		return nil, errMutationsNotSupported
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, apierr.InvalidRequest.Msg("formula name is required")
	}
	result, err := withIdempotency(s.idem, "/v0/formulas/"+name+"/cook", input.IdempotencyKey, input.Body,
		func() (FormulaCookResult, error) {
			return cooker.CookFormula(ctx, FormulaCookRequest{
				Name:  name,
				Rig:   strings.TrimSpace(input.Body.Rig),
				Title: input.Body.Title,
				Vars:  input.Body.Vars,
			})
		})
	if err != nil {
		var problem huma.StatusError
		if errors.As(err, &problem) {
			// withIdempotency's own replay/mismatch/in-flight problems.
			return nil, err
		}
		switch {
		case errors.Is(err, ErrFormulaCookNotFound):
			return nil, apierr.FormulaNotFound.Msg(err.Error())
		case errors.Is(err, ErrFormulaCookInvalid):
			return nil, apierr.InvalidRequest.Msg(err.Error())
		default:
			return nil, apierr.Internal.Msg(err.Error())
		}
	}
	out := &FormulaCookOutput{}
	out.Body.RootID = result.RootID
	out.Body.Created = result.Created
	out.Body.IDMapping = result.IDMapping
	return out, nil
}
