package memory

import (
	"errors"
	"testing"
)

func TestModelInputCandidateTransferExactFence(t *testing.T) {
	for _, mode := range []string{"allowed", "stale", "deleted", "quarantined", "retired", "foreign", "human", "source-restricted"} {
		t.Run(mode, func(t *testing.T) {
			f, c, _ := autoReviewFixture(t)
			f.auth.actors["transfer-service"] = actor("reviewer", roleService, true, "transfer", "read")
			in := ModelInputTransferInput{Scope: scopeA, Refs: []Ref{{Collection: "memory_candidates", ID: c.ID, Revision: c.Revision}}, Destination: "external:anthropic", Content: "exact candidate data"}
			token := "transfer-service"
			switch mode {
			case "stale":
				in.Refs[0].Revision++
			case "foreign":
				in.Scope.ProjectID = "other"
			case "human":
				token = f.human
			case "deleted", "quarantined", "retired":
				if mode == "deleted" {
					c.ContentErased = true
				} else if mode == "quarantined" {
					c.PrivacyQuarantined = true
				} else {
					c.Status = "rejected"
				}
				if err := f.repo.Replace(testCtx, "memory_candidates", c, c.Revision); err != nil {
					t.Fatal(err)
				}
			case "source-restricted":
				source := f.get(f.human, "sources", scopeA, c.Sources[0].SourceID)
				source.Sensitivity = "restricted"
				if err := f.repo.Replace(testCtx, "sources", source, source.Revision); err != nil {
					t.Fatal(err)
				}
			}
			out, err := f.svc.AuthorizeModelInput(testCtx, token, in)
			if mode == "allowed" {
				if err != nil || !out.Authorized || out.ContentHash != ContentHash(in.Content) {
					t.Fatal(out, err)
				}
			} else if err == nil {
				t.Fatal("unsafe transfer accepted")
			}
		})
	}
}
func TestAutomatedInputsRefuseOwnerCredential(t *testing.T) {
	f, c, review := autoReviewFixture(t)
	review.Automated = true
	if _, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, review); !errors.Is(err, ErrDenied) {
		t.Fatal("automated review accepted owner", err)
	}
	in := captureInput(scopeA, "automated-event", CaptureClaim{Subject: "preference", Text: "한국어를 사용한다", Kind: "user", Basis: "explicit_user", Reason: "statement"})
	in.Automated = true
	if _, err := f.svc.Capture(testCtx, f.human, in); !errors.Is(err, ErrDenied) {
		t.Fatal("automated capture accepted owner", err)
	}
	if _, err := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, review); err != nil {
		t.Fatal("configured reviewer blocked", err)
	}
}

func TestModelInputTransitiveArchiveAndSourceDeletion(t *testing.T) {
	for _, mode := range []string{"allowed", "ancestor-deleted", "archive-deleted", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f, _, archive := modelTransferFixture(t)
			ancestor := f.source()
			ancestor.ArchiveMessages = archive.Refs
			if e := f.repo.Replace(testCtx, "sources", ancestor, ancestor.Revision); e != nil {
				t.Fatal(e)
			}
			child := ancestor
			child.ID = "transitive-child"
			child.ArchiveMessages = nil
			child.Text = "derived child evidence"
			child.Sources = refs(ancestor)
			if e := f.repo.Insert(testCtx, "sources", child); e != nil {
				t.Fatal(e)
			}
			in := ModelInputTransferInput{Scope: scopeA, Refs: []Ref{{Collection: "sources", ID: child.ID, Revision: child.Revision}}, Destination: "external:anthropic", Content: "exact transitive evidence"}
			switch mode {
			case "ancestor-deleted":
				if e := f.repo.Delete(testCtx, "sources", scopeA, ancestor.ID, ancestor.Revision); e != nil {
					t.Fatal(e)
				}
			case "archive-deleted":
				r, e := f.repo.Get(testCtx, colSessionArchives, scopeA, archive.Refs[0].ArchiveID)
				if e != nil {
					t.Fatal(e)
				}
				if e = f.repo.Delete(testCtx, colSessionArchives, scopeA, r.ID, r.Revision); e != nil {
					t.Fatal(e)
				}
			case "audit":
				f.repo.failAudit = true
			}
			out, e := f.svc.AuthorizeModelInput(testCtx, "transfer-service", in)
			if mode == "allowed" {
				if e != nil || !out.Authorized {
					t.Fatal(out, e)
				}
			} else if e == nil || out.Authorized || out.ContentHash != "" {
				t.Fatal("unsafe transitive authorization", out, e)
			}
		})
	}
}

func TestModelInputAuthorizationToTransactionRace(t *testing.T) {
	for _, mode := range []string{"candidate-stale", "source-deleted"} {
		t.Run(mode, func(t *testing.T) {
			f, c, _ := autoReviewFixture(t)
			f.auth.actors["transfer-service"] = actor("reviewer", roleService, true, "transfer", "read")
			changed := false
			f.svc.Repo = evaluationChangeRepo{Repository: f.repo, change: func() {
				if changed {
					return
				}
				changed = true
				if mode == "candidate-stale" {
					c.Revision++
					if e := f.repo.Replace(testCtx, "memory_candidates", c, c.Revision-1); e != nil {
						t.Fatal(e)
					}
				} else {
					r, e := f.repo.Get(testCtx, "sources", scopeA, c.Sources[0].SourceID)
					if e != nil {
						t.Fatal(e)
					}
					if e = f.repo.Delete(testCtx, "sources", scopeA, r.ID, r.Revision); e != nil {
						t.Fatal(e)
					}
				}
			}}
			in := ModelInputTransferInput{Scope: scopeA, Refs: []Ref{{Collection: "memory_candidates", ID: c.ID, Revision: c.Revision}}, Destination: "external:anthropic", Content: "exact candidate data"}
			out, e := f.svc.AuthorizeModelInput(testCtx, "transfer-service", in)
			if !changed || e == nil || out.Authorized || out.ContentHash != "" {
				t.Fatal("transaction race authorized", changed, out, e)
			}
		})
	}
}
