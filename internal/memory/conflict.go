package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

type ConflictInput struct {
	Scope            Scope  `json:"scope"`
	Memories         []Ref  `json:"memories"`
	Action           string `json:"action"`
	Reason           string `json:"reason"`
	Hold             string `json:"hold,omitempty"`
	ConflictID       string `json:"conflict_id,omitempty"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
	Resolution       string `json:"resolution,omitempty"`
}
type ConflictOutput struct {
	Replacement *Record  `json:"replacement,omitempty"`
	Memories    []Record `json:"memories"`
	Relation    Record   `json:"relation"`
}
type ConflictDetails struct {
	Replacement      *Ref   `json:"replacement,omitempty" bson:"replacement,omitempty"`
	Missing          []Ref  `json:"missing,omitempty" bson:"missing,omitempty"`
	Hold             string `json:"hold" bson:"hold"`
	Resolution       string `json:"resolution,omitempty" bson:"resolution,omitempty"`
	ResolutionReason string `json:"resolution_reason,omitempty" bson:"resolution_reason,omitempty"`
	ResolvedBy       string `json:"resolved_by,omitempty" bson:"resolved_by,omitempty"`
}

func (s *Service) Conflict(ctx context.Context, token string, in ConflictInput) (ConflictOutput, error) {
	in, winner, err := validConflict(in)
	if err != nil {
		return ConflictOutput{}, err
	}
	principal, err := s.conflictPrincipal(ctx, token, in)
	if err != nil {
		return ConflictOutput{}, err
	}
	var out ConflictOutput
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		out, e = applyConflict(ctx, tx, in, principal, winner)
		return e
	})
	if err != nil {
		return ConflictOutput{}, err
	}
	return out, nil
}

// validConflict checks the input shape and returns it with defaults filled in,
// plus the index of the memory that supersedes the other (-1 for none).
func validConflict(in ConflictInput) (ConflictInput, int, error) {
	if !validConflictRefs(in) {
		return in, -1, ErrInvalid
	}
	switch in.Action {
	case "mark":
		if in.Hold == "" {
			in.Hold = "both"
		}
		if in.Hold != "both" && in.Hold != "first" && in.Hold != "second" || in.ConflictID != "" || in.ExpectedRevision != 0 || in.Resolution != "" {
			return in, -1, ErrInvalid
		}
		return in, -1, nil
	case "resolve":
		winner, ok := validResolve(in)
		if !ok {
			return in, -1, ErrInvalid
		}
		return in, winner, nil
	}
	return in, -1, ErrInvalid
}

// validResolve: a resolve names its conflict and revision, and a supersession is between two memories.
func validResolve(in ConflictInput) (int, bool) {
	if in.Hold != "" || in.ConflictID == "" || len(in.ConflictID) > 256 || in.ExpectedRevision < 1 {
		return -1, false
	}
	w, ok := resolutionWinner[in.Resolution]
	if !ok || w >= 0 && (in.Memories[0].Collection != "memories" || in.Memories[1].Collection != "memories") {
		return -1, false
	}
	return w, true
}

// resolutionWinner: the member that supersedes the other, -1 when none does.
var resolutionWinner = map[string]int{"keep_first": -1, "keep_second": -1, "keep_both": -1, "reject_both": -1, "first_supersedes_second": 0, "second_supersedes_first": 1}

func validConflictRefs(in ConflictInput) bool {
	if len(in.Memories) != 2 || in.Memories[0].ID == in.Memories[1].ID || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2000 {
		return false
	}
	for _, ref := range in.Memories {
		if ref.Collection != "memories" && ref.Collection != "memory_candidates" || ref.ID == "" || len(ref.ID) > 256 || ref.Revision < 1 {
			return false
		}
	}
	return true
}

// conflictPrincipal: both memories must be changed by the same human, and the reason must hold no secret.
func (s *Service) conflictPrincipal(ctx context.Context, token string, in ConflictInput) (string, error) {
	if containsSecret(structuredText(in)) {
		return "", ErrSecretDetected
	}
	principal := ""
	for _, ref := range in.Memories {
		d, e := s.authorize(ctx, token, "transition:"+ref.Collection, in.Scope, ref.ID, structuredText(in))
		if e != nil {
			return "", e
		}
		if d.Role != "human" || !d.ContentSafe || principal != "" && d.PrincipalID != principal {
			return "", ErrDenied
		}
		principal = d.PrincipalID
	}
	return principal, nil
}

// conflictRun is one mark or resolve while it is applied.
type conflictRun struct {
	in        ConflictInput
	principal string
	role      string
	winner    int
	rows      []Record
	base      []string // status each member returns to: active or candidate
	missing   []bool   // erased member, allowed only while resolving
	now       time.Time
}

// applyConflict marks or resolves inside the caller's transaction, so a caller
// can mark and resolve (and record its own decision) as one change.
func applyConflict(ctx context.Context, tx Tx, in ConflictInput, principal string, winner int) (ConflictOutput, error) {
	return applyConflictAs(ctx, tx, in, principal, roleHuman, winner)
}
func applyConflictAs(ctx context.Context, tx Tx, in ConflictInput, principal, role string, winner int) (ConflictOutput, error) {
	c := &conflictRun{in: in, principal: principal, role: role, winner: winner, rows: make([]Record, 2), base: []string{"active", "active"}, missing: make([]bool, 2)}
	for i, ref := range in.Memories {
		if ref.Collection == "memory_candidates" {
			c.base[i] = "candidate"
		}
	}
	if err := c.load(ctx, tx); err != nil {
		return ConflictOutput{}, err
	}
	c.now = time.Now().UTC().Truncate(time.Millisecond)
	var rel Record
	var err error
	if in.Action == "mark" {
		rel, err = c.mark(ctx, tx)
	} else {
		rel, err = c.resolve(ctx, tx)
	}
	if err != nil {
		return ConflictOutput{}, err
	}
	return c.write(ctx, tx, rel)
}

func (c *conflictRun) load(ctx context.Context, tx Tx) error {
	for i, ref := range c.in.Memories {
		r, e := tx.Get(ctx, ref.Collection, c.in.Scope, ref.ID)
		if errors.Is(e, ErrNotFound) && c.in.Action == "resolve" {
			c.missing[i] = true
			r = Record{ID: ref.ID, Scope: c.in.Scope, Revision: ref.Revision, ConflictID: c.in.ConflictID}
		} else if e != nil {
			return e
		}
		if r.Revision != ref.Revision {
			return ErrConflict
		}
		c.rows[i] = r
	}
	return nil
}

func (c *conflictRun) expired(r Record) bool {
	return r.ExpiresAt != nil && !r.ExpiresAt.After(c.now)
}

// markable: each member is in its base state, approved if a memory, outside any conflict, unexpired and well sourced.
func (c *conflictRun) markable(ctx context.Context, tx Tx) error {
	for i, r := range c.rows {
		if r.Status != c.base[i] || c.base[i] == "active" && !verifiedMemory(r) || r.ConflictID != "" || c.expired(r) {
			return ErrConflict
		}
		if e := checkSources(ctx, tx, r); e != nil {
			return e
		}
	}
	return nil
}

func (c *conflictRun) mark(ctx context.Context, tx Tx) (Record, error) {
	if e := c.markable(ctx, tx); e != nil {
		return Record{}, e
	}
	refs := unionSources(c.rows)
	if len(refs) == 0 || len(refs) > 64 {
		return Record{}, ErrInvalid
	}
	rel := Record{ID: ID(), Scope: c.in.Scope, Kind: "conflicts_with", Status: "active", Revision: 1, Text: c.in.Reason, Sources: refs, Sensitivity: "private", Conflict: &ConflictDetails{Hold: c.in.Hold}, CreatedAt: c.now, UpdatedAt: c.now, CreatedBy: c.principal, CreatedByType: c.role}
	for i := range c.rows {
		c.rows[i].ConflictID = rel.ID
		if c.holds(i) {
			c.rows[i].Status = "conflicted"
		}
	}
	return rel, nil
}

func (c *conflictRun) holds(i int) bool {
	h := c.in.Hold
	return h == "both" || h == "first" && i == 0 || h == "second" && i == 1
}

// unionSources: the members' sources, each source once, in order.
func unionSources(rows []Record) []SourceRef {
	refs := []SourceRef{}
	seen := map[string]bool{}
	for _, r := range rows {
		for _, src := range r.Sources {
			if !seen[src.SourceID] {
				refs = append(refs, src)
				seen[src.SourceID] = true
			}
		}
	}
	return refs
}

func (c *conflictRun) resolve(ctx context.Context, tx Tx) (Record, error) {
	rel, e := c.openRelation(ctx, tx)
	if e != nil {
		return Record{}, e
	}
	for i := range c.rows {
		if e := c.resolveRow(ctx, tx, rel, i); e != nil {
			return Record{}, e
		}
	}
	detail := *rel.Conflict
	detail.Missing = nil
	for i, absent := range c.missing {
		if absent {
			detail.Missing = append(detail.Missing, c.in.Memories[i])
		}
	}
	detail.Resolution = c.in.Resolution
	detail.ResolutionReason = c.in.Reason
	detail.ResolvedBy = c.principal
	rel.Conflict = &detail
	rel.Status = "resolved"
	rel.Revision++
	rel.UpdatedAt = c.now
	return rel, nil
}

func (c *conflictRun) openRelation(ctx context.Context, tx Tx) (Record, error) {
	rel, e := tx.Get(ctx, "memory_relations", c.in.Scope, c.in.ConflictID)
	if e != nil {
		return Record{}, e
	}
	if rel.ContentErased {
		if rel, e = conflictControlRecord(rel); e != nil {
			return Record{}, e
		}
	}
	if rel.Kind != "conflicts_with" || rel.Status != "active" || rel.Revision != c.in.ExpectedRevision || rel.Conflict == nil || len(rel.Relations) != 2 {
		return Record{}, ErrConflict
	}
	if c.winner >= 0 && (c.missing[0] || c.missing[1]) {
		return Record{}, ErrConflict
	}
	return rel, nil
}

func (c *conflictRun) keeps(i int) bool {
	r := c.in.Resolution
	return c.winner == i || r == "keep_both" || r == "keep_first" && i == 0 || r == "keep_second" && i == 1
}

func (c *conflictRun) resolveRow(ctx context.Context, tx Tx, rel Record, i int) error {
	r := c.rows[i]
	if r.ConflictID != rel.ID || rel.Relations[i] != c.in.Memories[i] || !c.missing[i] && r.Status != c.base[i] && r.Status != "conflicted" {
		return ErrConflict
	}
	keep := c.keeps(i)
	if c.missing[i] {
		if keep {
			return ErrConflict
		}
		return nil
	}
	c.rows[i].Status = "rejected"
	if c.winner >= 0 && i != c.winner {
		c.rows[i].Status = "superseded"
	}
	if keep {
		if e := c.checkKept(ctx, tx, r, i); e != nil {
			return e
		}
		c.rows[i].Status = c.base[i]
	}
	c.rows[i].ConflictID = ""
	return nil
}

func (c *conflictRun) checkKept(ctx context.Context, tx Tx, r Record, i int) error {
	if c.base[i] == "active" && !verifiedMemory(r) || c.expired(r) {
		return ErrConflict
	}
	if e := checkSources(ctx, tx, r); e != nil {
		return e
	}
	// References to either member change revision in this transaction.
	for _, ref := range r.Relations {
		for _, member := range c.in.Memories {
			if ref.Collection == member.Collection && ref.ID == member.ID {
				return ErrConflict
			}
		}
	}
	return checkRelations(ctx, tx, r)
}

func (c *conflictRun) write(ctx context.Context, tx Tx, rel Record) (ConflictOutput, error) {
	for i := range c.rows {
		if !c.missing[i] {
			c.rows[i].Revision++
			c.rows[i].UpdatedAt = c.now
		}
	}
	var replacement *Record
	if c.winner >= 0 {
		r, e := linkSupersession(&c.rows[1-c.winner], &c.rows[c.winner], c.in.Reason, c.principal, c.now)
		if e != nil {
			return ConflictOutput{}, e
		}
		r.CreatedByType = c.role
		replacement = &r
		rel.Conflict.Replacement = &Ref{Collection: "memory_relations", ID: r.ID, Revision: r.Revision}
		if e = insertSourceRecord(ctx, tx, "memory_relations", r); e != nil {
			return ConflictOutput{}, e
		}
	}
	survivors, e := c.writeRows(ctx, tx, &rel)
	if e != nil {
		return ConflictOutput{}, e
	}
	if e := c.writeRelation(ctx, tx, rel); e != nil {
		return ConflictOutput{}, e
	}
	if c.in.Action == "resolve" {
		if e := recordDecision(ctx, tx, c.in.Scope, c.principal, "conflict_resolved", rel.ID, c.in.Resolution, c.in.Reason); e != nil {
			return ConflictOutput{}, e
		}
	}
	if e := audit(ctx, tx, c.in.Scope, c.principal, "conflict:"+c.in.Action, rel.ID); e != nil {
		return ConflictOutput{}, e
	}
	return ConflictOutput{Memories: survivors, Relation: rel, Replacement: replacement}, nil
}

func (c *conflictRun) writeRows(ctx context.Context, tx Tx, rel *Record) ([]Record, error) {
	rel.Relations = nil
	survivors := []Record{}
	for i, row := range c.rows {
		ref := c.in.Memories[i]
		if c.missing[i] {
			rel.Relations = append(rel.Relations, ref)
			continue
		}
		if row.Sensitivity == "restricted" {
			rel.Sensitivity = "restricted"
		}
		if e := tx.Replace(ctx, ref.Collection, row, ref.Revision); e != nil {
			return nil, e
		}
		rel.Relations = append(rel.Relations, Ref{Collection: ref.Collection, ID: row.ID, Revision: row.Revision})
		survivors = append(survivors, row)
		if row.Status == "active" {
			if e := enqueue(ctx, tx, "memories", row); e != nil {
				return nil, e
			}
		}
	}
	return survivors, nil
}

func (c *conflictRun) writeRelation(ctx context.Context, tx Tx, rel Record) error {
	switch {
	case c.in.Action == "mark":
		return insertSourceRecord(ctx, tx, "memory_relations", rel)
	case rel.ContentErased:
		return tx.Delete(ctx, "memory_relations", c.in.Scope, rel.ID, c.in.ExpectedRevision)
	default:
		return tx.Replace(ctx, "memory_relations", rel, c.in.ExpectedRevision)
	}
}
