package memory

// What dreaming did, and undoing it: a person lists the runs (each change with
// the status before and after, reason and confidence, and what was skipped) and
// undoes one change or a whole run. Undo puts each row's status back, only while
// no one has changed the row since (a deleted row counts as changed); an approval the dream made is
// withdrawn (the memory moves to rejected) and a pair it merged is dismissed, so
// the next dream leaves both alone. Every row undo touches is tagged so the
// dream never redoes it.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"
)

type DreamRunItem struct {
	ID  string   `json:"id"`
	Run DreamRun `json:"run"`
}

type DreamRunPage struct {
	Mode  string         `json:"mode"` // auto | manual (MEMORY_DREAM)
	Items []DreamRunItem `json:"items"`
}

// DreamUndoInput undoes one change (its index in the run's changes) or, without Change, the whole run.
type DreamUndoInput struct {
	Scope  Scope `json:"scope"`
	Change *int  `json:"change,omitempty"`
}

// DreamUndoResult names the changes undone now and those a later write blocks.
type DreamUndoResult struct {
	Undone    []int        `json:"undone"`
	Conflicts []int        `json:"conflicts"`
	Run       DreamRunItem `json:"run"`
}

// ListDreamRuns returns the newest dream runs of a scope (human principals only).
func (s *Service) ListDreamRuns(ctx context.Context, token string, scope Scope, limit int) (DreamRunPage, error) {
	if limit < 1 || limit > dreamRunsMax {
		return DreamRunPage{}, ErrInvalid
	}
	if _, err := s.opsAuthorize(ctx, token, "read:"+colMaintenance, scope, "", roleHuman); err != nil {
		return DreamRunPage{}, err
	}
	rows, err := s.Repo.ListRecordsByStatus(ctx, scope, colJobs, dreamRunStatus, limit)
	if err != nil {
		return DreamRunPage{}, err
	}
	out := DreamRunPage{Mode: "manual", Items: []DreamRunItem{}}
	if dreamConfig().auto {
		out.Mode = "auto"
	}
	for _, r := range rows {
		if r.DreamRun != nil {
			out.Items = append(out.Items, DreamRunItem{ID: r.ID, Run: *r.DreamRun})
		}
	}
	return out, nil
}

// UndoDream undoes one change or a whole run, newest change first.
func (s *Service) UndoDream(ctx context.Context, token, runID string, in DreamUndoInput) (DreamUndoResult, error) {
	out := DreamUndoResult{Undone: []int{}, Conflicts: []int{}}
	d, err := s.opsAuthorize(ctx, token, "write:"+colMaintenance, in.Scope, runID, roleHuman)
	if err != nil {
		return out, err
	}
	if _, err = s.opsAuthorize(ctx, token, "transition:"+colMemories, in.Scope, "", roleHuman); err != nil {
		return out, err
	}
	run, err := s.Repo.Get(ctx, colJobs, in.Scope, runID)
	if err != nil {
		return out, err
	}
	if run.Status != dreamRunStatus || run.DreamRun == nil {
		return out, ErrNotFound
	}
	todo := []int{}
	for i := len(run.DreamRun.Changes) - 1; i >= 0; i-- {
		if in.Change == nil || *in.Change == i {
			todo = append(todo, i)
		}
	}
	if in.Change != nil && len(todo) == 0 {
		return out, ErrInvalid
	}
	for _, i := range todo {
		err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return undoDreamChange(ctx, tx, in.Scope, runID, i, d.PrincipalID)
		})
		switch {
		case errors.Is(err, errDreamUndone):
		case errors.Is(err, ErrConflict):
			out.Conflicts = append(out.Conflicts, i)
		case err != nil:
			return out, err
		default:
			out.Undone = append(out.Undone, i)
		}
	}
	if run, err = s.Repo.Get(ctx, colJobs, in.Scope, runID); err != nil {
		return out, err
	}
	out.Run = DreamRunItem{ID: run.ID, Run: *run.DreamRun}
	return out, nil
}

var errDreamUndone = errors.New("dream change already undone")

// undoDreamChange restores every row of change i and marks it undone, in one transaction.
func undoDreamChange(ctx context.Context, tx Tx, scope Scope, runID string, i int, principal string) error {
	run, err := tx.Get(ctx, colJobs, scope, runID)
	if err != nil {
		return err
	}
	if run.DreamRun == nil || i >= len(run.DreamRun.Changes) {
		return ErrConflict
	}
	c := &run.DreamRun.Changes[i]
	if c.UndoneAt != nil {
		return errDreamUndone
	}
	now := time.Now().UTC()
	for j := len(c.Rows) - 1; j >= 0; j-- {
		if err = undoDreamRow(ctx, tx, scope, c.Rows[j], principal, now); err != nil {
			return err
		}
	}
	c.UndoneAt, c.UndoneBy = &now, principal
	prev := run.Revision
	run.Revision, run.UpdatedAt = prev+1, now
	if err = tx.Replace(ctx, colJobs, run, prev); err != nil {
		return err
	}
	return audit(ctx, tx, scope, principal, "dream:undo", runID+"#"+strconv.Itoa(i))
}

// undoDreamRow puts one row back, only at the revision the dream left it.
func undoDreamRow(ctx context.Context, tx Tx, scope Scope, row dreamRow, principal string, now time.Time) error {
	cur, err := tx.Get(ctx, row.Collection, scope, row.ID)
	if errors.Is(err, ErrNotFound) {
		return ErrConflict // deleted since: nothing to put back, the rest of the run still undoes
	}
	if err != nil {
		return err
	}
	if cur.Revision != row.AfterRevision || cur.ContentErased || cur.PrivacyQuarantined {
		return ErrConflict
	}
	next := undoneRow(cur, row, principal, now)
	if !slices.Contains(next.Tags, dreamUndoneTag) {
		next.Tags = append(slices.Clone(next.Tags), dreamUndoneTag)
	}
	next.Revision, next.UpdatedAt = cur.Revision+1, now
	if err = tx.Replace(ctx, row.Collection, next, cur.Revision); err != nil {
		return err
	}
	if row.BeforeConflict != "" {
		if err = rebindConflictMember(ctx, tx, scope, row.BeforeConflict, next); err != nil {
			return err
		}
	}
	return audit(ctx, tx, scope, principal, "dream:undo:"+row.Collection, row.ID)
}

// undoneRow is a row as it was before the dream changed it.
func undoneRow(cur Record, row dreamRow, principal string, now time.Time) Record {
	next := cur
	switch {
	case row.Collection == colMaintenance && cur.Maintenance != nil:
		p := *cur.Maintenance // the merged pair stays two: the person's dismiss
		p.Decision, p.DecidedBy, p.DecidedAt = "dismiss", principal, &now
		next.Maintenance, next.Status = &p, maintenanceDecisions["dismiss"]
	case row.Header:
		next.ActiveVersion = row.BeforeActive // a promotion is taken back: the skill uses its earlier version
	case row.BeforeStatus == "":
		next.Status = "rejected" // an approval (or a skill version) the dream made is withdrawn
	default:
		// The dream changed only the status (and the conflict it resolved or one it cleared again);
		// a candidate also got the dream's review, which goes with it.
		next.Status, next.ConflictID = row.BeforeStatus, row.BeforeConflict
		if row.Collection == "memory_candidates" {
			next.Review = nil
		}
		if row.Collection == "memory_relations" && cur.Conflict != nil {
			open := ConflictDetails{Hold: cur.Conflict.Hold} // the conflict is open again
			next.Conflict = &open
		}
	}
	return next
}

// rebindConflictMember points the reopened conflict at the member's new revision, so it can be resolved again.
func rebindConflictMember(ctx context.Context, tx Tx, scope Scope, relID string, member Record) error {
	rel, err := tx.Get(ctx, "memory_relations", scope, relID)
	if err != nil {
		return err
	}
	for i, ref := range rel.Relations {
		if ref.ID == member.ID {
			rel.Relations[i].Revision = member.Revision
		}
	}
	prev := rel.Revision
	rel.Revision, rel.UpdatedAt = prev+1, member.UpdatedAt
	return tx.Replace(ctx, "memory_relations", rel, prev)
}

func registerDreamRunsCoreHTTP(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/dream_runs", func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			if limit, err = strconv.Atoi(raw); err != nil {
				respond(w, nil, ErrInvalid)
				return
			}
		}
		v, e := s.ListDreamRuns(r.Context(), bearer(r), scopeQuery(r), limit)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/dream_runs/{id}/undo", func(w http.ResponseWriter, r *http.Request) {
		var in DreamUndoInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.UndoDream(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
}
