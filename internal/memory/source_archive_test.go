package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSourceArchiveCaptureRoundtrip(t *testing.T) { testSourceArchiveCaptureRoundtrip(t, nil) }
func TestPostgresSourceArchiveCaptureRoundtrip(t *testing.T) {
	testSourceArchiveCaptureRoundtrip(t, isolatedPostgres(t))
}
func testSourceArchiveCaptureRoundtrip(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	raw := jsonLine(map[string]string{"role": "user", "content": "앞으로 답은 짧게 해 주세요."})
	id := ContentHash("linked-original")
	header := archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	ref := ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}
	original, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	claim := explicit("사용자는 짧은 답변을 선호한다.")
	claim.ArchiveMessages = []ArchiveMessageRef{ref}
	in := captureInput(scopeA, "original-ref-event", claim)
	in.Event.SessionID = "search-session"
	out, err := f.svc.Capture(testCtx, f.agent, in)
	if err != nil || len(out.Items) != 1 || out.Items[0].Status != "candidate" {
		t.Fatalf("capture: %+v %v", out, err)
	}
	candidate, err := f.svc.Repo.Get(testCtx, "memory_candidates", scopeA, out.Items[0].CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := f.svc.TraceMemorySources(testCtx, f.agent, scopeA, memory.ID)
	if err != nil || len(trace.Sources) != 1 || !reflect.DeepEqual(trace.Sources[0].ArchiveMessages, []ArchiveMessageRef{original.Ref}) {
		t.Fatalf("lost original link: %+v %v", trace, err)
	}
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: trace.Sources[0].ArchiveMessages[0]})
	if err != nil || view.Text != "앞으로 답은 짧게 해 주세요." {
		t.Fatal("wrong original", err)
	}
	stored, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || !stored.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("link extended TTL", err)
	}
	source, err := f.svc.Repo.Get(testCtx, "sources", scopeA, trace.Sources[0].SourceID)
	if err != nil || source.Text != claim.Text {
		t.Fatal("raw copied into durable source", err)
	}
	changed := source
	changed.ArchiveMessages = nil
	if repo != nil {
		changed.Revision++
		if err := repo.Replace(testCtx, "sources", changed, source.Revision); !errors.Is(err, ErrConflict) {
			t.Fatal("stored link changed", err)
		}
	}

	*now = now.Add(time.Second)
	archiveSearchUpload(t, f, *now, id, append(raw, jsonLine(map[string]string{"role": "assistant", "content": "follow-up answer"})...), "jsonl")
	stable, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: trace.Sources[0].ArchiveMessages[0]})
	if err != nil || stable.Text != original.Text {
		t.Fatal("stored source lost after append", err)
	}
	*now = now.Add(SessionArchiveTTL)
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref}); !errors.Is(err, ErrConflict) {
		t.Fatal("expired original returned", err)
	}
	if _, err = f.svc.Get(testCtx, f.agent, "memories", scopeA, memory.ID); err != nil {
		t.Fatal("raw expiry invalidated durable memory", err)
	}
	trace, err = f.svc.TraceMemorySources(testCtx, f.agent, scopeA, memory.ID)
	if err != nil || len(trace.Sources[0].ArchiveMessages) != 1 {
		t.Fatal("expiry lost provenance", err)
	}
}
func TestSourceArchiveRejectsInvalidLinks(t *testing.T) {
	for _, mode := range []string{"session", "role", "offset", "foreign", "duplicate", "too_many", "non_source"} {
		t.Run(mode, func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			raw := jsonLine(map[string]string{"role": "assistant", "content": "A verified answer"})
			id := ContentHash("invalid-link")
			archiveSearchUpload(t, f, *now, id, raw, "jsonl")
			ref := ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}
			if mode == "session" || mode == "role" {
				claim := explicit("A new lasting preference")
				if mode == "session" {
					claim.Basis = "inferred"
				}
				claim.ArchiveMessages = []ArchiveMessageRef{ref}
				in := captureInput(scopeA, "invalid-link-event", claim)
				if mode == "role" {
					in.Event.SessionID = "search-session"
				}
				out, err := f.svc.Capture(testCtx, f.agent, in)
				if err != nil || len(out.Items) != 1 || out.Items[0].Status != "rejected" {
					t.Fatal("invalid capture accepted", out, err)
				}
				return
			}
			in := sourceInput(CreateInput{Scope: scopeA, Text: "source summary"})
			in.ArchiveMessages = []ArchiveMessageRef{ref}
			col := "sources"
			switch mode {
			case "offset":
				in.ArchiveMessages[0].Offset = 1
			case "foreign":
				in.Scope = scopeB
			case "duplicate":
				in.ArchiveMessages = append(in.ArchiveMessages, ref)
			case "too_many":
				in.ArchiveMessages = make([]ArchiveMessageRef, 9)
			case "non_source":
				col = "memory_candidates"
			}
			if _, err := f.svc.Create(testCtx, f.human, col, in); err == nil {
				t.Fatal("invalid ref accepted")
			}
		})
	}
}

type sourceArchiveMutation struct {
	*Postgres
	before func()
}

func (r *sourceArchiveMutation) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if r.before != nil {
		f := r.before
		r.before = nil
		f()
	}
	return r.Postgres.Transaction(ctx, fn)
}
func TestPostgresSourceArchiveExpiresBeforeInsert(t *testing.T) {
	p := isolatedPostgres(t)
	f, now := archiveSearchFixture(t, p)
	raw := jsonLine(map[string]string{"role": "user", "content": "source evidence"})
	id := ContentHash("source-insert-race")
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	f.svc.Repo = &sourceArchiveMutation{Postgres: p, before: func() { *now = now.Add(SessionArchiveTTL) }}
	in := sourceInput(CreateInput{Scope: scopeA, Text: "summary never stored", ArchiveMessages: []ArchiveMessageRef{{ArchiveID: id, SHA256: ContentHash(string(raw))}}})
	if _, err := f.svc.Create(testCtx, f.human, "sources", in); !errors.Is(err, ErrConflict) {
		t.Fatal("expired reference persisted", err)
	}
	var n int
	if err := p.pool.QueryRow(testCtx, "SELECT count(*) FROM memory_records WHERE collection='sources'").Scan(&n); err != nil || n != 0 {
		t.Fatal("source inserted despite failed fence", n, err)
	}
}

func TestSourceArchiveNormalizesVerifiedIdentity(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	raw := jsonLine(map[string]string{"role": "user", "content": "verified identity"})
	id := ContentHash("normalized-message")
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	legacy := ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.human, ArchiveMessageInput{Scope: scopeA, Ref: legacy})
	if err != nil {
		t.Fatal(err)
	}
	claimed := view.Ref
	claimed.SHA256 = ContentHash("unproven old version")
	input := sourceInput(CreateInput{Scope: scopeA, Text: "linked summary", ArchiveMessages: []ArchiveMessageRef{claimed}})
	source, err := f.svc.Create(testCtx, f.human, "sources", input)
	if err != nil || len(source.ArchiveMessages) != 1 || source.ArchiveMessages[0] != view.Ref {
		t.Fatal("stored unverified archive version", err)
	}

	*now = now.Add(time.Second)
	archiveSearchUpload(t, f, *now, id, append(raw, jsonLine(map[string]string{"role": "assistant", "content": "later answer"})...), "jsonl")
	duplicate, err := f.svc.Create(testCtx, f.human, "sources", input)
	if err != nil || !duplicate.Duplicate || duplicate.ID != source.ID || duplicate.ArchiveMessages[0] != source.ArchiveMessages[0] {
		t.Fatal("append changed source dedupe/provenance", err)
	}
	// Both variants identify the same position even across archive versions.
	legacy.SHA256 = ContentHash(string(append(append([]byte{}, raw...), jsonLine(map[string]string{"role": "assistant", "content": "later answer"})...)))
	input.Text = "duplicate position"
	input.ArchiveMessages = []ArchiveMessageRef{legacy, view.Ref}
	if _, err := f.svc.Create(testCtx, f.human, "sources", input); !errors.Is(err, ErrInvalid) {
		t.Fatal("same position accepted twice", err)
	}
}
