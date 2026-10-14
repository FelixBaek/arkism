package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func modelTransferFixture(t *testing.T) (*fixture, *time.Time, ModelArchiveTransferInput) {
	t.Helper()
	f, now := archiveSearchFixture(t, nil)
	f.auth.actors["transfer-service"] = actor("harness-service", roleService, true, "transfer", "read", "write")
	raw := jsonLine(map[string]string{"role": "user", "content": "한국어 선호를 다음 작업에도 적용한다"})
	id := ContentHash("model-transfer-current")
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}})
	if err != nil {
		t.Fatal(err)
	}
	return f, now, ModelArchiveTransferInput{Scope: scopeA, Refs: []ArchiveMessageRef{view.Ref}, Destination: "external:anthropic", Content: view.Text}
}

func TestModelArchiveTransferPrivateServiceAndNoStoredBody(t *testing.T) {
	f, _, in := modelTransferFixture(t)
	before, err := f.repo.Get(testCtx, colSessionArchives, scopeA, in.Refs[0].ArchiveID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.AuthorizeModelArchiveTransfer(testCtx, "transfer-service", in)
	if err != nil || !out.Authorized || out.ContentHash != ContentHash(in.Content) {
		t.Fatalf("valid private archive egress: %+v %v", out, err)
	}
	after, err := f.repo.Get(testCtx, colSessionArchives, scopeA, before.ID)
	if err != nil || after.CreatedBy != before.CreatedBy || after.CreatedByType != before.CreatedByType || after.Revision != before.Revision {
		t.Fatal("transfer changed archive provenance", err)
	}
	found := false
	for _, row := range f.repo.rows("audit_events") {
		if row.Kind != "transfer:model_input" {
			continue
		}
		found = true
		raw, _ := json.Marshal(row)
		if strings.Contains(string(raw), in.Content) || row.Text != "" || row.CreatedBy != "harness-service" {
			t.Fatal("egress audit stores body or fabricates actor")
		}
	}
	if !found {
		t.Fatal("missing transfer audit")
	}
}

func TestModelArchiveTransferRejectsUnauthorizedAndUnsafe(t *testing.T) {
	for _, mode := range []string{"human", "agent", "no-transfer", "no-external", "no-read", "foreign-scope", "secret", "restricted", "quarantined", "deleted", "changed-message", "bad-destination", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f, now, in := modelTransferFixture(t)
			token := "transfer-service"
			switch mode {
			case "human":
				f.auth.actors[token] = actor("owner", roleHuman, true, "transfer", "read")
			case "agent":
				f.auth.actors[token] = actor("agent", roleAgent, true, "transfer", "read")
			case "no-transfer":
				f.auth.actors[token] = actor("harness-service", roleService, true, "read")
			case "no-external":
				f.auth.actors[token] = actor("harness-service", roleService, false, "transfer", "read")
			case "no-read":
				f.auth.actors[token] = actor("harness-service", roleService, true, "transfer")
			case "foreign-scope":
				in.Scope = scopeB
			case "secret":
				in.Content = "password: hunter2secret"
			case "bad-destination":
				in.Destination = "external:other"
			case "audit":
				f.repo.failAudit = true
			case "changed-message":
				*now = now.Add(time.Second)
				raw := jsonLine(map[string]string{"role": "user", "content": "이제 다른 내용으로 정정한다"})
				archiveSearchUpload(t, f, *now, in.Refs[0].ArchiveID, raw, "jsonl")
			default:
				r, e := f.repo.Get(testCtx, colSessionArchives, scopeA, in.Refs[0].ArchiveID)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "deleted" {
					if e = f.repo.Delete(testCtx, colSessionArchives, scopeA, r.ID, r.Revision); e != nil {
						t.Fatal(e)
					}
				} else {
					if mode == "restricted" {
						r.Sensitivity = "restricted"
					} else {
						r.PrivacyQuarantined = true
					}
					r.Revision++
					if e = f.repo.Replace(testCtx, colSessionArchives, r, r.Revision-1); e != nil {
						t.Fatal(e)
					}
				}
			}
			out, err := f.svc.AuthorizeModelArchiveTransfer(testCtx, token, in)
			if err == nil || out.Authorized || out.ContentHash != "" {
				t.Fatalf("unsafe %s allowed: %+v %v", mode, out, err)
			}
		})
	}
}

func TestModelArchiveTransferRevalidatesPreviousWorkEvidence(t *testing.T) {
	for _, mode := range []string{"live", "source-revision", "source-deleted", "source-restricted-live", "source-restricted", "source-quarantined", "archive-deleted", "archive-restricted", "archive-changed", "work-revision"} {
		t.Run(mode, func(t *testing.T) {
			f, now, in := modelTransferFixture(t)
			source := f.source()
			if mode == "source-restricted-live" {
				source.Sensitivity = "restricted"
				source.Revision++
				if err := f.repo.Replace(testCtx, "sources", source, source.Revision-1); err != nil {
					t.Fatal(err)
				}
			}
			pkg := workCheckpointPackage()
			pkg.SourceRefs = []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}
			raw := jsonLine(map[string]string{"role": "assistant", "content": "지난 세션에서 확인한 단계"})
			id := ContentHash("previous-transfer-evidence")
			archiveSearchUpload(t, f, *now, id, raw, "jsonl")
			previous, e := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}})
			if e != nil {
				t.Fatal(e)
			}
			saved, e := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "transfer-work", EventID: "event-1", Package: pkg, ArchiveMessages: []ArchiveMessageRef{previous.Ref}})
			if e != nil {
				t.Fatal(e)
			}
			in.Work = &Ref{Collection: colWorkCheckpoints, ID: saved.ID, Revision: saved.Revision}
			in.Content += "\n이전 요약: " + pkg.Goal
			switch mode {
			case "source-deleted":
				e = f.repo.Delete(testCtx, "sources", scopeA, source.ID, source.Revision)
			case "source-revision", "source-restricted", "source-quarantined":
				r := source
				r.Revision++
				if mode == "source-restricted" {
					r.Sensitivity = "restricted"
				}
				if mode == "source-quarantined" {
					r.PrivacyQuarantined = true
				}
				e = f.repo.Replace(testCtx, "sources", r, source.Revision)
			case "archive-deleted", "archive-restricted":
				r, err := f.repo.Get(testCtx, colSessionArchives, scopeA, id)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "archive-deleted" {
					e = f.repo.Delete(testCtx, colSessionArchives, scopeA, id, r.Revision)
				} else {
					r.Sensitivity = "restricted"
					r.Revision++
					e = f.repo.Replace(testCtx, colSessionArchives, r, r.Revision-1)
				}
			case "archive-changed":
				*now = now.Add(time.Second)
				archiveSearchUpload(t, f, *now, id, jsonLine(map[string]string{"role": "assistant", "content": "이전 근거를 정정함"}), "jsonl")
			case "work-revision":
				in.Work.Revision++
			}
			if e != nil {
				t.Fatal(e)
			}
			out, err := f.svc.AuthorizeModelArchiveTransfer(testCtx, "transfer-service", in)
			if mode == "live" {
				if err != nil || !out.Authorized {
					t.Fatalf("valid work refused: %+v %v", out, err)
				}
			} else if err == nil || out.Authorized {
				t.Fatalf("stale previous evidence allowed: %s", mode)
			}
		})
	}
}

// A legacy checkpoint can cite a source whose archive provenance is stored on
// the source only. Export must revalidate that transitive dependency as well.
func TestModelArchiveTransferRevalidatesSourceArchiveEvidence(t *testing.T) {
	f, now, in := modelTransferFixture(t)
	raw := jsonLine(map[string]string{"role": "assistant", "content": "이전 요약의 원래 근거"})
	id := ContentHash("transitive-transfer-evidence")
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	previous, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw))}})
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "previous derived summary", ArchiveMessages: []ArchiveMessageRef{previous.Ref}}))
	if err != nil {
		t.Fatal(err)
	}
	pkg := workCheckpointPackage()
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}
	work, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "transitive-work", EventID: "event-1", Package: pkg})
	if err != nil {
		t.Fatal(err)
	}
	in.Work = &Ref{Collection: colWorkCheckpoints, ID: work.ID, Revision: work.Revision}
	if out, e := f.svc.AuthorizeModelArchiveTransfer(testCtx, "transfer-service", in); e != nil || !out.Authorized {
		t.Fatal("baseline", e)
	}
	header, err := f.repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.Delete(testCtx, colSessionArchives, scopeA, id, header.Revision); err != nil {
		t.Fatal(err)
	}
	if out, e := f.svc.AuthorizeModelArchiveTransfer(testCtx, "transfer-service", in); e == nil || out.Authorized {
		t.Fatal("export allowed after previous source archive was removed")
	}
}
