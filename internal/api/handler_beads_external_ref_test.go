package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestBeadGetServesExternalRef(t *testing.T) {
	state := newFakeState(t)
	created, err := state.stores["myrig"].Create(beads.Bead{Title: "mirror", ExternalRef: "2013950"})
	if err != nil {
		t.Fatal(err)
	}
	h := newTestCityHandler(t, state)

	req := httptest.NewRequest("GET", cityURL(state, "/bead/")+created.ID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"external_ref":"2013950"`) {
		t.Fatalf("GET /bead/%s body = %s, want \"external_ref\":\"2013950\"", created.ID, rec.Body.String())
	}
}

func TestBeadCreateAcceptsExternalRef(t *testing.T) {
	state := newFakeState(t)
	h := newTestCityHandler(t, state)

	body := `{"rig":"myrig","title":"mirror","external_ref":"2013950","metadata":{"external_ref":"2013950"}}`
	req := newPostRequest(cityURL(state, "/beads"), bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var created beads.Bead
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ExternalRef != "2013950" {
		t.Fatalf("response ExternalRef = %q, want 2013950", created.ExternalRef)
	}
	stored, err := state.stores["myrig"].Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExternalRef != "2013950" {
		t.Fatalf("stored ExternalRef = %q, want 2013950", stored.ExternalRef)
	}
}

func TestBeadListExternalRefUnion(t *testing.T) {
	state := newFakeState(t)
	store := state.stores["myrig"]
	mustCreate := func(b beads.Bead) beads.Bead {
		t.Helper()
		created, err := store.Create(b)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	a := mustCreate(beads.Bead{Title: "A top-level only", ExternalRef: "2013950"})
	b := mustCreate(beads.Bead{Title: "B metadata only", Metadata: map[string]string{"external_ref": "2013950"}})
	c := mustCreate(beads.Bead{Title: "C both", ExternalRef: "2013950", Metadata: map[string]string{"external_ref": "2013950"}})
	mustCreate(beads.Bead{Title: "D other ticket", ExternalRef: "2013951", Metadata: map[string]string{"external_ref": "2013951"}})
	mustCreate(beads.Bead{Title: "E no ref"})
	if err := store.Close(c.ID); err != nil {
		t.Fatal(err)
	}
	h := newTestCityHandler(t, state)

	list := func(query string) []string {
		t.Helper()
		req := httptest.NewRequest("GET", cityURL(state, "/beads?"+query), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /beads?%s status = %d, body: %s", query, rec.Code, rec.Body.String())
		}
		var resp struct {
			Items []beads.Bead `json:"items"`
			Total int          `json:"total"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(resp.Items))
		for _, item := range resp.Items {
			ids = append(ids, item.ID)
		}
		sort.Strings(ids)
		if resp.Total != len(ids) {
			t.Fatalf("GET /beads?%s total = %d, items = %d", query, resp.Total, len(ids))
		}
		return ids
	}
	sorted := func(ids ...string) []string {
		out := append([]string(nil), ids...)
		sort.Strings(out)
		return out
	}

	got := list("rig=myrig&all=true&external_ref=2013950")
	want := sorted(a.ID, b.ID, c.ID)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("all=true union = %v, want %v (each exactly once)", got, want)
	}

	got = list("rig=myrig&external_ref=2013950")
	want = sorted(a.ID, b.ID)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("open-only union = %v, want %v (closed C excluded without all=true)", got, want)
	}

	got = list("all=true&external_ref=2013950")
	want = sorted(a.ID, b.ID, c.ID)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("all-rigs union = %v, want %v", got, want)
	}
}
