//go:build gascity_native_beads

package beads

import (
	"testing"
	"time"
)

// TestDoltliteReadStoreExternalRef pins the doltlite read path for bd's
// top-level external_ref column: it is projected onto Bead.ExternalRef and a
// ListQuery.ExternalRef filter is an exact SQL match, safe under a limit.
func TestDoltliteReadStoreExternalRef(t *testing.T) {
	store, closeStore := newTestDoltliteReadStore(t)
	defer closeStore()
	writer := openTestDoltliteWriter(t, store.db)
	defer writer.Close() //nolint:errcheck // test cleanup

	if _, err := writer.Exec(`ALTER TABLE issues ADD COLUMN external_ref TEXT`); err != nil {
		t.Fatalf("add external_ref column: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	insert := func(id string, offset time.Duration, ref any) {
		t.Helper()
		ts := base.Add(offset).Format(time.RFC3339Nano)
		if _, err := writer.Exec(`
			INSERT INTO issues (
				id, title, status, issue_type, priority, created_at, updated_at,
				assignee, description, design, acceptance_criteria, notes, metadata, external_ref
			)
			VALUES (?, ?, 'open', 'task', 2, ?, ?, '', '', '', '', '', '{"external_ref":"2013950"}', ?)
		`, id, id, ts, ts, ref); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// The match is the OLDEST row, so a created-desc LIMIT 1 taken before the
	// filter would miss it.
	insert("gc-ext-match", 0, "2013950")
	insert("gc-ext-other", time.Minute, "2013951")
	insert("gc-ext-null", 2*time.Minute, nil)

	got, err := store.Get("gc-ext-match")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ExternalRef != "2013950" {
		t.Fatalf("Get ExternalRef = %q, want 2013950", got.ExternalRef)
	}
	null, err := store.Get("gc-ext-null")
	if err != nil {
		t.Fatalf("Get null: %v", err)
	}
	if null.ExternalRef != "" {
		t.Fatalf("NULL external_ref = %q, want empty", null.ExternalRef)
	}

	for _, q := range []ListQuery{
		{ExternalRef: "2013950"},
		{ExternalRef: "2013950", Limit: 1, Sort: SortCreatedDesc},
		{ExternalRef: "2013950", TierMode: TierBoth, IncludeClosed: true, Limit: 1, Sort: SortCreatedDesc},
	} {
		rows, err := store.List(q)
		if err != nil {
			t.Fatalf("List(%+v): %v", q, err)
		}
		if len(rows) != 1 || rows[0].ID != "gc-ext-match" {
			t.Fatalf("List(%+v) = %#v, want only gc-ext-match (top-level column only, not metadata)", q, rows)
		}
	}
}

// TestDoltliteReadStoreExternalRefWithoutColumn covers a snapshot whose issues
// table predates external_ref: reads keep working, Bead.ExternalRef is empty,
// and an external_ref filter matches nothing rather than erroring.
func TestDoltliteReadStoreExternalRefWithoutColumn(t *testing.T) {
	store, closeStore := newTestDoltliteReadStore(t)
	defer closeStore()

	rows, err := store.List(ListQuery{ExternalRef: "2013950", IncludeClosed: true, TierMode: TierBoth})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("List(ExternalRef) on a column-less snapshot = %#v, want none", rows)
	}
	all, err := store.List(ListQuery{AllowScan: true})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("column-less snapshot lost its rows")
	}
}
