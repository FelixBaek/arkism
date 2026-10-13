package memory

import (
	"context"
	"testing"
	"time"
)

// The digest part survives the storage codec, and a transaction can count active digests (the cap check).
func TestDigestPostgresRoundTrip(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{ID: "dg_" + ID(), Scope: scopeA, Kind: digestKindTopic, Status: digestActive, Revision: 1, Text: "본문", Summary: "제목", CreatedAt: now, UpdatedAt: now,
		Digest: &DigestData{Title: "제목", Members: []Ref{{Collection: colMemories, ID: "m1", Revision: 2}}, Proposal: "new"}}
	if err := p.Insert(testCtx, colTopicDigests, r); err != nil {
		t.Fatal(err)
	}
	got, err := p.Get(testCtx, colTopicDigests, scopeA, r.ID)
	if err != nil || got.Digest == nil || got.Digest.Title != "제목" || len(got.Digest.Members) != 1 || got.Digest.Members[0].Revision != 2 {
		t.Fatalf("round trip %+v %v", got.Digest, err)
	}
	err = p.Transaction(testCtx, func(ctx2 context.Context, tx Tx) error {
		rows, err := activeDigests(ctx2, tx, colTopicDigests, scopeA)
		if err == nil && len(rows) != 1 {
			t.Fatal("active in tx", len(rows))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
