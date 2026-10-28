package beads_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// fakeBdExternalRef is a tiny stateful stand-in for the bd CLI that models
// exactly the external_ref wire surface BdStore relies on: `bd create
// --external-ref`, the `external_ref` key in bd's JSON, and `bd list
// --external-ref=<v>` as an exact top-level match. Every other argument is
// accepted and ignored, so the test pins the external_ref contract without
// re-specifying the rest of bd.
type fakeBdExternalRef struct {
	mu      sync.Mutex
	next    int
	issues  []map[string]any
	creates [][]string
	lists   [][]string
}

func (f *fakeBdExternalRef) run(_, _ string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 0 {
		return nil, fmt.Errorf("fake bd: no args")
	}
	switch args[0] {
	case "create":
		f.creates = append(f.creates, append([]string(nil), args...))
		f.next++
		issue := map[string]any{
			"id":         fmt.Sprintf("bd-%d", f.next),
			"title":      args[2],
			"status":     "open",
			"issue_type": "task",
			"created_at": "2026-10-28T10:00:00Z",
		}
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--external-ref" {
				issue["external_ref"] = args[i+1]
			}
		}
		f.issues = append(f.issues, issue)
		return json.Marshal(issue)
	case "show":
		for _, issue := range f.issues {
			for _, a := range args[1:] {
				if issue["id"] == a {
					return json.Marshal([]map[string]any{issue})
				}
			}
		}
		return nil, fmt.Errorf("fake bd: no issue found")
	case "list":
		f.lists = append(f.lists, append([]string(nil), args...))
		want, filtered := "", false
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--external-ref="); ok {
				want, filtered = v, true
			}
		}
		out := []map[string]any{}
		for _, issue := range f.issues {
			if filtered && issue["external_ref"] != want {
				continue
			}
			out = append(out, issue)
		}
		return json.Marshal(out)
	}
	return []byte("[]"), nil
}

func TestBdStoreExternalRefRoundTrip(t *testing.T) {
	fake := &fakeBdExternalRef{}
	s := beads.NewBdStore("/city", fake.run)

	created, err := s.Create(beads.Bead{Title: "mirror", ExternalRef: "2013950"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create(beads.Bead{Title: "other mirror", ExternalRef: "2013951"}); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if _, err := s.Create(beads.Bead{Title: "no ref"}); err != nil {
		t.Fatalf("Create no-ref: %v", err)
	}

	if len(fake.creates) == 0 || !strings.Contains(strings.Join(fake.creates[0], " "), "--external-ref 2013950") {
		t.Fatalf("bd create args = %q, want --external-ref 2013950", fake.creates)
	}
	for _, args := range fake.creates[2:] {
		if strings.Contains(strings.Join(args, " "), "--external-ref") {
			t.Fatalf("bd create without ExternalRef passed --external-ref: %q", args)
		}
	}
	if created.ExternalRef != "2013950" {
		t.Fatalf("Create echo ExternalRef = %q, want 2013950", created.ExternalRef)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ExternalRef != "2013950" {
		t.Fatalf("Get ExternalRef = %q, want 2013950", got.ExternalRef)
	}

	list, err := s.List(beads.ListQuery{ExternalRef: "2013950"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("List(ExternalRef=2013950) = %+v, want exactly %s", list, created.ID)
	}
	sawFlag := false
	for _, args := range fake.lists {
		if strings.Contains(strings.Join(args, " "), "--external-ref=2013950") {
			sawFlag = true
		}
	}
	if !sawFlag {
		t.Fatalf("bd list args = %q, want --external-ref=2013950 pushed to bd", fake.lists)
	}
}

func TestBdStoreExternalRefNullDecodesEmpty(t *testing.T) {
	runner := func(_, _ string, _ ...string) ([]byte, error) {
		return []byte(`[{"id":"bd-1","title":"t","status":"open","issue_type":"task","created_at":"2026-10-28T10:00:00Z","external_ref":null}]`), nil
	}
	s := beads.NewBdStore("/city", runner)
	got, err := s.Get("bd-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ExternalRef != "" {
		t.Fatalf("ExternalRef = %q, want empty for JSON null", got.ExternalRef)
	}
}

func TestListQueryExternalRefIsAFilter(t *testing.T) {
	q := beads.ListQuery{ExternalRef: "2013950"}
	if !q.HasFilter() {
		t.Fatal("ListQuery{ExternalRef} must count as a filter (no AllowScan needed)")
	}
	if !q.Matches(beads.Bead{Status: "open", ExternalRef: "2013950"}) {
		t.Fatal("Matches must accept an exact top-level external_ref match")
	}
	if q.Matches(beads.Bead{Status: "open", ExternalRef: "20139500"}) {
		t.Fatal("Matches must be exact, not prefix")
	}
	if q.Matches(beads.Bead{Status: "open", Metadata: map[string]string{"external_ref": "2013950"}}) {
		t.Fatal("ListQuery.ExternalRef matches only the top-level field, not metadata")
	}
}
