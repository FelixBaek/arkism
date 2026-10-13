package memory

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionArchiveStableReference(t *testing.T) { testArchiveStableReference(t, nil) }
func TestPostgresSessionArchiveStableReference(t *testing.T) {
	testArchiveStableReference(t, isolatedPostgres(t))
}
func testArchiveStableReference(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	id := ContentHash("stable-record")
	raw := bytes.TrimSuffix(jsonLine(map[string]string{"role": "user", "content": "첫 번째 단서"}), []byte{'\n'})
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, Query: "단서"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Ref.MessageSHA256 == "" {
		t.Fatal("missing message identity", err)
	}
	ref := page.Items[0].Ref
	*now = now.Add(time.Second)
	raw = append(append(raw, '\n'), jsonLine(map[string]string{"role": "assistant", "content": "later"})...)
	appended := archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref})
	if err != nil || view.Text != "첫 번째 단서" || view.Ref.MessageSHA256 != ref.MessageSHA256 || view.Ref.SHA256 == ref.SHA256 {
		t.Fatalf("append broke unchanged record: %+v %v", view, err)
	}
	legacy := ref
	legacy.MessageSHA256 = ""
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: legacy}); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy SHA bypassed", err)
	}
	for _, altered := range []ArchiveMessageRef{
		{ArchiveID: id, SHA256: ref.SHA256, MessageSHA256: "invalid"},
		{ArchiveID: id, SHA256: ref.SHA256, MessageSHA256: ContentHash("wrong")},
	} {
		if result, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: altered}); err == nil || result.Text != "" {
			t.Fatal("invalid digest returned content")
		}
	}
	h, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || !h.ExpiresAt.Equal(*appended.ExpiresAt) {
		t.Fatal("stable read renewed retention", err)
	}
	*now = now.Add(time.Second)
	changed := jsonLine(map[string]string{"role": "user", "content": "수정된 단서"})
	changedHeader := archiveSearchUpload(t, f, *now, id, changed, "jsonl")
	if result, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref}); !errors.Is(err, ErrConflict) || result.Text != "" {
		t.Fatal("changed record returned", err)
	}
	current, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(changed))}})
	if err != nil {
		t.Fatal(err)
	}
	ref = current.Ref
	*now = *changedHeader.ExpiresAt
	if result, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref}); !errors.Is(err, ErrConflict) || result.Text != "" {
		t.Fatal("stable ref bypassed expiry", err)
	}
}
func TestSessionArchiveStableHashBindsZcodeRole(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	before := []byte("{\"table\":\"message\",\"record\":{\"id\":\"m\",\"data\":\"{\\\"role\\\":\\\"user\\\"}\"}}     \n")
	after := []byte("{\"table\":\"message\",\"record\":{\"id\":\"m\",\"data\":\"{\\\"role\\\":\\\"assistant\\\"}\"}}\n")
	if len(before) != len(after) {
		t.Fatal("fixture must preserve exact part offset")
	}
	part := jsonLine(map[string]any{"table": "part", "record": map[string]string{"message_id": "m", "data": `{"type":"text","text":"단서"}`}})
	id := ContentHash("zcode-role-change")
	archiveSearchUpload(t, f, *now, id, append(before, part...), "reconstructed_zcode")
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, Query: "단서"})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("initial message missing", err)
	}
	*now = now.Add(time.Second)
	archiveSearchUpload(t, f, *now, id, append(after, part...), "reconstructed_zcode")
	if view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: page.Items[0].Ref}); !errors.Is(err, ErrConflict) || view.Text != "" {
		t.Fatal("unchanged part inherited new role", err)
	}
}
func TestSessionArchiveStableRefBindsFormatAndPosition(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	raw := jsonLine(map[string]string{"role": "user", "content": "단서"})
	first := ContentHash("jsonl-ref")
	archiveSearchUpload(t, f, *now, first, raw, "jsonl")
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: first, SHA256: ContentHash(string(raw))}})
	if err != nil {
		t.Fatal(err)
	}
	second := ContentHash("agentsview-ref")
	archiveSearchUpload(t, f, *now, second, raw, "reconstructed_agentsview")
	forged := view.Ref
	forged.ArchiveID = second
	if _, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: forged}); !errors.Is(err, ErrConflict) {
		t.Fatal("message digest ignored format", err)
	}
	*now = now.Add(time.Second)
	archiveSearchUpload(t, f, *now, first, append(jsonLine(map[string]string{"role": "user", "content": "다른 메시지"}), raw...), "jsonl")
	if out, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: view.Ref}); !errors.Is(err, ErrConflict) || out.Text != "" {
		t.Fatal("moved message guessed at wrong position", err)
	}
}

type archiveBetweenReferences struct {
	Repository
	reads  int
	change func()
}

func (r *archiveBetweenReferences) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	row, err := r.Repository.Get(ctx, col, scope, id)
	if col == colSessionArchives && err == nil {
		r.reads++
		if r.reads == 2 {
			r.change()
		}
	}
	return row, err
}
func TestSourceArchiveStableMultiRefFence(t *testing.T) { testSourceArchiveStableMultiRefFence(t, nil) }
func TestPostgresSourceArchiveStableMultiRefFence(t *testing.T) {
	testSourceArchiveStableMultiRefFence(t, isolatedPostgres(t))
}
func testSourceArchiveStableMultiRefFence(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	base := f.svc.Repo
	id := ContentHash("multi-ref-change")
	first := jsonLine(map[string]string{"role": "user", "content": "original-one"})
	second := jsonLine(map[string]string{"role": "user", "content": "original-two"})
	raw := append(append([]byte{}, first...), second...)
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	refs := []ArchiveMessageRef{}
	for _, offset := range []int{0, len(first)} {
		view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.human, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw)), Offset: offset}})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, view.Ref)
	}
	writer := &fixture{t: t, human: f.human, svc: &Service{Repo: base, Auth: f.auth, SessionArchiveKey: f.svc.SessionArchiveKey, SessionArchiveNow: f.svc.SessionArchiveNow}}
	f.svc.Repo = &archiveBetweenReferences{Repository: base, change: func() {
		*now = now.Add(time.Second)
		changed := jsonLine(map[string]string{"role": "user", "content": "modified-one"})
		if len(changed) != len(first) {
			t.Fatal("fixture moved second record")
		}
		archiveSearchUpload(t, writer, *now, id, append(changed, second...), "jsonl")
	}}
	in := sourceInput(CreateInput{Scope: scopeA, Text: "must not mix two archive versions", ArchiveMessages: refs})
	if _, err := f.svc.Create(testCtx, f.human, "sources", in); !errors.Is(err, ErrConflict) {
		t.Fatal("mixed archive versions persisted", err)
	}
}

func TestCaptureStableReferenceRetryAfterAppend(t *testing.T) {
	testCaptureStableReferenceRetry(t, nil, ErrUnavailable)
}
func TestPostgresCaptureStableReferenceRetryAfterAppend(t *testing.T) {
	testCaptureStableReferenceRetry(t, isolatedPostgres(t), ErrUnavailable)
}
func TestCaptureStableReferenceConflictRetry(t *testing.T) {
	testCaptureStableReferenceRetry(t, nil, ErrConflict)
}
func TestPostgresCaptureStableReferenceConflictRetry(t *testing.T) {
	testCaptureStableReferenceRetry(t, isolatedPostgres(t), ErrConflict)
}
func testCaptureStableReferenceRetry(t *testing.T, repo Repository, failure error) {
	f, now := archiveSearchFixture(t, repo)
	id := ContentHash("capture-retry-append")
	raw := jsonLine(map[string]string{"role": "user", "content": "앞으로 짧게 답해주세요"})
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}})
	if err != nil {
		t.Fatal(err)
	}
	claim := explicit("짧은 답변을 선호한다")
	claim.ArchiveMessages = []ArchiveMessageRef{view.Ref}
	input := captureInput(scopeA, "retry-same-event", claim)
	input.Event.SessionID = "search-session"
	original := f.svc.Auth
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "write:memory_candidates" {
			return Decision{}, failure
		}
		return original.Authorize(ctx, token, req)
	})
	first, err := f.svc.Capture(testCtx, f.agent, input)
	if err != nil || len(first.Items) != 1 || first.Items[0].Status != "rejected" {
		t.Fatal("failure injection did not interrupt candidate creation", first, err)
	}
	f.svc.Auth = original
	*now = now.Add(time.Second)
	archiveSearchUpload(t, f, *now, id, append(raw, jsonLine(map[string]string{"role": "assistant", "content": "later answer"})...), "jsonl")
	retried, err := f.svc.Capture(testCtx, f.agent, input)
	if err != nil || len(retried.Items) != 1 || retried.Items[0].Status != "candidate" {
		t.Fatal("retry lost claim after append", retried, err)
	}
	candidate, err := f.svc.Repo.Get(testCtx, "memory_candidates", scopeA, retried.Items[0].CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.svc.Repo.Get(testCtx, "sources", scopeA, candidate.Sources[0].SourceID)
	if err != nil || len(source.ArchiveMessages) != 1 || source.ArchiveMessages[0] != view.Ref {
		t.Fatal("retry replaced saved provenance", err)
	}
}
func TestSourceArchiveLegacyDedupe(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	raw := jsonLine(map[string]string{"role": "user", "content": "same original"})
	id := ContentHash("legacy-dedupe")
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	ref := ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}
	input := sourceInput(CreateInput{Scope: scopeA, Text: "legacy source summary", ArchiveMessages: []ArchiveMessageRef{ref}})
	source, err := f.svc.Create(testCtx, f.human, "sources", input)
	if err != nil {
		t.Fatal(err)
	}
	source.ArchiveMessages = []ArchiveMessageRef{ref}
	f.repo.store["sources"][recKey(scopeA, source.ID)] = source // pre-upgrade storage fixture
	duplicate, err := f.svc.Create(testCtx, f.human, "sources", input)
	if err != nil || !duplicate.Duplicate || duplicate.ID != source.ID || duplicate.ArchiveMessages[0].MessageSHA256 != "" {
		t.Fatal("legacy source dedupe changed", err)
	}
}
