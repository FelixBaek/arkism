package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

type archiveUsageMutation struct {
	*Postgres
	before      func()
	afterMemory func()
}
type archiveUsageMutationTx struct {
	Tx
	afterMemory func()
}

func (r *archiveUsageMutation) SessionArchiveTransaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if r.before != nil {
		f := r.before
		r.before = nil
		f()
	}
	return r.Postgres.SessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, &archiveUsageMutationTx{Tx: tx, afterMemory: r.afterMemory})
	})
}
func (t *archiveUsageMutationTx) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	r, e := t.Tx.Get(ctx, col, scope, id)
	if e == nil && col == colMemories && t.afterMemory != nil {
		f := t.afterMemory
		t.afterMemory = nil
		f()
	}
	return r, e
}
func TestPostgresSessionArchiveUsageFence(t *testing.T) {
	for _, change := range []string{"expiry", "memory_expiry", "quarantine", "policy", "valid_delay"} {
		t.Run(change, func(t *testing.T) {
			p := isolatedPostgres(t)
			f, now := archiveSearchFixture(t, p)
			f.auth.actors[f.agent] = actor("usage-agent", "agent", true, "read", "use")
			id := ContentHash("usage-fence-" + change)
			header := archiveSearchUpload(t, f, *now, id, jsonLine(map[string]string{"role": "user", "content": "근거"}), "jsonl")
			m := Record{ID: ID(), Scope: scopeA, Status: "active", Kind: "decision", Revision: 1, Text: "기억", Tags: []string{"session:search-session"}, CreatedAt: *now, UpdatedAt: *now}
			expiry := now.Add(time.Hour)
			if change == "memory_expiry" {
				m.ExpiresAt = &expiry
			}
			if err := p.Insert(testCtx, colMemories, m); err != nil {
				t.Fatal(err)
			}
			original := f.svc.Auth
			denied := false
			f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
				if denied {
					return Decision{}, ErrDenied
				}
				return original.Authorize(ctx, token, req)
			})
			r := &archiveUsageMutation{Postgres: p}
			switch change {
			case "valid_delay":
				r.before = func() { *now = now.Add(time.Hour) }
			case "expiry":
				r.before = func() { *now = *header.ExpiresAt }
			case "memory_expiry":
				r.before = func() { *now = expiry }
			case "policy":
				r.before = func() { denied = true }
			case "quarantine":
				r.afterMemory = func() {
					newM := m
					newM.PrivacyQuarantined = true
					newM.Revision++
					if err := p.Replace(testCtx, colMemories, newM, m.Revision); err != nil {
						t.Fatal(err)
					}
				}
			}
			f.svc.Repo = r
			out, err := f.svc.SessionArchiveUsed(testCtx, f.agent, SessionArchiveUsedInput{Scope: scopeA, ID: id, MemoryID: m.ID, EventID: "actual-use"})
			if change == "valid_delay" {
				if err != nil || !out.Extended || !out.ExpiresAt.Equal(now.Add(SessionArchiveTTL)) {
					t.Fatal("server time renewal", err)
				}
				first := *out.ExpiresAt
				*now = now.Add(time.Hour)
				retry, err := f.svc.SessionArchiveUsed(testCtx, f.agent, SessionArchiveUsedInput{Scope: scopeA, ID: id, MemoryID: m.ID, EventID: "actual-use"})
				if err != nil || retry.Extended || !retry.ExpiresAt.Equal(first) {
					t.Fatal("duplicate renewal", err)
				}
				return
			}
			expected := ErrDenied
			if change == "expiry" || change == "quarantine" {
				expected = ErrConflict
			}
			if !errors.Is(err, expected) || out.Extended {
				t.Fatalf("late %s result: %+v %v", change, out, err)
			}
			receipt := sessionArchiveReceiptID(scopeA, "usage-agent", "actual-use", id, m.ID)
			if _, err := p.Get(testCtx, colJobs, scopeA, receipt); !errors.Is(err, ErrNotFound) {
				t.Fatal("failed use left receipt", err)
			}
			got, e := p.Get(testCtx, colSessionArchives, scopeA, id)
			if e != nil || !got.ExpiresAt.Equal(*header.ExpiresAt) || got.Revision != header.Revision {
				t.Fatal("failed use mutated retention", e)
			}
		})
	}
}
