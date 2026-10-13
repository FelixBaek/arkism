package memory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorkCheckpointArchiveProvenance(t *testing.T) { testWorkArchiveProvenance(t, nil) }
func TestPostgresWorkCheckpointArchiveProvenance(t *testing.T) {
	testWorkArchiveProvenance(t, isolatedPostgres(t))
}
func testWorkArchiveProvenance(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	id := ContentHash("work-provenance")
	raw := jsonLine(map[string]string{"role": "user", "content": "작업 근거"})
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	in := WorkCheckpointSaveInput{Scope: scopeA, ID: "work-proof", EventID: "initial", Package: workCheckpointPackage(), ArchiveMessages: []ArchiveMessageRef{{ArchiveID: id, SHA256: ContentHash(string(raw))}}}
	first, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, in)
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: in.ID})
	if err != nil || len(view.ArchiveMessages) != 1 || view.ArchiveMessages[0].MessageSHA256 == "" {
		t.Fatalf("missing verified provenance: %+v %v", view, err)
	}
	original := view.ArchiveMessages[0]
	record, err := f.svc.Repo.Get(testCtx, colWorkCheckpoints, scopeA, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(record)
	if strings.Contains(string(public), id) {
		t.Fatal("generic record leaked provenance")
	}
	*now = now.Add(time.Second)
	raw = append(raw, jsonLine(map[string]string{"role": "assistant", "content": "후속 메시지"})...)
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	old := in
	old.EventID = "stale"
	old.ExpectedRevision = 1
	old.ArchiveMessages = []ArchiveMessageRef{original}
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, old); !errors.Is(err, ErrConflict) {
		t.Fatal("stale original version accepted", err)
	}
	updated := old
	updated.EventID = "updated"
	updated.ArchiveMessages = append([]ArchiveMessageRef(nil), old.ArchiveMessages...)
	updated.ArchiveMessages[0].SHA256 = ContentHash(string(raw))
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, updated); err != nil {
		t.Fatal(err)
	}
	view, err = f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: in.ID})
	if err != nil || !reflect.DeepEqual(view.ArchiveMessages, []ArchiveMessageRef{original}) {
		t.Fatal("immutable provenance lost or duplicated", err)
	}
	*now = now.Add(SessionArchiveTTL)
	if _, err := f.svc.ExpireSessionArchives(testCtx, f.human, SessionArchiveExpireInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	// Expired raw data does not delete independently saved work or its old pointers.
	view, err = f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: in.ID})
	if err != nil || len(view.ArchiveMessages) != 1 {
		t.Fatal("expired archive erased work", err)
	}
	if _, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: original}); !errors.Is(err, ErrConflict) {
		t.Fatal("expired raw text exposed", err)
	}
	again, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, in)
	if err != nil || again != first {
		t.Fatal("exact committed receipt lost after expiry", err)
	}
	noRefs := in
	noRefs.EventID = "manual"
	noRefs.ExpectedRevision = 2
	noRefs.ArchiveMessages = nil
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, noRefs); err != nil {
		t.Fatal("manual update reread expired raw", err)
	}
	view, err = f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: in.ID})
	if err != nil || !reflect.DeepEqual(view.ArchiveMessages, []ArchiveMessageRef{original}) {
		t.Fatal("omission erased old provenance", err)
	}
}

func TestPostgresWorkArchiveFenceBeforeSave(t *testing.T) {
	for _, change := range []string{"expire", "append"} {
		t.Run(change, func(t *testing.T) {
			p := isolatedPostgres(t)
			f, now := archiveSearchFixture(t, p)
			id := ContentHash("work-race-" + change)
			raw := jsonLine(map[string]string{"role": "user", "content": "저장 근거"})
			archiveSearchUpload(t, f, *now, id, raw, "jsonl")
			f.svc.Repo = &sourceArchiveMutation{Postgres: p, before: func() {
				if change == "expire" {
					*now = now.Add(SessionArchiveTTL)
					return
				}
				*now = now.Add(time.Second)
				other := *f
				other.svc = &Service{Repo: p, Auth: f.auth, SessionArchiveKey: f.svc.SessionArchiveKey, SessionArchiveNow: f.svc.SessionArchiveNow}
				archiveSearchUpload(t, &other, *now, id, append(raw, jsonLine(map[string]string{"role": "user", "content": "new"})...), "jsonl")
			}}
			in := WorkCheckpointSaveInput{Scope: scopeA, ID: "race-work", EventID: "race-event", Package: workCheckpointPackage(), ArchiveMessages: []ArchiveMessageRef{{ArchiveID: id, SHA256: ContentHash(string(raw))}}}
			if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, in); !errors.Is(err, ErrConflict) {
				t.Fatal("changed archive persisted", err)
			}
			for _, collection := range []string{colWorkCheckpoints, colWorkCheckpointReceipts} {
				var n int
				if err := p.pool.QueryRow(testCtx, "SELECT count(*) FROM memory_records WHERE collection=$1", collection).Scan(&n); err != nil || n != 0 {
					t.Fatal("write/receipt survived failed fence", collection, n, err)
				}
			}
		})
	}
}

func TestWorkArchiveActorCannotChange(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	id := ContentHash("work-actor")
	raw := jsonLine(map[string]string{"role": "user", "content": "근거"})
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	original := f.svc.Auth
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, in Authorization) (Decision, error) {
		d, err := original.Authorize(ctx, token, in)
		if in.Action == "read:"+colSessionArchives {
			d.PrincipalID = "different-reader"
		}
		return d, err
	})
	_, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "actor-work", EventID: "actor-event", Package: workCheckpointPackage(), ArchiveMessages: []ArchiveMessageRef{{ArchiveID: id, SHA256: ContentHash(string(raw))}}})
	if !errors.Is(err, ErrDenied) {
		t.Fatal("archive reader changed actor", err)
	}
}
