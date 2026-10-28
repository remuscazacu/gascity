//go:build gascity_native_beads

package beads

import (
	"database/sql"
	"slices"
	"strings"
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

// TestDoltliteMetadataFilterPredicatesMatchNumericValues pins the SQL
// pre-filter for a metadata value bd stored as a JSON number: bd's
// --set-metadata coerces digit strings, so live mirrors carry
// "external_ref":2015999. A digit-only filter value matches that form too, in
// every separator/terminator spelling, and never matches a longer number or a
// number that merely starts with it.
func TestDoltliteMetadataFilterPredicatesMatchNumericValues(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	if _, err := db.Exec(`CREATE TABLE rows (id TEXT, metadata TEXT)`); err != nil {
		t.Fatalf("create rows: %v", err)
	}
	for id, md := range map[string]string{
		"quoted":       `{"external_ref":"2015999"}`,
		"int-last":     `{"x":"y","external_ref":2015999}`,
		"int-first":    `{"external_ref":2015999,"x":"y"}`,
		"int-spaced":   `{"external_ref" : 2015999 , "x": "y"}`,
		"int-pretty":   "{\n  \"external_ref\": 2015999\n}",
		"longer":       `{"external_ref":20159990}`,
		"longer-quote": `{"external_ref":"20159990"}`,
		"decimal":      `{"external_ref":2015999.5}`,
		"other-key":    `{"not_external_ref":1,"y":2015999}`,
	} {
		if _, err := db.Exec(`INSERT INTO rows (id, metadata) VALUES (?, ?)`, id, md); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	query := func(value string) []string {
		t.Helper()
		where, args := doltliteMetadataFilterPredicates(map[string]string{"external_ref": value})
		rows, err := db.Query(`SELECT id FROM rows i WHERE `+strings.Join(where, " AND ")+` ORDER BY id`, args...)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close() //nolint:errcheck // test cleanup
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return ids
	}
	if got, want := query("2015999"), []string{"int-first", "int-last", "int-pretty", "int-spaced", "quoted"}; !slices.Equal(got, want) {
		t.Fatalf("numeric value ids = %v, want %v", got, want)
	}
	// A non-digit value keeps the quoted-only match.
	if got := query("201599x"); len(got) != 0 {
		t.Fatalf("non-digit value ids = %v, want none", got)
	}
	// 201599 is a prefix of 2015999 and 20159990: neither may match.
	if got := query("201599"); len(got) != 0 {
		t.Fatalf("prefix value ids = %v, want none", got)
	}
}

// TestDoltliteReadStoreListMetadataIntValue is the end-to-end form: a bead
// whose metadata.external_ref is a JSON number is found by a metadata filter
// on its digit string, including under a limit, and reads back as that string.
func TestDoltliteReadStoreListMetadataIntValue(t *testing.T) {
	store, closeStore := newTestDoltliteReadStore(t)
	defer closeStore()
	writer := openTestDoltliteWriter(t, store.db)
	defer writer.Close() //nolint:errcheck // test cleanup

	base := time.Now().UTC().Add(-time.Hour)
	insert := func(id string, offset time.Duration, metadata string) {
		t.Helper()
		ts := base.Add(offset).Format(time.RFC3339Nano)
		if _, err := writer.Exec(`
			INSERT INTO issues (
				id, title, status, issue_type, priority, created_at, updated_at,
				assignee, description, design, acceptance_criteria, notes, metadata
			)
			VALUES (?, ?, 'open', 'task', 2, ?, ?, '', '', '', '', '', ?)
		`, id, id, ts, ts, metadata); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// The int-valued match is the OLDEST row, so a created-desc LIMIT 1 taken
	// before the filter would miss it.
	insert("gc-meta-int", 0, `{"external_ref":2015999,"mirror":"1"}`)
	insert("gc-meta-longer", time.Minute, `{"external_ref":20159990}`)
	insert("gc-meta-other", 2*time.Minute, `{"external_ref":"2015998"}`)

	for _, q := range []ListQuery{
		{Metadata: map[string]string{"external_ref": "2015999"}},
		{Metadata: map[string]string{"external_ref": "2015999"}, Limit: 1, Sort: SortCreatedDesc},
		{Metadata: map[string]string{"external_ref": "2015999"}, TierMode: TierBoth, IncludeClosed: true, Limit: 1, Sort: SortCreatedDesc},
	} {
		rows, err := store.List(q)
		if err != nil {
			t.Fatalf("List(%+v): %v", q, err)
		}
		if len(rows) != 1 || rows[0].ID != "gc-meta-int" || rows[0].Metadata["external_ref"] != "2015999" {
			t.Fatalf("List(%+v) = %#v, want only gc-meta-int with external_ref \"2015999\"", q, rows)
		}
	}
}
