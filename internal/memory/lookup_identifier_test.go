package memory

import "testing"

func TestLookupIdentifier(t *testing.T) {
	for query, want := range map[string]string{
		"HarborQuartz853 표식": "HarborQuartz853", "JasperAster244e0ea187 로드맵 라벨": "JasperAster244e0ea187",
		"HarborQuartz853 말고 다른 표식": "", "한국어로 답해줘": "", "HarborQuartz853 or VioletCairn874610": "", "roadmaps": "",
	} {
		if got := lookupIdentifier(query); got != want {
			t.Errorf("%q: got %q, want %q", query, got, want)
		}
	}
	for text, want := range map[string]bool{"(harborquartz853)": true, "HarborQuartz8531": false, "aHarborQuartz853": false, "other_HarborQuartz853": false} {
		if got := containsLookupIdentifier(text, "HarborQuartz853"); got != want {
			t.Errorf("%q: got %t", text, got)
		}
	}
}
func TestSearchLiteralIdentifier(t *testing.T) {
	f := newFixture(t)
	record := f.activeMemory("The ticket is CraneLeaf724.", "CraneLeaf724")
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	f.emb.calls = nil
	for _, tc := range []struct {
		query string
		found bool
	}{
		{"CraneLeaf724 label", true}, {"VioletCairn874610", false}, {"VioletCairn874610 로드맵 라벨", false},
	} {
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: tc.query, Purpose: "answer"})
		if err != nil || len(out.Degraded) > 0 || len(f.emb.calls) != 0 {
			t.Fatalf("%s: err=%v degraded=%v embeds=%d", tc.query, err, out.Degraded, len(f.emb.calls))
		}
		if tc.found && (len(out.Items) != 1 || out.Items[0].ID != record.ID) || !tc.found && len(out.Items) != 0 {
			t.Fatalf("%s: found %d records", tc.query, len(out.Items))
		}
	}
	_, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "VioletCairn874610", Modes: []string{"vector"}})
	if err != nil || len(f.emb.calls) != 1 {
		t.Fatal("explicit vector search", err, len(f.emb.calls))
	}
}
