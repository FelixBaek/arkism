package memory

import (
	"errors"
	"testing"
)

func conflictReviewFixture(t *testing.T, resolution string, reverse bool) (*fixture, Record, Record, CandidateReviewInput) {
	t.Helper()
	f, c, in := autoReviewFixture(t)
	old := reviewedForEvaluation(t, f)
	refs := []Ref{{Collection: colMemories, ID: old.ID, Revision: old.Revision}, {Collection: "memory_candidates", ID: c.ID, Revision: c.Revision}}
	if reverse {
		refs[0], refs[1] = refs[1], refs[0]
	}
	marked, err := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Memories: refs, Action: "mark", Reason: "changed preference"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range marked.Memories {
		if row.ID == old.ID {
			old = row
		} else {
			c = row
		}
	}
	in.ExpectedRevision = c.Revision
	in.Conflict = &CandidateReviewConflict{ID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Memory: Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}, Resolution: resolution, Reason: "explicit correction"}
	if resolution == "keep_old" {
		in.Action = "reject"
		in.Reason = "existing memory is still correct"
	}
	return f, c, old, in
}

func TestCandidateConflictReviewAtomicAndReplay(t *testing.T) {
	for _, resolution := range []string{"replace_old", "keep_both", "keep_old"} {
		for _, reverse := range []bool{false, true} {
			t.Run(resolution+map[bool]string{false: "-normal", true: "-reversed"}[reverse], func(t *testing.T) {
				f, c, old, in := conflictReviewFixture(t, resolution, reverse)
				got, err := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in)
				if err != nil {
					t.Fatal(err)
				}
				current, err := f.repo.Get(testCtx, colMemories, scopeA, old.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.ConflictID != "" || got.ConflictID != "" {
					t.Fatal("unresolved conflict")
				}
				if resolution == "replace_old" {
					if current.Status != "superseded" || current.SupersededBy == nil || current.SupersededBy.ID != got.ID || len(got.Supersedes) != 1 {
						t.Fatal("not replaced")
					}
					for _, r := range f.repo.rows("memory_relations") {
						if r.Kind == "supersedes" && r.CreatedByType != roleService {
							t.Fatal("incorrect actor")
						}
					}
				} else if current.Status != "active" {
					t.Fatal(current.Status)
				}
				if resolution == "keep_old" {
					if got.Status != "rejected" || len(f.repo.rows(colMemories)) != 1 {
						t.Fatal("rejected candidate activated")
					}
				} else {
					evaluationJob(t, f, got)
				}
				jobs := len(f.repo.rows("jobs"))
				replay, err := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in)
				if err != nil || !replay.Duplicate || replay.Revision != got.Revision || len(f.repo.rows("jobs")) != jobs {
					t.Fatal("retry", err)
				}
			})
		}
	}
}

func TestCandidateConflictReviewRollsBackAndRecovers(t *testing.T) {
	f, c, old, in := conflictReviewFixture(t, "replace_old", false)
	jobs := len(f.repo.rows("jobs"))
	f.svc.Repo = evaluationFailureRepo{f.repo, "enqueue"}
	got, err := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in)
	if !errors.Is(err, ErrUnavailable) || got.ID != "" {
		t.Fatal(got, err)
	}
	for _, ref := range []Ref{{Collection: colMemories, ID: old.ID, Revision: old.Revision}, {Collection: "memory_candidates", ID: c.ID, Revision: c.Revision}} {
		r, e := f.repo.Get(testCtx, ref.Collection, scopeA, ref.ID)
		if e != nil || r.Revision != ref.Revision || r.ConflictID != in.Conflict.ID {
			t.Fatal("partial commit", e)
		}
	}
	if len(f.repo.rows("jobs")) != jobs || len(f.repo.rows(colMemories)) != 1 {
		t.Fatal("orphan jobs or memory")
	}
	f.svc.Repo = f.repo
	if _, err = f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); err != nil {
		t.Fatal("recovery", err)
	}
}

func TestCandidateConflictReviewRejectsStaleOrDifferentEndpoint(t *testing.T) {
	for _, mode := range []string{"candidate", "memory", "relation", "endpoint", "policy"} {
		t.Run(mode, func(t *testing.T) {
			f, c, _, in := conflictReviewFixture(t, "replace_old", false)
			switch mode {
			case "candidate":
				in.ExpectedRevision++
			case "memory":
				in.Conflict.Memory.Revision++
			case "relation":
				in.Conflict.ExpectedRevision++
			case "endpoint":
				in.Conflict.Memory.ID = "unrelated"
			case "policy":
				f.svc.MemoryReviewPolicies = nil
			}
			if _, err := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); err == nil {
				t.Fatal("invalid replacement succeeded")
			}
			if len(f.repo.rows(colMemories)) != 1 {
				t.Fatal("created memory")
			}
		})
	}
}

func TestCandidateConflictReviewCannotRevivePrivateCandidate(t *testing.T) {
	for _, resolution := range []string{"keep_old", "keep_both", "replace_old"} {
		for _, erased := range []bool{true, false} {
			f, c, old, in := conflictReviewFixture(t, resolution, false)
			c.ContentErased = erased
			c.PrivacyQuarantined = !erased
			f.repo.store["memory_candidates"][recKey(scopeA, c.ID)] = c
			jobs := len(f.repo.rows("jobs"))
			if _, e := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); !errors.Is(e, ErrNotFound) {
				t.Fatal(resolution, erased, e)
			}
			got, e := f.repo.Get(testCtx, colMemories, scopeA, old.ID)
			if e != nil || got.Revision != old.Revision || got.Status != old.Status || len(f.repo.rows("jobs")) != jobs || len(f.repo.rows(colMemories)) != 1 {
				t.Fatal("privacy mutation escaped")
			}
		}
	}
}
