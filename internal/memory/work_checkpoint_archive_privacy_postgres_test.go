package memory

import (
	"errors"
	"testing"
)

func TestPostgresWorkArchivePrivacyCascade(t *testing.T) {
	for _, target := range []string{"source_event", "work_checkpoint"} {
		t.Run(target, func(t *testing.T) {
			p := isolatedPostgres(t)
			f, now := archiveSearchFixture(t, p)
			first := jsonLine(map[string]string{"role": "user", "content": "private first message"})
			raw := append(append([]byte{}, first...), jsonLine(map[string]string{"role": "user", "content": "unrelated second message"})...)
			archive := ContentHash("archive-privacy-" + target)
			archiveSearchUpload(t, f, *now, archive, raw, "jsonl")
			firstRef := ArchiveMessageRef{ArchiveID: archive, SHA256: ContentHash(string(raw))}
			secondRef := firstRef
			secondRef.Offset = len(first)
			for _, entry := range []struct {
				id  string
				ref ArchiveMessageRef
			}{{"parent", firstRef}, {"child", firstRef}, {"unrelated", secondRef}} {
				if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: entry.id, EventID: entry.id, Package: workCheckpointPackage(), ArchiveMessages: []ArchiveMessageRef{entry.ref}}); err != nil {
					t.Fatal(err)
				}
			}
			sourceIn := sourceInput(CreateInput{Scope: scopeA, Text: "private archive summary"})
			sourceIn.ArchiveMessages = []ArchiveMessageRef{firstRef}
			src := f.create(f.human, "sources", sourceIn)
			// Same archive pointer in another scope must not be included in discovery.
			foreign, err := p.Get(testCtx, colWorkCheckpoints, scopeA, "child")
			if err != nil {
				t.Fatal(err)
			}
			foreign.ID = "foreign"
			foreign.Scope = scopeB
			if err = p.Insert(testCtx, colWorkCheckpoints, foreign); err != nil {
				t.Fatal(err)
			}
			// Expiration intentionally preserves independent checkpoint summaries.
			*now = now.Add(SessionArchiveTTL)
			if _, err = f.svc.ExpireSessionArchives(testCtx, f.human, SessionArchiveExpireInput{Scope: scopeA}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: "child"}); err != nil {
				t.Fatalf("TTL erased summary: %v", err)
			}
			targetID := "parent"
			if target == "source_event" {
				targetID = src.ID
			}
			if _, err := p.DiscoverPrivacyImpact(testCtx, scopeA, target, []string{targetID}, 3); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("cascade exceeded bounded impact: %v", err)
			}
			// Explicit privacy deletion must still follow immutable provenance after TTL.
			deletion, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "archive-privacy", TargetType: target, TargetIDs: []string{targetID}, Reason: "owner_request", RetentionPolicyRef: retentionExternal})
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, ref := range deletion.PrivacyDeletion.Impact.Records {
				counts[ref.Collection]++
			}
			if counts[colWorkCheckpoints] != 2 || counts[colWorkCheckpointReceipts] != 2 {
				t.Fatalf("cascade lost parent/child or receipts: %v", counts)
			}
			for _, id := range []string{"parent", "child"} {
				if _, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: id}); !errors.Is(err, ErrNotFound) {
					t.Fatalf("%s still readable: %v", id, err)
				}
			}
			if _, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: "unrelated"}); err != nil {
				t.Fatalf("different message affected: %v", err)
			}
			if _, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeB, ID: "foreign"}); err != nil {
				t.Fatalf("foreign scope affected: %v", err)
			}
		})
	}
}
