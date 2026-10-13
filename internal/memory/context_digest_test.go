package memory

import "testing"

func TestContextRecordsExposeCurrentDigest(t *testing.T) {
	records := []struct {
		collection string
		record     Record
	}{
		{colMemories, Record{ID: "memory", Scope: scopeA, Status: "active", Revision: 1, Text: "remember this", Summary: "summary"}},
		{colSkillVersions, Record{ID: "skill-version", SkillID: "skill", Scope: scopeA, Status: "active", Revision: 2, Text: "do this"}},
	}

	for _, tc := range records {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFixture(t)
			tc.record.Sources = refs(f.source())
			want := ContentHash(recordContent(tc.record))
			if got := HitFromRecord(tc.collection, tc.record).Digest; got != want {
				t.Fatalf("search digest = %q, want %q", got, want)
			}
			if err := f.repo.Insert(testCtx, tc.collection, tc.record); err != nil {
				t.Fatal(err)
			}
			if got := f.get(f.human, tc.collection, scopeA, tc.record.ID).ContentHash; got != want {
				t.Fatalf("read digest = %q, want %q", got, want)
			}

			changed := tc.record
			changed.Text += " changed"
			if HitFromRecord(tc.collection, changed).Digest == want {
				t.Fatal("digest did not change with content")
			}
		})
	}
}
