package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeCookerState is a fakeState that also implements FormulaCooker, recording
// every request it receives and answering with a canned result or error.
type fakeCookerState struct {
	*fakeState
	requests []FormulaCookRequest
	result   FormulaCookResult
	err      error
}

func (f *fakeCookerState) CookFormula(_ context.Context, req FormulaCookRequest) (FormulaCookResult, error) {
	f.requests = append(f.requests, req)
	return f.result, f.err
}

func TestFormulaCookEndpointForwardsRequestAndReturnsRoot(t *testing.T) {
	state := &fakeCookerState{
		fakeState: newFakeState(t),
		result: FormulaCookResult{
			RootID:    "st-root1",
			Created:   3,
			IDMapping: map[string]string{"intake": "st-step1"},
		},
	}
	h := newTestCityHandler(t, state)

	body := `{"rig":"myrig","vars":{"bead":"st-abc","family":"erp"},"title":"lifecycle st-abc"}`
	req := newPostRequest(cityURL(state, "/formulas/ticket-lifecycle/cook"), bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("cook status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}
	if len(state.requests) != 1 {
		t.Fatalf("CookFormula calls = %d, want 1", len(state.requests))
	}
	got := state.requests[0]
	if got.Name != "ticket-lifecycle" || got.Rig != "myrig" || got.Title != "lifecycle st-abc" ||
		got.Vars["bead"] != "st-abc" || got.Vars["family"] != "erp" || len(got.Vars) != 2 {
		t.Fatalf("CookFormula request = %+v", got)
	}
	var resp struct {
		RootID    string            `json:"root_id"`
		Created   int               `json:"created"`
		IDMapping map[string]string `json:"id_mapping"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.RootID != "st-root1" || resp.Created != 3 || resp.IDMapping["intake"] != "st-step1" {
		t.Fatalf("cook response = %+v", resp)
	}
}

func TestFormulaCookEndpointMapsErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", fmt.Errorf("rig %q: %w", "nope", ErrFormulaCookNotFound), http.StatusNotFound},
		{"invalid", fmt.Errorf("missing var: %w", ErrFormulaCookInvalid), http.StatusBadRequest},
		{"internal", fmt.Errorf("store down"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &fakeCookerState{fakeState: newFakeState(t), err: tc.err}
			h := newTestCityHandler(t, state)
			req := newPostRequest(cityURL(state, "/formulas/ticket-lifecycle/cook"), bytes.NewBufferString(`{"rig":"myrig"}`))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestFormulaCookEndpointNotImplementedWithoutCooker(t *testing.T) {
	state := newFakeState(t)
	h := newTestCityHandler(t, state)
	req := newPostRequest(cityURL(state, "/formulas/ticket-lifecycle/cook"), bytes.NewBufferString(`{"rig":"myrig"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501, body: %s", rec.Code, rec.Body.String())
	}
}

func TestFormulaCookEndpointIdempotencyKeyReplays(t *testing.T) {
	state := &fakeCookerState{
		fakeState: newFakeState(t),
		result:    FormulaCookResult{RootID: "st-root1", Created: 2},
	}
	h := newTestCityHandler(t, state)
	post := func(body string) *httptest.ResponseRecorder {
		req := newPostRequest(cityURL(state, "/formulas/ticket-lifecycle/cook"), bytes.NewBufferString(body))
		req.Header.Set("Idempotency-Key", "cook-st-abc")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	body := `{"rig":"myrig","vars":{"bead":"st-abc"}}`
	first, second := post(body), post(body)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("statuses = %d, %d, want 201, 201; bodies %s / %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if len(state.requests) != 1 {
		t.Fatalf("CookFormula calls = %d, want 1 (retry with the same key must replay)", len(state.requests))
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replay body %s != first body %s", second.Body.String(), first.Body.String())
	}
	if mismatch := post(`{"rig":"myrig","vars":{"bead":"st-other"}}`); mismatch.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key, different body: status = %d, want 422", mismatch.Code)
	}
}
