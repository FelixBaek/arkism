package memory

import (
	"context"
	"slices"
	"time"
)

func maintenanceAdd(ids *[]string, id string) {
	if !slices.Contains(*ids, id) {
		*ids = append(*ids, id)
	}
}
func maintenanceArgs(j *memoryMaintenance, count int) []any {
	out := []any{j.Scope.WorkspaceID, j.Scope.ProjectID, j.Scope.UserID, j.Scope.AgentID, j.Cursor}
	if count > 1 {
		out = append(out, j.Cursor2)
	}
	if count > 2 {
		out = append(out, j.Cursor3)
	}
	return out
}
func maintenancePageDone(j *memoryMaintenance, n int, next int) {
	j.Scanned += int64(n)
	j.Offset += n
	if n < 100 {
		j.Phase = next
		j.Offset = 0
		j.Cursor, j.Cursor2, j.Cursor3 = "", "", ""
	}
}

func advanceSubjectMaintenance(ctx context.Context, p *Postgres, j *memoryMaintenance) (bool, error) {
	if j.Privacy == nil || j.Request == nil || j.Request.TargetType == "work_checkpoint" {
		return false, ErrUnavailable
	}
	impact := &j.Privacy.Impact
	if err := checkPrivacyOverlap(ctx, p, j.Scope, j.ID, j.Request.TargetType, *impact, false); err != nil {
		return false, err
	}
	switch j.Phase {
	case 0, 3, 5:
		return false, maintenanceRecordPage(ctx, p, j)
	case 1:
		return false, maintenanceSourceEventPage(ctx, p, j)
	case 2:
		return false, maintenanceRelationPage(ctx, p, j)
	case 4:
		return false, maintenanceTracePage(ctx, p, j)
	case 10, 11:
		return false, maintenanceWorkPage(ctx, p, j)
	case 6, 7, 8:
		return false, maintenanceQuarantinePage(ctx, p, j)
	case 9:
		return maintenancePrivacyRequest(ctx, p, j)
	default:
		return false, ErrUnavailable
	}
}

// maintenanceScanPage runs one page query, calls visit for each row and returns
// the row count. visit receives the row scanner and owns its error mapping.
func maintenanceScanPage(ctx context.Context, p *Postgres, query string, args []any, visit func(scan func(...any) error) error) (int, error) {
	rows, err := p.q().Query(ctx, query, args...)
	if err != nil {
		return 0, pgErr(err)
	}
	n := 0
	for rows.Next() {
		if err = visit(rows.Scan); err != nil {
			rows.Close()
			return n, err
		}
		n++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return n, pgErr(err)
	}
	return n, nil
}

func maintenanceSourceEventPage(ctx context.Context, p *Postgres, j *memoryMaintenance) error {
	impact := &j.Privacy.Impact
	n, err := maintenanceScanPage(ctx, p, `SELECT id,trace_id FROM source_events WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id>$5 ORDER BY id LIMIT 100`, maintenanceArgs(j, 1), func(scan func(...any) error) error {
		var id, trace string
		if err := scan(&id, &trace); err != nil {
			return pgErr(err)
		}
		j.Cursor = id
		if slices.Contains(impact.SourceEventIDs, id) || trace != "" && slices.Contains(impact.TraceIDs, trace) {
			maintenanceAdd(&impact.SourceEventIDs, id)
			if trace != "" {
				maintenanceAdd(&impact.TraceIDs, trace)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	maintenancePageDone(j, n, 2)
	return nil
}

func maintenanceRelationPage(ctx context.Context, p *Postgres, j *memoryMaintenance) error {
	impact := &j.Privacy.Impact
	n, err := maintenanceScanPage(ctx, p, `SELECT source_kind,source_id,target_id FROM relations WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND target_kind='source_event' AND relation_kind='context_uses' AND (source_kind,source_id,target_id)>($5,$6,$7) ORDER BY source_kind,source_id,target_id LIMIT 100`, maintenanceArgs(j, 3), func(scan func(...any) error) error {
		var kind, id, target string
		if err := scan(&kind, &id, &target); err != nil {
			return pgErr(err)
		}
		j.Cursor, j.Cursor2, j.Cursor3 = kind, id, target
		if !slices.Contains(impact.SourceEventIDs, target) {
			return nil
		}
		switch kind {
		case "source_event":
			maintenanceAdd(&impact.SourceEventIDs, id)
		case "trace":
			maintenanceAdd(&impact.TraceIDs, id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	maintenancePageDone(j, n, 3)
	return nil
}

func maintenanceTracePage(ctx context.Context, p *Postgres, j *memoryMaintenance) error {
	impact := &j.Privacy.Impact
	n, err := maintenanceScanPage(ctx, p, `SELECT id,thread_id,agent_run_id FROM execution_traces WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id>$5 ORDER BY id LIMIT 100`, maintenanceArgs(j, 1), func(scan func(...any) error) error {
		var id, thread, run string
		if err := scan(&id, &thread, &run); err != nil {
			return pgErr(err)
		}
		j.Cursor = id
		if slices.Contains(impact.TraceIDs, id) {
			maintenanceAdd(&impact.ThreadIDs, thread)
			maintenanceAdd(&impact.ExpectedRunIDs, run)
		}
		return nil
	})
	if err != nil {
		return err
	}
	maintenancePageDone(j, n, 5)
	return nil
}

// maintenanceQuarantinePage quarantines the next page of source events (phase
// 6), traces (7) or records (8).
func maintenanceQuarantinePage(ctx context.Context, p *Postgres, j *memoryMaintenance) error {
	impact := &j.Privacy.Impact
	n := 0
	switch j.Phase {
	case 6:
		n = min(100, len(impact.SourceEventIDs)-j.Offset)
		if n < 0 {
			return ErrConflict
		}
		if err := p.QuarantinePrivacyImpact(ctx, j.Scope, PrivacyImpact{SourceEventIDs: impact.SourceEventIDs[j.Offset : j.Offset+n]}); err != nil {
			return err
		}
	case 7:
		n = min(100, len(impact.TraceIDs)-j.Offset)
		if n < 0 {
			return ErrConflict
		}
		if err := p.QuarantinePrivacyImpact(ctx, j.Scope, PrivacyImpact{TraceIDs: impact.TraceIDs[j.Offset : j.Offset+n]}); err != nil {
			return err
		}
	case 8:
		n = min(100, len(impact.Records)-j.Offset)
		if n < 0 {
			return ErrConflict
		}
		if err := quarantinePrivacyRecords(ctx, p, j.Scope, PrivacyImpact{Records: impact.Records[j.Offset : j.Offset+n]}, j.Privacy.RequestedAt); err != nil {
			return err
		}
	}
	maintenancePageDone(j, n, j.Phase+1)
	return nil
}

// maintenancePrivacyRequest files the quarantined impact as a pending privacy
// deletion job, which ends the maintenance.
func maintenancePrivacyRequest(ctx context.Context, p *Postgres, j *memoryMaintenance) (bool, error) {
	pd := j.Privacy
	pd.State = privacyQuarantined
	pd.Report = PrivacyPurgeReport{LiveAccessRemoved: true, BackupExpiryPending: pd.RetentionPolicyRef != retentionSynthetic}
	now := time.Now().UTC()
	row := Record{ID: j.ID, Scope: j.Scope, Kind: privacyDeletionKind, Status: JobStatusPending, Revision: 1, CreatedBy: pd.RequesterID, CreatedByType: pd.RequesterType, CreatedAt: pd.RequestedAt, UpdatedAt: now, RequestHash: pd.RequestHash, PrivacyDeletion: pd}
	if err := p.Insert(ctx, colJobs, row); err != nil {
		return false, err
	}
	return true, audit(ctx, p, j.Scope, j.Actor, "request:privacy_deletion", j.ID)
}

type maintenanceRecordItem struct {
	col string
	r   Record
}

func maintenanceRecordPage(ctx context.Context, p *Postgres, j *memoryMaintenance) error {
	where := "collection='sources'"
	if j.Phase == 3 {
		where = "collection='consultations'"
	}
	if j.Phase == 5 {
		where = "collection NOT IN ('jobs','audit_events','provider_usage','blobs')"
	}
	page := []maintenanceRecordItem{}
	_, err := maintenanceScanPage(ctx, p, `SELECT collection,payload FROM memory_records WHERE workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND (`+where+`) AND (collection,id)>($5,$6) ORDER BY collection,id LIMIT 100`, maintenanceArgs(j, 2), func(scan func(...any) error) error {
		var col string
		var raw []byte
		if err := scan(&col, &raw); err != nil {
			return pgErr(err)
		}
		r, e := pgDecode(raw)
		if e != nil {
			return e
		}
		page = append(page, maintenanceRecordItem{col, r})
		return nil
	})
	if err != nil {
		return err
	}
	imp := &j.Privacy.Impact
	for _, v := range page {
		r := v.r
		j.Cursor, j.Cursor2 = v.col, r.ID
		switch j.Phase {
		case 0:
			if j.Request.TargetType == "subject" {
				maintenanceAdd(&imp.SourceEventIDs, r.ID)
			}
		case 3:
			maintenanceConsultationImpact(imp, r)
		case 5:
			if maintenanceRecordMatches(imp, v.col, r) {
				imp.Records = append(imp.Records, Ref{Collection: v.col, ID: r.ID, Revision: r.Revision})
			}
		}
	}
	phase := j.Phase
	maintenancePageDone(j, len(page), phase+1)
	if len(page) < 100 {
		maintenanceRecordPassDone(j, phase)
	}
	return nil
}

// maintenanceConsultationImpact widens the impact with a consultation that
// touches it.
func maintenanceConsultationImpact(imp *PrivacyImpact, r Record) {
	match := slices.Contains(imp.SourceEventIDs, r.SourceEventID) || slices.Contains(imp.TraceIDs, r.TraceID)
	if r.Consultation != nil {
		match = match || slices.Contains(imp.TraceIDs, r.Consultation.Parent.TraceID) || r.Consultation.Child != nil && slices.Contains(imp.TraceIDs, r.Consultation.Child.TraceID)
	}
	if !match {
		return
	}
	if r.SourceEventID != "" {
		maintenanceAdd(&imp.SourceEventIDs, r.SourceEventID)
	}
	if r.Consultation != nil {
		runs, threads := map[string]bool{}, map[string]bool{}
		addConsultationHandoffs(r.Consultation, runs, threads)
		for id := range runs {
			maintenanceAdd(&imp.ExpectedRunIDs, id)
		}
		for id := range threads {
			maintenanceAdd(&imp.ThreadIDs, id)
		}
	}
}

// maintenanceRecordMatches reports whether a record is, or derives from, an
// impacted source event, trace or run.
func maintenanceRecordMatches(imp *PrivacyImpact, col string, r Record) bool {
	match := col == "sources" && slices.Contains(imp.SourceEventIDs, r.ID) || col == "execution_traces" && slices.Contains(imp.TraceIDs, r.ID) || col == "agent_runs" && slices.Contains(imp.ExpectedRunIDs, r.ID) || slices.Contains(imp.SourceEventIDs, r.SourceEventID) || slices.Contains(imp.TraceIDs, r.TraceID)
	for _, src := range r.Sources {
		match = match || slices.Contains(imp.SourceEventIDs, src.SourceID)
	}
	for _, ref := range r.Relations {
		match = match || ref.Collection == "sources" && slices.Contains(imp.SourceEventIDs, ref.ID) || ref.Collection == "execution_traces" && slices.Contains(imp.TraceIDs, ref.ID)
	}
	return match
}

// maintenanceRecordPassDone closes a finished record pass: phase 0 sizes the
// seed, phase 3 repeats discovery while it still grows, phase 5 moves on.
func maintenanceRecordPassDone(j *memoryMaintenance, phase int) {
	imp := &j.Privacy.Impact
	if phase == 0 {
		j.PassSize = len(imp.SourceEventIDs) + len(imp.TraceIDs)
	}
	if phase == 3 {
		size := len(imp.SourceEventIDs) + len(imp.TraceIDs)
		if size != j.PassSize {
			j.Phase = 1
			j.PassSize = size
		}
	}
	if phase == 5 {
		j.Phase = 10
		j.PassSize = len(imp.Records)
		slices.Sort(imp.SourceEventIDs)
		slices.Sort(imp.TraceIDs)
		slices.Sort(imp.ThreadIDs)
		slices.Sort(imp.ExpectedRunIDs)
	}
}
