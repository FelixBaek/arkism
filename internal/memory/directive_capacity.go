package memory

import (
	"context"
	"errors"
	"time"
)

type directiveScopeLocker interface {
	LockDirectiveScope(context.Context, Scope) error
}

func (p *Postgres) LockDirectiveScope(ctx context.Context, scope Scope) error {
	if checkScope(scope) != nil {
		return ErrInvalid
	}
	_, e := p.q().Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "arkism.directives:"+structuredText(scope))
	return pgErr(e)
}

// Only linked automatic rules are retired. Provenance and text remain for
// review; retirement never turns itself back on when the source later changes.
func checkDirectiveCapacity(ctx context.Context, tx Tx, scope Scope, actor, exclude string) error {
	if locker, ok := tx.(directiveScopeLocker); ok {
		if e := locker.LockDirectiveScope(ctx, scope); e != nil {
			return e
		}
	}
	lister := listerOf(ctx, tx)
	if lister == nil {
		return ErrUnavailable
	}
	count := 0
	e := walkStatusRows(ctx, lister, scope, colDirectives, statusActive, func(r Record) error {
		if r.ID == exclude {
			return nil
		}
		retire := false
		if r.Directive != nil && !r.Directive.Independent && r.Proposal != nil && r.Proposal.Collection == colMemories {
			d := r.Directive
			retire = d.SourceScope == nil || d.SourceRule == "" || d.SourceScope != nil && projectScope(*d.SourceScope) != scope
			if !retire {
				source, err := tx.Get(ctx, colMemories, *d.SourceScope, r.Proposal.ID)
				if err != nil && !errors.Is(err, ErrNotFound) {
					return err
				}
				retire = err != nil || source.Scope != *d.SourceScope || source.Revision < r.Proposal.Revision || (source.Status != statusActive || source.PrivacyQuarantined || source.ExpiresAt != nil && !source.ExpiresAt.After(time.Now())) || source.ContentErased || source.ApprovedBy == "" || source.Text != r.Text || ruleOf(source) != d.SourceRule
				if !retire {
					err = checkSources(ctx, tx, source)
					if err != nil {
						if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrDenied) && !errors.Is(err, ErrNotFound) {
							return err
						}
						retire = true
					}
				}
			}
		}
		if retire {
			prev := r.Revision
			r.Revision++
			r.Status = "inactive"
			r.UpdatedAt = time.Now().UTC()
			if e := tx.Replace(ctx, colDirectives, r, prev); e != nil {
				return e
			}
			if e := audit(ctx, tx, scope, actor, "retire:directive:source", r.ID); e != nil {
				return e
			}
		} else if r.ID != exclude {
			count++
		}
		return nil
	})
	if e != nil {
		return e
	}
	if count >= directiveMaxPerScope {
		return ErrConflict
	}
	return nil
}
