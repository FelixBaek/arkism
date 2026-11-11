package memory

import "testing"

func TestConceptEvidenceCountsEachGroupOnce(t *testing.T) {
	for _, tc := range []struct {
		name, query, text string
		found, total      int
	}{
		{"particle_and_stem", "도서관마다 운영 안내", "도서관마다 도서관", 1, 3},
		{"independent_subject", "도서관마다 운영 안내", "도서관마다 도서관 운영", 2, 3},
		{"plural_aliases", "deploy deploys server", "deploy deploys", 1, 2},
		{"english_prefixes", "java javascript plan planet", "java plan", 2, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, total := conceptsFound(searchQueryTerms(tc.query), Hit{Text: tc.text})
			if found != tc.found || total != tc.total {
				t.Fatalf("evidence = %d/%d, want %d/%d", found, total, tc.found, tc.total)
			}
		})
	}
	// The shared stem belongs only to the first matching head, just as in the
	// SQL concept builder; it cannot support both otherwise distinct compounds.
	terms := []string{"자료집정리", "자료집보관", "자료집", "배포"}
	if found, total := conceptsFound(terms, Hit{Text: "자료집"}); found != 1 || total != 3 {
		t.Fatalf("shared prefix counted twice: %d/%d", found, total)
	}
}

func TestPostgresConceptEvidenceRejectsRepeatedSubject(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	f.activeMemory("도서관마다 도서관", "도서관마다")
	good := f.activeMemory("도서관마다 도서관 운영", "도서관마다")
	in := SearchInput{Scope: scopeA, Query: "도서관마다 운영 안내", Modes: []string{"exact", "fts"}, Limit: 5}
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].ID != good.ID {
		t.Fatalf("one repeated subject passed the gate: got %d records, want the independently supported record", len(out.Items))
	}
}
