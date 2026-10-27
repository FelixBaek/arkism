package memory

import (
	"context"
	"strings"
	"time"
)

type SupersedeInput struct {
	Tags                        []string    `json:"tags,omitempty"`
	Scope                       Scope       `json:"scope"`
	ExpectedRevision            int64       `json:"expected_revision"`
	Reason                      string      `json:"reason"`
	ReplacementMemoryID         string      `json:"replacement_memory_id,omitempty"`
	ReplacementExpectedRevision int64       `json:"replacement_expected_revision,omitempty"`
	NewContent                  string      `json:"new_content,omitempty"`
	Sources                     []SourceRef `json:"sources,omitempty"`
	Sensitivity                 string      `json:"sensitivity,omitempty"`
}
type SupersedeOutput struct {
	Old      Record `json:"old"`
	New      Record `json:"new"`
	Relation Record `json:"relation"`
}

func (s *Service) Supersede(ctx context.Context, token, id string, in SupersedeInput) (SupersedeOutput, error) {
	var out SupersedeOutput
	if !validTags(in.Tags) || id == "" || len(id) > 256 || in.ExpectedRevision < 1 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2000 {
		return out, ErrInvalid
	}
	existing := in.ReplacementMemoryID != ""
	var clean CreateInput
	var inspection Inspection
	if existing {
		if len(in.Tags) != 0 || in.ReplacementMemoryID == id || len(in.ReplacementMemoryID) > 256 || in.ReplacementExpectedRevision < 1 || in.NewContent != "" || len(in.Sources) != 0 || in.Sensitivity != "" {
			return out, ErrInvalid
		}
	} else {
		var e error
		clean, inspection, e = inspectSupersedeContent(in)
		if e != nil {
			return out, e
		}
	}
	actor, e := s.authorizeSupersede(ctx, token, id, in, existing)
	if e != nil {
		return out, e
	}
	e = s.Repo.Transaction(s.digestCtx(ctx), func(ctx context.Context, tx Tx) error {
		out = SupersedeOutput{}
		old, e := tx.Get(ctx, "memories", in.Scope, id)
		if e != nil {
			return e
		}
		if old.ConflictID != "" || old.Revision != in.ExpectedRevision || old.Status != "active" && old.Status != "conflicted" {
			return ErrConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		var next Record
		if existing {
			next, e = loadSupersedeReplacement(ctx, tx, in, old)
		} else {
			next, e = newSupersedeMemory(in, old, clean, inspection, actor, now)
		}
		if e != nil {
			return e
		}
		if e = checkSources(ctx, tx, next); e != nil {
			return e
		}
		if e = memoryProvenance(ctx, tx, next); e != nil {
			return e
		}
		old.Status = "superseded"
		old.Revision++
		old.UpdatedAt = now
		next.UpdatedAt = now
		relation, e := linkSupersession(&old, &next, in.Reason, actor.PrincipalID, now)
		if e != nil {
			return e
		}
		if e = tx.Replace(ctx, "memories", old, in.ExpectedRevision); e != nil {
			return e
		}
		if existing {
			e = tx.Replace(ctx, "memories", next, in.ReplacementExpectedRevision)
		} else {
			e = insertSourceRecord(ctx, tx, "memories", next)
		}
		if e != nil {
			return e
		}
		if e = insertSourceRecord(ctx, tx, "memory_relations", relation); e != nil {
			return e
		}
		if e = enqueue(ctx, tx, "memories", next); e != nil {
			return e
		}
		if e = staleCascade(ctx, tx, old, next, actor.PrincipalID, now); e != nil {
			return e
		}
		if e = audit(ctx, tx, in.Scope, actor.PrincipalID, "supersede:memories", old.ID); e != nil {
			return e
		}
		out = SupersedeOutput{Old: old, New: next, Relation: relation}
		return nil
	})
	if e != nil {
		return SupersedeOutput{}, e
	}
	return out, nil
}

func inspectSupersedeContent(in SupersedeInput) (CreateInput, Inspection, error) {
	if in.ReplacementExpectedRevision != 0 || len(in.Sources) < 1 || len(in.Sources) > 64 {
		return CreateInput{}, Inspection{}, ErrInvalid
	}
	for _, ref := range in.Sources {
		if ref.Span == nil {
			return CreateInput{}, Inspection{}, ErrInvalid
		}
	}
	return inspectCreate(CreateInput{Tags: in.Tags, Scope: in.Scope, Text: in.NewContent, Sources: in.Sources, Sensitivity: in.Sensitivity})
}

func (s *Service) authorizeSupersede(ctx context.Context, token, id string, in SupersedeInput, existing bool) (Decision, error) {
	if containsSecret(structuredText(in)) || containsSecret(id) {
		return Decision{}, ErrSecretDetected
	}
	actor, e := s.authorize(ctx, token, "transition:memories", in.Scope, id, structuredText(in))
	if e != nil {
		return Decision{}, e
	}
	if actor.Role != "human" || !actor.ContentSafe {
		return Decision{}, ErrDenied
	}
	if existing {
		d, e := s.authorize(ctx, token, "transition:memories", in.Scope, in.ReplacementMemoryID, "")
		if e != nil {
			return Decision{}, e
		}
		if d.Role != "human" || d.PrincipalID != actor.PrincipalID {
			return Decision{}, ErrDenied
		}
	}
	return actor, nil
}

func loadSupersedeReplacement(ctx context.Context, tx Tx, in SupersedeInput, old Record) (Record, error) {
	next, e := tx.Get(ctx, "memories", in.Scope, in.ReplacementMemoryID)
	if e != nil {
		return Record{}, e
	}
	if next.ConflictID != "" || next.Revision != in.ReplacementExpectedRevision || !live(next) || !verifiedMemory(next) {
		return Record{}, ErrConflict
	}
	for _, ref := range next.Relations {
		if ref.Collection == "memories" && ref.ID == old.ID {
			return Record{}, ErrConflict
		}
	}
	if e = checkRelations(ctx, tx, next); e != nil {
		return Record{}, e
	}
	next.Revision++
	return next, nil
}

func newSupersedeMemory(in SupersedeInput, old Record, clean CreateInput, inspection Inspection, actor Decision, now time.Time) (Record, error) {
	if !validCreateKind("memory_candidates", old.Kind) {
		return Record{}, ErrInvalid
	}
	next := Record{Tags: clean.Tags, ID: ID(), Scope: in.Scope, Kind: old.Kind, Status: "active", VerificationStatus: VerificationVerified, Revision: 1, Text: clean.Text, Sources: clean.Sources, SourceEventID: clean.SourceEventID, TraceID: clean.TraceID, Sensitivity: clean.Sensitivity, Inspection: &inspection, CreatedAt: now, UpdatedAt: now, EffectiveAt: &now, CreatedBy: actor.PrincipalID, CreatedByType: "human", ApprovedBy: actor.PrincipalID, TrustLevel: "user_verified", Trust: 1, ContentHash: ContentHash(clean.Text)}
	if next.SourceEventID == "" && len(next.Sources) > 0 {
		next.SourceEventID = next.Sources[0].SourceID
	}
	return next, nil
}

// Both callers set final states/revisions before constructing historical links.
func linkSupersession(old, next *Record, reason, actor string, now time.Time) (Record, error) {
	if old.Scope != next.Scope || old.ID == next.ID || old.Status != "superseded" || next.Status != "active" || len(next.Supersedes) >= 64 {
		return Record{}, ErrInvalid
	}
	if !liveAt(*next, now) {
		return Record{}, ErrConflict
	}
	oldRef := Ref{Collection: "memories", ID: old.ID, Revision: old.Revision}
	nextRef := Ref{Collection: "memories", ID: next.ID, Revision: next.Revision}
	old.SupersededBy = &nextRef
	next.Supersedes = append(append([]Ref(nil), next.Supersedes...), oldRef)
	sensitivity := "internal"
	for _, value := range []string{old.Sensitivity, next.Sensitivity} {
		if value == "restricted" {
			sensitivity = "restricted"
		} else if sensitivity != "restricted" && (value == "private" || value == "" || !validSensitivity(value)) {
			sensitivity = "private"
		}
	}
	if personalPattern.MatchString(reason) && sensitivity != "restricted" {
		sensitivity = "private"
	}
	relation := Record{ID: ID(), Scope: old.Scope, Kind: "supersedes", Status: "active", Revision: 1, Text: reason, Sources: next.Sources, Relations: []Ref{nextRef, oldRef}, Sensitivity: sensitivity, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, CreatedByType: "human"}
	return relation, nil
}
