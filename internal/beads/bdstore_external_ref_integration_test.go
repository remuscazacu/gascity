//go:build integration

package beads_test

import (
	"os/exec"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestBdStoreExternalRefRoundTripRealBd runs the external_ref wire contract
// the fake-runner TestBdStoreExternalRefRoundTrip assumes against a REAL bd in
// a scope-local embedded database (newConditionalIntegrationBdStore pins
// BEADS_DIR and clears every dolt-server knob, so no live server is touched):
// `bd create --external-ref` persists the top-level column, `bd show --json`
// returns it, and `bd list --external-ref=` is an exact match that ignores
// metadata.external_ref.
func TestBdStoreExternalRefRoundTripRealBd(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skipf("bd not on PATH: %v", err)
	}
	store, _ := newConditionalIntegrationBdStore(t)

	match, err := store.Create(beads.Bead{Title: "top-level ref", ExternalRef: "2013950"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if match.ExternalRef != "2013950" {
		t.Fatalf("create echo ExternalRef = %q, want 2013950", match.ExternalRef)
	}
	if _, err := store.Create(beads.Bead{Title: "other ref", ExternalRef: "2013951"}); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if _, err := store.Create(beads.Bead{Title: "metadata only", Metadata: map[string]string{"external_ref": "2013950"}}); err != nil {
		t.Fatalf("Create metadata-only: %v", err)
	}

	got, err := store.Get(match.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ExternalRef != "2013950" {
		t.Fatalf("Get ExternalRef = %q, want 2013950", got.ExternalRef)
	}

	for _, q := range []beads.ListQuery{
		{ExternalRef: "2013950"},
		{ExternalRef: "2013950", IncludeClosed: true, TierMode: beads.TierBoth},
		{ExternalRef: "2013950", Limit: 1, Sort: beads.SortCreatedDesc},
	} {
		list, err := store.List(q)
		if err != nil {
			t.Fatalf("List(%+v): %v", q, err)
		}
		if len(list) != 1 || list[0].ID != match.ID {
			ids := make([]string, 0, len(list))
			for _, b := range list {
				ids = append(ids, b.ID)
			}
			t.Fatalf("List(%+v) = %v, want exactly [%s]", q, ids, match.ID)
		}
	}
}
