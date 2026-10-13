package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Simulate a direct same-revision edit, beyond the adapter's revision guard.

// Ordinary adapter changes increment the semantic revision.

func TestSourceReplayChange(t *testing.T) {
	for _, file := range []bool{false, true} {
		for _, replay := range []bool{false, true} {
			for _, change := range []string{"text", "sensitivity", "expiry", "revision", "delete", "audit"} {
				name := "text/"
				if file {
					name = "file/"
				}
				if replay {
					name += "receipt/"
				} else {
					name += "digest/"
				}
				t.Run(name+change, func(t *testing.T) { exerciseSourceReplayChange(t, newFakeRepo(), file, replay, change) })
			}
		}
	}
}

func exerciseSourceReplayChange(t *testing.T, repo Repository, file, replay bool, change string) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	in := sourceInput(CreateInput{Scope: scopeA, Text: "source replay original", IdempotencyKey: "original"})
	body := []byte("file replay original")
	if file {
		f.svc.BlobDirectory = blobTestDir(t)
	}
	call := func() (Record, error) {
		if !file {
			return f.svc.Create(testCtx, f.human, "sources", in)
		}
		out, err := f.svc.IngestFile(testCtx, f.human, FileIngestInput{Source: in, Artifact: FileArtifactSpec{Kind: "tool_output", MIME: "text/plain"}}, body)
		if err != nil && out.Artifact.ID != "" {
			t.Fatal("failed file response contains artifact")
		}
		return out.Source, err
	}
	src, err := call()
	if err != nil {
		t.Fatal(err)
	}
	if !replay {
		in.IdempotencyKey = ""
	}
	first, err := call()
	if err != nil || first.ID != src.ID || first.Duplicate != !replay {
		t.Fatal("positive control", err)
	}
	changed := false
	var beforeFinal Record
	f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
		if a.Action != "read:sources" || a.ResourceID != src.ID {
			return nil
		}
		changed = true
		current, err := repo.Get(testCtx, "sources", scopeA, src.ID)
		if err != nil {
			return err
		}
		switch change {
		case "delete":
			deleter := &Service{Repo: repo, Auth: f.auth}
			return deleter.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision})
		case "text":
			// Simulate a direct same-revision edit, beyond the adapter's revision guard.
			switch r := repo.(type) {
			case *fakeRepo:
				r.mu.Lock()
				current.Text = "new unpublished content"
				r.view().store["sources"][recKey(scopeA, src.ID)] = current
				r.mu.Unlock()
				return nil
			case *Postgres:
				current.Text = "new unpublished content"
				b, j, err := encodeRecord(current)
				if err != nil {
					return err
				}
				_, err = r.pool.Exec(testCtx, "UPDATE memory_records SET payload=$1, doc=$2 WHERE collection='sources' AND id=$3", b, j, src.ID)
				return err
			default:
				t.Fatal("unsupported test repository")
			}
		case "sensitivity":
			current.Sensitivity = "restricted"
		case "expiry":
			at := time.Now().Add(-time.Second)
			current.ExpiresAt = &at
		case "revision":
			current.Revision++
		case "audit":
			beforeFinal = current
			repo.(*fakeRepo).failAudit = true
			return nil
		}
		// Ordinary adapter changes increment the semantic revision.
		return repo.Replace(testCtx, "sources", current, src.Revision)
	}}
	out, err := call()
	if !changed || err == nil || out.ID != "" || out.Text != "" {
		t.Fatal("stale replay returned", changed, err, out.ID)
	}
	if r, ok := repo.(*fakeRepo); ok {
		r.failAudit = false
	}
	receiptID := "source-receipt-" + ContentHash(structuredText(scopeA)+"\nhuman-1\noriginal")
	if _, err = repo.Get(testCtx, "jobs", scopeA, receiptID); err != nil {
		t.Fatal("earlier receipt lost", err)
	}
	if file {
		digest := ContentHash(string(body))
		if _, err = repo.Get(testCtx, colArtifacts, scopeA, artifactID(scopeA, src.ID, digest)); err != nil {
			t.Fatal("earlier artifact lost", err)
		}
		if stored, e := os.ReadFile(filepath.Join(f.svc.BlobDirectory, digest)); e != nil || string(stored) != string(body) {
			t.Fatal("earlier bytes lost", e)
		}
	}
	if change == "audit" {
		current, err := repo.Get(testCtx, "sources", scopeA, src.ID)
		if err != nil || structuredText(current) != structuredText(src) || current.MemoryReferenceFence != beforeFinal.MemoryReferenceFence {
			t.Fatal("failed final transaction changed source", err)
		}
	}
}
