package memory

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MemoryMaintenanceStatus describes a deliberately global, owner-only outage.
// No source text or credential is returned. Upgraded repository clients fail
// closed while Active. Operators must stop older clients and direct SQL access;
// this application gate is not a fence for arbitrary database clients.
type MemoryMaintenanceStatus struct {
	Revision int64  `json:"revision"`
	Active   bool   `json:"active"`
	Held     bool   `json:"held"`
	ID       string `json:"id,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Scanned  int64  `json:"scanned"`
}
type memoryMaintenance struct {
	Cursor2  string
	Cursor3  string
	Cursor   string
	ID       string
	Kind     string
	Phase    int
	Offset   int
	Scanned  int64
	Held     bool
	Scope    Scope
	Actor    string
	Request  *PrivacyDeletionRequest
	Privacy  *PrivacyDeletion
	Mark     *StaleMark
	PassSize int
}

// maintenanceTransaction is intentionally not exposed through the HTTP router.
// Only an unbound database owner connection can enter; normal repositories,
// including superuser application connections, remain behind the shared gate.
func (p *Postgres) maintenanceTransaction(ctx context.Context, fn func(*Postgres, int64, *memoryMaintenance) (*memoryMaintenance, error)) (MemoryMaintenanceStatus, error) {
	if p == nil || p.pool == nil || p.tx != nil {
		return MemoryMaintenanceStatus{}, ErrInvalid
	}
	if _, bound := p.effectiveTokenHash(ctx); bound {
		return MemoryMaintenanceStatus{}, ErrDenied
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return MemoryMaintenanceStatus{}, pgErr(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var owner bool
	if err = tx.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls OR current_user='arkism_init' FROM pg_roles WHERE rolname=current_user`).Scan(&owner); err != nil || !owner {
		return MemoryMaintenanceStatus{}, ErrDenied
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4706904311881285641)`); err != nil {
		return MemoryMaintenanceStatus{}, pgErr(err)
	}
	var revision int64
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT revision,operation FROM arkism.memory_maintenance_gate WHERE id=1 FOR UPDATE`).Scan(&revision, &raw); err != nil {
		return MemoryMaintenanceStatus{}, pgErr(err)
	}
	var job *memoryMaintenance
	if raw != nil {
		if err = json.Unmarshal(raw, &job); err != nil || job == nil {
			return MemoryMaintenanceStatus{}, ErrUnavailable
		}
	}
	if job != nil && (!validScope(job.Scope) || job.ID == "" || job.Phase < 0 || job.Offset < 0 || job.Scanned < 0 || (job.Kind != "privacy" && job.Kind != "stale") || (job.Kind == "privacy" && job.Phase > 11) || (job.Kind == "stale" && job.Phase > len(staleMaintenancePhases))) {
		return MemoryMaintenanceStatus{}, ErrUnavailable
	}
	child := &Postgres{pool: p.pool, tx: tx, vectorMaxDistance: p.vectorMaxDistance}
	next, err := fn(child, revision, job)
	if errors.Is(err, errMaintenanceReadOnly) {
		return maintenanceStatus(revision, job), nil
	}
	if err != nil {
		return MemoryMaintenanceStatus{}, err
	}
	var payload any
	if next != nil {
		b, e := json.Marshal(next)
		if e != nil {
			return MemoryMaintenanceStatus{}, ErrInvalid
		}
		payload = string(b)
	}
	if _, err = tx.Exec(ctx, `UPDATE arkism.memory_maintenance_gate SET revision=revision+1,operation=$1::jsonb WHERE id=1`, payload); err != nil {
		return MemoryMaintenanceStatus{}, pgErr(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return MemoryMaintenanceStatus{}, pgErr(err)
	}
	return maintenanceStatus(revision+1, next), nil
}
func maintenanceStatus(rev int64, j *memoryMaintenance) MemoryMaintenanceStatus {
	out := MemoryMaintenanceStatus{Revision: rev, Active: j != nil}
	if j != nil {
		out.ID, out.Kind, out.Held, out.Scanned = j.ID, j.Kind, j.Held, j.Scanned
		out.Phase = maintenancePhase(j)
	}
	return out
}
func maintenancePhase(j *memoryMaintenance) string {
	if j.Kind == "privacy" {
		return []string{"sources", "source_graph", "relations", "consultations", "trace_owners", "records", "quarantine_sources", "quarantine_traces", "quarantine_records", "publish", "work_closure", "work_receipts"}[min(j.Phase, 11)]
	}
	return "stale_cascade"
}

var errMaintenanceReadOnly = errors.New("maintenance status read")

func (p *Postgres) MemoryMaintenanceStatus(ctx context.Context) (MemoryMaintenanceStatus, error) {
	// Status uses the owner boundary without changing the operation revision.
	return p.maintenanceTransaction(ctx, func(_ *Postgres, _ int64, j *memoryMaintenance) (*memoryMaintenance, error) {
		return j, errMaintenanceReadOnly
	})
}
func (p *Postgres) HoldMemoryMaintenance(ctx context.Context, expected int64, held bool) (MemoryMaintenanceStatus, error) {
	return p.maintenanceTransaction(ctx, func(_ *Postgres, rev int64, j *memoryMaintenance) (*memoryMaintenance, error) {
		if rev != expected || j == nil {
			return nil, ErrConflict
		}
		j.Held = held
		return j, nil
	})
}
func (s *Service) maintenanceOwner() (*Postgres, error) {
	p, ok := s.Repo.(*Postgres)
	if !ok {
		return nil, ErrUnavailable
	}
	a, ok := s.Auth.(*PostgresAuthorizer)
	if !ok || a.store.pool != p.pool {
		return nil, ErrDenied
	}
	return p, nil
}
func (s *Service) maintenanceService(p *Postgres) *Service {
	// These immutable configuration/interfaces are needed by authorization,
	// defense rules and approval scheduling. Never copy service locks/caches.
	// Wiki is retained only for the existing queue eligibility checks; supersede
	// schedules work but does not execute a provider call.
	return &Service{Repo: p, Auth: &PostgresAuthorizer{store: p}, SpaceTemplate: s.SpaceTemplate, Extensions: s.Extensions, Wiki: s.Wiki, AuditBodies: s.AuditBodies}
}

// BeginPrivacyMaintenance is an explicit resumable alternative to the normal
// atomic API. It freezes ALL repository access, not only the requested scope.
func (s *Service) BeginPrivacyMaintenance(ctx context.Context, token string, in PrivacyDeletionRequest) (MemoryMaintenanceStatus, error) {
	if err := validatePrivacyRequest(&in); err != nil {
		return MemoryMaintenanceStatus{}, err
	}
	if in.TargetType == "work_checkpoint" || in.TargetType == "subject" && in.ExpectedRevision != 0 {
		return MemoryMaintenanceStatus{}, ErrInvalid
	}
	p, err := s.maintenanceOwner()
	if err != nil {
		return MemoryMaintenanceStatus{}, err
	}
	return p.maintenanceTransaction(ctx, func(tx *Postgres, _ int64, current *memoryMaintenance) (*memoryMaintenance, error) {
		if current != nil {
			return nil, ErrConflict
		}
		if err := maintenanceToken(ctx, tx, token); err != nil {
			return nil, err
		}
		svc := s.maintenanceService(tx)
		actor, err := svc.authorizePrivacyRequest(ctx, token, in)
		if err != nil {
			return nil, err
		}
		if actor.Role != "human" {
			return nil, ErrDenied
		}
		seedTraces := []string{}
		if in.TargetType != "subject" {
			collection := "sources"
			if in.TargetType == "trace" {
				collection = "execution_traces"
			}
			evidence := PrivacyImpact{}
			for _, target := range in.TargetIDs {
				row, e := tx.Get(ctx, collection, in.Scope, target)
				if e != nil {
					return nil, e
				}
				evidence.Records = append(evidence.Records, Ref{Collection: collection, ID: row.ID, Revision: row.Revision})
				if in.TargetType == "source_event" && row.TraceID != "" {
					maintenanceAdd(&seedTraces, row.TraceID)
				}
			}
			if in.ExpectedRevision != 0 {
				if e := checkPrivacyExpectedRevision(in, evidence); e != nil {
					return nil, e
				}
			}
		}
		id := privacyJobID(in.Scope, actor.PrincipalID, in.IdempotencyKey)
		if _, err = tx.Get(ctx, colJobs, in.Scope, id); !errors.Is(err, ErrNotFound) {
			if err == nil {
				err = ErrConflict
			}
			return nil, err
		}
		if err = checkPrivacyOverlap(ctx, tx, in.Scope, id, "subject", PrivacyImpact{}, false); err != nil {
			return nil, err
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		pd := &PrivacyDeletion{State: privacyDiscoveringImpact, RequesterType: actor.Role, RequesterID: actor.PrincipalID, TargetType: in.TargetType, TargetIDs: slices.Clone(in.TargetIDs), Reason: in.Reason, LegalHoldStatus: privacyHoldNone, RetentionPolicyRef: in.RetentionPolicyRef, RequestedAt: now, AuthorizedAt: &now, IdempotencyKey: in.IdempotencyKey, CorrelationID: auditRequestID(ctx), RequestHash: ContentHash(structuredText(in)), PurgeReportRef: "jobs/" + id + "#privacy_deletion.report"}
		if in.TargetType == "source_event" {
			pd.Impact.SourceEventIDs = slices.Clone(in.TargetIDs)
			pd.Impact.TraceIDs = seedTraces
		}
		if in.TargetType == "trace" {
			pd.Impact.TraceIDs = slices.Clone(in.TargetIDs)
		}
		return &memoryMaintenance{ID: id, Kind: "privacy", Scope: in.Scope, Actor: actor.PrincipalID, Request: &in, Privacy: pd}, audit(ctx, tx, in.Scope, actor.PrincipalID, "begin:memory_maintenance", id)
	})
}
func (p *Postgres) AdvanceMemoryMaintenance(ctx context.Context, expected int64) (MemoryMaintenanceStatus, error) {
	return p.maintenanceTransaction(ctx, func(tx *Postgres, rev int64, j *memoryMaintenance) (*memoryMaintenance, error) {
		if j == nil || j.Held || rev != expected {
			return nil, ErrConflict
		}
		var done bool
		var err error
		switch j.Kind {
		case "privacy":
			done, err = advanceSubjectMaintenance(ctx, tx, j)
		case "stale":
			done, err = advanceStaleMaintenance(ctx, tx, j)
		default:
			err = ErrUnavailable
		}
		if err != nil {
			return nil, err
		}
		if done {
			return nil, nil
		}
		return j, nil
	})
}

func maintenanceToken(ctx context.Context, p *Postgres, token string) error {
	hash, _ := p.effectiveTokenHash(ContextWithPostgresToken(ctx, token))
	_, err := p.tx.Exec(ctx, `SELECT set_config('arkism.token_hash',$1,true)`, hash)
	return pgErr(err)
}

// A session advisory lease protects read-only snapshots without consuming a
// second pool connection. Owner transitions take the matching exclusive lock.
func (p *Postgres) maintenanceReadLease(ctx context.Context) (*pgxpool.Conn, func(), error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, pgErr(err)
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, e := conn.Exec(cleanup, `SELECT pg_advisory_unlock_shared(4706904311881285641)`); e != nil {
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
	}
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock_shared(4706904311881285641)`); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = conn.Conn().Close(cleanup)
		cancel()
		conn.Release()
		return nil, nil, pgErr(err)
	}
	if _, err = conn.Exec(ctx, `SELECT arkism.check_maintenance_access()`); err != nil {
		release()
		return nil, nil, pgErr(err)
	}
	return conn, release, nil
}

// Cancellation is safe only before quarantine mutates canonical records. It
// abandons the unaccepted deletion proposal; it never undoes a legal hold.
func (p *Postgres) CancelMemoryMaintenance(ctx context.Context, expected int64) (MemoryMaintenanceStatus, error) {
	return p.maintenanceTransaction(ctx, func(tx *Postgres, rev int64, j *memoryMaintenance) (*memoryMaintenance, error) {
		if j == nil || rev != expected || j.Kind != "privacy" || !slices.Contains([]int{0, 1, 2, 3, 4, 5, 10, 11}, j.Phase) {
			return nil, ErrConflict
		}
		return nil, audit(ctx, tx, j.Scope, j.Actor, "cancel:memory_maintenance", j.ID)
	})
}
