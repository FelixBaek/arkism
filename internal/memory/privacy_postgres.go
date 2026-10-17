package memory

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

func (p *Postgres) DiscoverPrivacyImpact(ctx context.Context, scope Scope, targetType string, targetIDs []string, limit int) (PrivacyImpact, error) {
	if checkScope(scope) != nil || !slices.Contains([]string{"source_event", "trace", "subject", "work_checkpoint"}, targetType) || len(targetIDs) == 0 || len(targetIDs) > 64 || limit < 1 || limit > privacyImpactLimit {
		return PrivacyImpact{}, ErrInvalid
	}
	if targetType == "work_checkpoint" {
		return p.discoverSelectedWorkPrivacyImpact(ctx, scope, targetIDs, limit)
	}
	sources, traces := map[string]bool{}, map[string]bool{}
	handoffRuns, handoffThreads := map[string]bool{}, map[string]bool{}
	switch targetType {
	case "source_event":
		for _, id := range targetIDs {
			r, err := p.row(ctx, "sources", scope, id, false)
			if err != nil {
				return PrivacyImpact{}, err
			}
			sources[id] = true
			if r.TraceID != "" {
				traces[r.TraceID] = true
			}
		}
	case "trace":
		for _, id := range targetIDs {
			if _, err := p.row(ctx, "execution_traces", scope, id, false); err != nil {
				return PrivacyImpact{}, err
			}
			traces[id] = true
		}
	case "subject":
		if scope.UserID == "" || len(targetIDs) != 1 || targetIDs[0] != scope.UserID {
			return PrivacyImpact{}, ErrInvalid
		}
		rows, err := p.q().Query(ctx, `SELECT id FROM memory_records WHERE collection='sources' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 ORDER BY id LIMIT $5`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, limit+1)
		if err != nil {
			return PrivacyImpact{}, pgErr(err)
		}
		for rows.Next() {
			var id string
			if rows.Scan(&id) != nil {
				rows.Close()
				return PrivacyImpact{}, ErrUnavailable
			}
			sources[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return PrivacyImpact{}, pgErr(err)
		}
	}

	// Follow only typed provenance edges. source_kind is the contaminated
	// dependent and target_kind is its evidence dependency.
	changed := true
	for rounds := 0; changed && rounds < 64; rounds++ {

		changed = false
		// A consultation is one minimal evidence package: deleting a child trace
		// must also quarantine its parent anchor and every approved runtime reader.
		rows, err := p.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection='consultations' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND (
        doc->>'source_event_id'=ANY($5) OR doc->>'trace_id'=ANY($6) OR doc->'consultation'->'child'->>'trace_id'=ANY($6) OR doc->'consultation'->'parent'->>'trace_id'=ANY($6)) ORDER BY id LIMIT $7`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, sortedSet(sources), sortedSet(traces), limit+1)
		if err != nil {
			return PrivacyImpact{}, pgErr(err)
		}
		count := 0
		for rows.Next() {
			var raw []byte
			if rows.Scan(&raw) != nil {
				rows.Close()
				return PrivacyImpact{}, ErrUnavailable
			}
			r, e := pgDecode(raw)
			if e != nil {
				rows.Close()
				return PrivacyImpact{}, e
			}
			count++
			if r.SourceEventID != "" && !sources[r.SourceEventID] {
				sources[r.SourceEventID] = true
				changed = true
			}
			if r.Consultation != nil {
				agents := []ConsultationAgent{r.Consultation.Parent}
				if r.Consultation.Child != nil {
					agents = append(agents, *r.Consultation.Child)
				}
				for _, agent := range agents {
					if agent.AgentRunID != "" {
						handoffRuns[agent.AgentRunID] = true
					}
					if agent.ThreadID != "" {
						handoffThreads[agent.ThreadID] = true
					}
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return PrivacyImpact{}, pgErr(err)
		}
		if count > limit {
			return PrivacyImpact{}, ErrUnavailable
		}
		sourceIDs := sortedSet(sources)
		if len(sourceIDs) > 0 {
			rows, err := p.q().Query(ctx, `SELECT source_kind,source_id FROM relations WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND target_kind='source_event' AND target_id=ANY($5) AND relation_kind='context_uses' ORDER BY source_kind,source_id LIMIT $6`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, sourceIDs, limit+1)
			if err != nil {
				return PrivacyImpact{}, pgErr(err)
			}
			count := 0
			for rows.Next() {
				var kind, id string
				if rows.Scan(&kind, &id) != nil {
					rows.Close()
					return PrivacyImpact{}, ErrUnavailable
				}
				count++
				switch kind {
				case "source_event":
					if !sources[id] {
						sources[id], changed = true, true
					}
				case "trace":
					if !traces[id] {
						traces[id], changed = true, true
					}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return PrivacyImpact{}, pgErr(err)
			}
			if count > limit {
				return PrivacyImpact{}, ErrUnavailable
			}
		}
		if len(sources) > 0 {
			rows, err := p.q().Query(ctx, `SELECT DISTINCT trace_id FROM source_events WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=ANY($5)`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, sortedSet(sources))
			if err != nil {
				return PrivacyImpact{}, pgErr(err)
			}
			for rows.Next() {
				var id string
				if rows.Scan(&id) != nil {
					rows.Close()
					return PrivacyImpact{}, ErrUnavailable
				}
				if id != "" && !traces[id] {
					traces[id], changed = true, true
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return PrivacyImpact{}, pgErr(err)
			}
		}
		if len(traces) > 0 {
			rows, err := p.q().Query(ctx, `SELECT id FROM source_events WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND trace_id=ANY($5) ORDER BY id LIMIT $6`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, sortedSet(traces), limit+1)
			if err != nil {
				return PrivacyImpact{}, pgErr(err)
			}
			count := 0
			for rows.Next() {
				var id string
				if rows.Scan(&id) != nil {
					rows.Close()
					return PrivacyImpact{}, ErrUnavailable
				}
				count++
				if !sources[id] {
					sources[id], changed = true, true
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return PrivacyImpact{}, pgErr(err)
			}
			if count > limit {
				return PrivacyImpact{}, ErrUnavailable
			}
		}
		if len(sources)+len(traces) > limit {
			return PrivacyImpact{}, ErrUnavailable
		}
	}
	if changed {
		return PrivacyImpact{}, ErrUnavailable
	}
	if len(sources)+len(traces) > limit {
		return PrivacyImpact{}, ErrUnavailable
	}

	// The fixed point above treats a trace as one privacy aggregate: any event
	// contaminates all sibling events, and those siblings can in turn be used by
	// downstream traces.
	if len(sources)+len(traces) > limit {
		return PrivacyImpact{}, ErrUnavailable
	}

	impact := PrivacyImpact{SourceEventIDs: sortedSet(sources), TraceIDs: sortedSet(traces), ExpectedRunIDs: sortedSet(handoffRuns), ThreadIDs: sortedSet(handoffThreads)}
	if len(impact.TraceIDs) > 0 {
		rows, err := p.q().Query(ctx, `SELECT id,thread_id,agent_run_id FROM execution_traces WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=ANY($5) ORDER BY id LIMIT $6`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, impact.TraceIDs, limit+1)
		if err != nil {
			return PrivacyImpact{}, pgErr(err)
		}
		found, threads, runs := map[string]bool{}, handoffThreads, handoffRuns
		for rows.Next() {
			var id, thread, run string
			if rows.Scan(&id, &thread, &run) != nil {
				rows.Close()
				return PrivacyImpact{}, ErrUnavailable
			}
			found[id], threads[thread], runs[run] = true, true, true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return PrivacyImpact{}, pgErr(err)
		}
		if targetType == "trace" && len(found) != len(traces) {
			return PrivacyImpact{}, ErrConflict
		}
		impact.ThreadIDs, impact.ExpectedRunIDs = sortedSet(threads), sortedSet(runs)
	}

	rows, err := p.q().Query(ctx, `SELECT collection,payload FROM memory_records
WHERE workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4
  AND collection NOT IN ('jobs','audit_events','provider_usage','blobs') AND (
    (collection='sources' AND id=ANY($5)) OR (collection='execution_traces' AND id=ANY($6)) OR
    (collection='agent_runs' AND id=ANY($7)) OR doc->>'source_event_id'=ANY($5) OR doc->>'trace_id'=ANY($6) OR
    EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'sources')='array' THEN doc->'sources' ELSE '[]'::jsonb END) x WHERE x->>'source_id'=ANY($5)) OR
    EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'relations')='array' THEN doc->'relations' ELSE '[]'::jsonb END) x WHERE (x->>'collection'='execution_traces' AND x->>'id'=ANY($6)) OR (x->>'collection'='sources' AND x->>'id'=ANY($5)))
  ) ORDER BY collection,id LIMIT $8`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, impact.SourceEventIDs, impact.TraceIDs, impact.ExpectedRunIDs, limit+1)
	if err != nil {
		return PrivacyImpact{}, pgErr(err)
	}
	for rows.Next() {
		var collection string
		var payload []byte
		if rows.Scan(&collection, &payload) != nil {
			rows.Close()
			return PrivacyImpact{}, ErrUnavailable
		}
		r, decodeErr := pgDecode(payload)
		if decodeErr != nil {
			rows.Close()
			return PrivacyImpact{}, decodeErr
		}
		impact.Records = append(impact.Records, Ref{Collection: collection, ID: r.ID, Revision: r.Revision})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return PrivacyImpact{}, pgErr(err)
	}
	if len(impact.Records) > limit {
		return PrivacyImpact{}, ErrUnavailable
	}
	if err := p.discoverWorkPrivacyImpact(ctx, scope, targetType == "subject", &impact, limit); err != nil {
		return PrivacyImpact{}, err
	}
	return impact, nil
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func (p *Postgres) QuarantinePrivacyImpact(ctx context.Context, scope Scope, impact PrivacyImpact) error {
	if p.tx == nil || checkScope(scope) != nil {
		return ErrInvalid
	}
	if len(impact.SourceEventIDs) > 0 {
		if _, err := p.q().Exec(ctx, `UPDATE source_events SET privacy_quarantined=true WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=ANY($5)`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, impact.SourceEventIDs); err != nil {
			return pgErr(err)
		}
	}
	if len(impact.TraceIDs) > 0 {
		if _, err := p.q().Exec(ctx, `UPDATE execution_traces SET privacy_quarantined=true WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=ANY($5)`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, impact.TraceIDs); err != nil {
			return pgErr(err)
		}
	}
	return nil
}

func (p *Postgres) ClaimPrivacyDeletion(ctx context.Context, scope Scope, now time.Time, lease time.Duration) (Record, error) {
	if checkScope(scope) != nil || lease <= 0 {
		return Record{}, ErrInvalid
	}
	var out Record
	err := p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		pp := tx.(*Postgres)
		skipped := []string{}
		for {
			row := pp.q().QueryRow(ctx, `SELECT payload FROM memory_records WHERE collection='jobs' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind=$5 AND attempts<$8 AND NOT(id=ANY($9::text[])) AND doc->'privacy_deletion'->>'state' IN ('quarantined','purging_runtime','held','partial') AND ((status=ANY($6) AND (run_at IS NULL OR run_at<=$7)) OR (status='running' AND lease_until<=$7)) ORDER BY run_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, privacyDeletionKind, []string{JobStatusPending, JobStatusRetry}, now.UTC(), maxJobAttempts, skipped)
			var payload []byte
			if scanErr := row.Scan(&payload); errors.Is(scanErr, pgx.ErrNoRows) {
				return ErrNotFound
			} else if scanErr != nil {
				return pgErr(scanErr)
			}
			var decodeErr error
			out, decodeErr = pgDecode(payload)
			if decodeErr != nil {
				return decodeErr
			}
			if out.PrivacyDeletion == nil {
				return ErrUnavailable
			}
			if !privacyTransitionAllowed(out.PrivacyDeletion.State, privacyPurgingRuntime) && out.PrivacyDeletion.State != privacyPurgingRuntime && out.PrivacyDeletion.State != privacyHeld {
				return ErrConflict
			}
			if out.PrivacyDeletion.LegalHoldStatus != privacyHoldHeld && out.PrivacyDeletion.State != privacyHeld {
				if err := pp.CheckPrivacyOverlap(ctx, scope, out.ID, out.PrivacyDeletion.TargetType, out.PrivacyDeletion.Impact, false); err != nil {
					if errors.Is(err, ErrConflict) {
						skipped = append(skipped, out.ID)
						continue
					}
					return err
				}
			}
			token, tokenErr := newLeaseToken()
			if tokenErr != nil {
				return ErrUnavailable
			}
			out.Status, out.LeaseToken, out.LeaseUntil = JobStatusRunning, token, now.UTC().Add(lease)
			out.Attempts++
			out.Revision++
			out.UpdatedAt = now.UTC()
			if out.PrivacyDeletion.State != privacyHeld {
				out.PrivacyDeletion.State = privacyPurgingRuntime
			}
			return pp.Replace(ctx, colJobs, out, out.Revision-1)
		}
	})
	return out, err
}

// NextPrivacyAdvance returns one canonical phase that can be advanced by the
// existing service worker. RLS limits this unscoped queue scan to projects the
// bound worker token can access; AdvancePrivacyDeletion repeats authorization
// and revision checks before mutation.
func (p *Postgres) NextPrivacyAdvance(ctx context.Context, now time.Time) (Record, error) {
	var payload []byte
	err := p.q().QueryRow(ctx, `SELECT payload FROM memory_records
WHERE collection='jobs' AND kind=$1 AND status=ANY($2)
  AND doc->'privacy_deletion'->>'state' IN ('purging_derived','verifying')
  AND doc->'privacy_deletion'->>'legal_hold_status'<>'held'
  AND (run_at IS NULL OR run_at<=$3)
ORDER BY updated_at,id LIMIT 1`, privacyDeletionKind, []string{JobStatusPending, JobStatusRetry}, now.UTC()).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, pgErr(err)
	}
	return pgDecode(payload)
}

// PrivacyBackupWaiting lists waiting deletions parked before the given backup
// time, under the same RLS as NextPrivacyAdvance.
func (p *Postgres) PrivacyBackupWaiting(ctx context.Context, before time.Time) ([]Record, error) {
	// status 'pending' covers rows parked before JobStatusWaiting existed.
	rows, err := p.q().Query(ctx, `SELECT payload FROM memory_records
WHERE collection='jobs' AND kind=$1 AND status=ANY($2)
  AND doc->'privacy_deletion'->>'state'=$3
  AND doc->'privacy_deletion'->>'legal_hold_status'<>'held'
  AND updated_at<$4
ORDER BY updated_at,id LIMIT 100`, privacyDeletionKind, []string{JobStatusWaiting, JobStatusPending}, privacyWaiting, before.UTC())
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, pgErr(err)
		}
		r, err := pgDecode(payload)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, pgErr(rows.Err())
}

func (p *Postgres) PrivacyFence(ctx context.Context, in PrivacyFenceInput) (PrivacyFenceResult, error) {
	if checkScope(in.Scope) != nil || len(in.SourceEventIDs)+len(in.TraceIDs) == 0 || len(in.SourceEventIDs)+len(in.TraceIDs) > 128 {
		return PrivacyFenceResult{}, ErrInvalid
	}
	for _, ids := range [][]string{in.SourceEventIDs, in.TraceIDs, in.ExpectedRunIDs} {
		for _, id := range ids {
			if !validRunLabel(id, 256) {
				return PrivacyFenceResult{}, ErrInvalid
			}
		}
	}
	result := PrivacyFenceResult{Allowed: true, DeletionRequestIDs: []string{}}
	for _, target := range []struct {
		collection string
		ids        []string
	}{{"sources", in.SourceEventIDs}, {"execution_traces", in.TraceIDs}} {
		for _, id := range target.ids {
			r, err := p.row(ctx, target.collection, in.Scope, id, false)
			if err != nil {
				return PrivacyFenceResult{}, ErrConflict
			}
			if r.PrivacyQuarantined {
				result.Allowed = false
			}
		}
	}
	rows, err := p.q().Query(ctx, `SELECT id FROM memory_records WHERE collection='jobs' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind=$5 AND ((doc->'privacy_deletion'->'impact'->'source_event_ids') ?| $6 OR (doc->'privacy_deletion'->'impact'->'trace_ids') ?| $7) ORDER BY id LIMIT 101`, in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, privacyDeletionKind, in.SourceEventIDs, in.TraceIDs)
	if err != nil {
		return PrivacyFenceResult{}, pgErr(err)
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return PrivacyFenceResult{}, ErrUnavailable
		}
		result.DeletionRequestIDs = append(result.DeletionRequestIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return PrivacyFenceResult{}, pgErr(err)
	}
	if len(result.DeletionRequestIDs) > 100 {
		return PrivacyFenceResult{}, ErrUnavailable
	}
	if len(result.DeletionRequestIDs) > 0 {
		result.Allowed = false
	}
	return result, nil
}

func (p *Postgres) VerifyPrivacyPurge(ctx context.Context, scope Scope, impact PrivacyImpact) (PrivacyPurgeReport, error) {
	if p.tx == nil || checkScope(scope) != nil {
		return PrivacyPurgeReport{}, ErrInvalid
	}
	report := PrivacyPurgeReport{LiveAccessRemoved: true, DerivedPurged: true, ArtifactsPurged: true}
	if err := verifyWorkPrivacyImpact(ctx, p, scope, impact); err != nil {
		return report, err
	}
	for _, id := range impact.SourceEventIDs {
		if _, err := p.row(ctx, "sources", scope, id, false); err == nil {
			return report, ErrConflict
		} else if !errors.Is(err, ErrNotFound) {
			return report, err
		}
		job, err := p.row(ctx, colJobs, scope, sourceMemoryErasureID(scope, id), false)
		if err == nil && job.Status == JobStatusFailed {
			return report, ErrUnavailable
		}
		if err != nil || job.Status != JobStatusDone {
			if err != nil && !errors.Is(err, ErrNotFound) {
				return report, err
			}
			return report, ErrConflict
		}
		if job.SourceMemoryErasure == nil || len(job.SourceMemoryErasure.PendingArtifacts) != 0 {
			return report, ErrConflict
		}
		children, childErr := dependentErasureStatus(ctx, p, job)
		if childErr != nil {
			return report, childErr
		}
		if children.Statuses[JobStatusFailed] != 0 {
			return report, ErrUnavailable
		}
		if !children.Complete {
			return report, ErrConflict
		}
	}
	var remaining int
	if err := p.q().QueryRow(ctx, `SELECT count(*) FROM memory_records WHERE workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND collection NOT IN ('jobs','audit_events','provider_usage','blobs') AND (EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'sources')='array' THEN doc->'sources' ELSE '[]'::jsonb END) x WHERE x->>'source_id'=ANY($5)) OR doc->>'source_event_id'=ANY($5))`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, impact.SourceEventIDs).Scan(&remaining); err != nil {
		return report, pgErr(err)
	}
	if remaining != 0 {
		return report, ErrConflict
	}
	if err := p.q().QueryRow(ctx, `SELECT count(*) FROM source_events WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=ANY($5) AND (NOT content_erased OR octet_length(payload_raw)>0)`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, impact.SourceEventIDs).Scan(&remaining); err != nil {
		return report, pgErr(err)
	}
	if remaining != 0 {
		return report, ErrConflict
	}
	if len(impact.TraceIDs) > 0 {
		if err := p.q().QueryRow(ctx, `SELECT count(*) FROM source_events WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND trace_id=ANY($5) AND (NOT (id=ANY($6)) OR NOT content_erased OR octet_length(payload_raw)>0)`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, impact.TraceIDs, impact.SourceEventIDs).Scan(&remaining); err != nil {
			return report, pgErr(err)
		}
		if remaining != 0 {
			return report, ErrConflict
		}
	}
	if err := p.q().QueryRow(ctx, `SELECT count(*) FROM execution_traces WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=ANY($5) AND (NOT content_erased OR octet_length(payload_raw)>0)`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, impact.TraceIDs).Scan(&remaining); err != nil {
		return report, pgErr(err)
	}
	if remaining != 0 {
		return report, ErrConflict
	}
	return report, nil
}

// PrivacyRuntimeScopes is every scope with a deletion waiting for its runtime step.
func (p *Postgres) PrivacyRuntimeScopes(ctx context.Context) ([]Scope, error) {
	rows, err := p.q().Query(ctx, `SELECT DISTINCT workspace_id, project_id, user_id, agent_id FROM memory_records WHERE collection='jobs' AND kind=$1 AND doc->'privacy_deletion'->>'state' IN ('quarantined','purging_runtime','partial') AND status=ANY($2)`,
		privacyDeletionKind, []string{JobStatusPending, JobStatusRetry, JobStatusRunning})
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	var out []Scope
	for rows.Next() {
		var s Scope
		if err := rows.Scan(&s.WorkspaceID, &s.ProjectID, &s.UserID, &s.AgentID); err != nil {
			return nil, pgErr(err)
		}
		out = append(out, s)
	}
	return out, pgErr(rows.Err())
}
