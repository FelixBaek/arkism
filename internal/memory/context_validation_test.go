package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validationInput(r Record) ContextValidationInput {
	return ContextValidationInput{Scope: r.Scope, Renderer: ContextValidationRenderer, BlockHash: ContentHash("synthetic trusted rendering"), Refs: []ContextValidationRef{{Collection: colMemories, ID: r.ID, Revision: r.Revision, Digest: ContentHash(recordContent(r))}}}
}
func validationReplay(t *testing.T, f *fixture, in ContextValidationInput) ContextValidationInput {
	t.Helper()
	out, e := f.svc.ValidateContext(testCtx, f.human, in)
	if e != nil || !out.Integrity || len(out.Items) != len(in.Refs) {
		t.Fatal("initial current snapshot", e, out)
	}
	in.Fingerprint = out.Fingerprint
	for i, row := range out.Items {
		if row.Status != "valid" || row.Fingerprint == "" {
			t.Fatal("missing canonical snapshot")
		}
		in.Refs[i].Fingerprint = row.Fingerprint
	}
	return in
}
func assertValidationUnavailable(t *testing.T, out ContextValidationOutput, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Items) != 1 || out.Items[0].Status != "unavailable" || out.Items[0].Fingerprint != "" {
		t.Fatal("stale reference escaped or current state leaked", out)
	}
}
func exerciseContextValidation(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	// Older imported records can lack span hashes even though new candidate
	// creation requires them. Their dependency fingerprint must still detect edits.
	r := f.activeMemory("synthetic approved standing rule", "synthetic")
	r.ID = ID()
	r.Sources = []SourceRef{{SourceID: r.Sources[0].SourceID, Locator: "synthetic-legacy-no-span"}}
	if e := repo.Insert(testCtx, colMemories, r); e != nil {
		t.Fatal(e)
	}
	in := validationReplay(t, f, validationInput(r))
	out, e := f.svc.ValidateContext(testCtx, f.human, in)
	if e != nil || !out.Integrity || out.Fingerprint != in.Fingerprint || out.Items[0].Status != "valid" {
		t.Fatal("unchanged snapshot must be reusable", e, out)
	}
	// Changing only the source must invalidate replay even though the memory's
	// revision, digest and management GET remain unchanged.
	source, e := repo.Get(testCtx, "sources", scopeA, r.Sources[0].SourceID)
	if e != nil {
		t.Fatal(e)
	}
	source.Text = "synthetic corrected source"
	source.Revision++
	if e = repo.Replace(testCtx, "sources", source, source.Revision-1); e != nil {
		t.Fatal(e)
	}
	current, e := repo.Get(testCtx, colMemories, scopeA, r.ID)
	if e != nil || current.Revision != r.Revision || recordContent(current) != recordContent(r) {
		t.Fatal("fixture unexpectedly changed memory")
	}
	out, e = f.svc.ValidateContext(testCtx, f.human, in)
	assertValidationUnavailable(t, out, e)
	if !out.Integrity {
		t.Fatal("current eligibility and stored integrity were conflated")
	}
	// An old rendering never gets a fresh dependency hash attached during replay.
	again, e := f.svc.ValidateContext(testCtx, f.human, in)
	assertValidationUnavailable(t, again, e)
}
func TestContextValidationSourceCorrectionWithoutMemoryRevision(t *testing.T) {
	exerciseContextValidation(t, newFakeRepo())
}

func TestContextValidationWithdrawalAndNoExistenceOracle(t *testing.T) {
	for _, mode := range []string{"deleted", "quarantined", "expired", "approval_revoked", "superseded", "memory_revision", "source_quarantined", "source_erased"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			r := f.activeMemory("synthetic sensitive withdrawn rule")
			in := validationReplay(t, f, validationInput(r))
			col := "memories"
			target := r
			if strings.HasPrefix(mode, "source_") {
				col = "sources"
				var e error
				target, e = f.repo.Get(testCtx, col, scopeA, r.Sources[0].SourceID)
				if e != nil {
					t.Fatal(e)
				}
			}
			switch mode {
			case "deleted":
				if e := f.repo.Delete(testCtx, col, scopeA, target.ID, target.Revision); e != nil {
					t.Fatal(e)
				}
			default:
				switch mode {
				case "quarantined", "source_quarantined":
					target.PrivacyQuarantined = true
				case "expired":
					v := time.Now().Add(-time.Minute)
					target.ExpiresAt = &v
				case "approval_revoked":
					target.ApprovedBy = ""
				case "superseded":
					target.Status = "superseded"
				case "memory_revision":
					target.Revision++
					target.Text = "synthetic new revision"
				case "source_erased":
					target.ContentErased = true
				}
				if e := f.repo.Replace(testCtx, col, target, rRevision(f, t, col, target.ID)); e != nil {
					t.Fatal(e)
				}
			}
			out, e := f.svc.ValidateContext(testCtx, f.human, in)
			assertValidationUnavailable(t, out, e)
			// Missing and wrong-scope IDs cannot reveal a current digest or revision.
			missing := validationInput(r)
			missing.Refs[0].ID = "synthetic-missing"
			out, e = f.svc.ValidateContext(testCtx, f.human, missing)
			assertValidationUnavailable(t, out, e)
			if out.Fingerprint != "" || out.Integrity {
				t.Fatal("unavailable initial ref was sealed")
			}
		})
	}
}
func rRevision(f *fixture, t *testing.T, col, id string) int64 {
	t.Helper()
	r, e := f.repo.Get(testCtx, col, scopeA, id)
	if e != nil {
		t.Fatal(e)
	}
	return r.Revision
}

func TestContextValidationIntegrityIdentityAndInputBounds(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("synthetic immutable body")
	in := validationReplay(t, f, validationInput(r))
	for _, mode := range []string{"body_hash", "dependency_hash", "different_principal", "different_role", "scope"} {
		t.Run(mode, func(t *testing.T) {
			changed := in
			changed.Refs = append([]ContextValidationRef{}, in.Refs...)
			token := f.human
			switch mode {
			case "body_hash":
				changed.BlockHash = ContentHash("tampered rendered text")
			case "dependency_hash":
				changed.Refs[0].Fingerprint = ContentHash("tampered dependencies")
			case "different_principal":
				token = f.human2
			case "different_role":
				token = f.agent
			case "scope":
				changed.Scope.ProjectID = "synthetic-other"
			}
			out, e := f.svc.ValidateContext(testCtx, token, changed)
			if e != nil && !errors.Is(e, ErrDenied) {
				t.Fatal(e)
			}
			if out.Integrity || out.Fingerprint != "" {
				t.Fatal("seal survived changed binding")
			}
			for _, v := range out.Items {
				if v.Status != "unavailable" || v.Fingerprint != "" {
					t.Fatal("unverified seal disclosed state")
				}
			}
		})
	}
	for _, mode := range []string{"duplicate", "too_many", "zero_revision", "bad_digest", "renderer", "partial_evidence"} {
		t.Run(mode, func(t *testing.T) {
			bad := validationInput(r)
			switch mode {
			case "duplicate":
				bad.Refs = append(bad.Refs, bad.Refs[0])
			case "too_many":
				bad.Refs = make([]ContextValidationRef, 129)
			case "zero_revision":
				bad.Refs[0].Revision = 0
			case "bad_digest":
				bad.Refs[0].Digest = "bad"
			case "renderer":
				bad.Renderer = "older-renderer"
			case "partial_evidence":
				bad.Refs[0].Fingerprint = ContentHash("not sealed")
			}
			if _, e := f.svc.ValidateContext(testCtx, f.human, bad); !errors.Is(e, ErrInvalid) {
				t.Fatal("malformed input accepted", e)
			}
		})
	}
	// An empty non-memory block stays canonical across null/empty JSON refs.
	empty := validationInput(r)
	empty.Refs = nil
	first, e := f.svc.ValidateContext(testCtx, f.human, empty)
	if e != nil || !first.Integrity {
		t.Fatal(e)
	}
	empty.Fingerprint = first.Fingerprint
	empty.Refs = []ContextValidationRef{}
	second, e := f.svc.ValidateContext(testCtx, f.human, empty)
	if e != nil || !second.Integrity {
		t.Fatal("empty snapshot representation changed", e)
	}
}

type validationRaceRepo struct {
	Repository
	before func()
	fired  bool
}

func (r *validationRaceRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if !r.fired {
		r.fired = true
		r.before()
	}
	return r.Repository.Transaction(ctx, fn)
}
func TestContextValidationFencesAuthorizationSnapshot(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("synthetic pre-race body")
	in := validationReplay(t, f, validationInput(r))
	f.svc.Repo = &validationRaceRepo{Repository: f.repo, before: func() {
		r.PrivacyQuarantined = true
		if e := f.repo.Replace(testCtx, colMemories, r, r.Revision); e != nil {
			t.Fatal(e)
		}
	}}
	out, e := f.svc.ValidateContext(testCtx, f.human, in)
	assertValidationUnavailable(t, out, e)
}
