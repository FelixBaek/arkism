package memory

import (
	"context"
	"encoding/json"
	"errors"
)

var errSpaceRenamePreview = errors.New("space rename preview")

const spaceRenamePage = 500

// RenameSpace moves one project to a new project id of the same workspace in
// one serializable transaction: memory records (scope columns, payload and doc,
// remapped ids), the graph, traces and source events, code index, memberships,
// permissions and decision counts. A preview runs the same transaction and
// rolls it back, so its counts are exact. The target must be empty and no job
// of the source may hold a lease. Run it with the server stopped: a running
// server could write to the old name between the check and the commit.
func (p *Postgres) RenameSpace(ctx context.Context, from, to Scope, execute bool) (SpaceRenameReport, error) {
	from, to = spaceScope(from), spaceScope(to)
	if !validScope(from) || !validScope(to) || from.WorkspaceID != to.WorkspaceID || from == to {
		return SpaceRenameReport{}, ErrInvalid
	}
	var report SpaceRenameReport
	err := p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		pg, ok := tx.(*Postgres)
		if !ok {
			return ErrUnavailable
		}
		report = SpaceRenameReport{From: from, To: to, Executed: execute, Records: map[string]int64{}, Tables: map[string]int64{}}
		if err := pg.renameSpaceRows(ctx, from, to, &report); err != nil {
			return err
		}
		if !execute {
			return errSpaceRenamePreview
		}
		return nil
	})
	if errors.Is(err, errSpaceRenamePreview) {
		err = nil
	}
	return report, err
}

func (p *Postgres) renameSpaceRows(ctx context.Context, from, to Scope, report *SpaceRenameReport) error {
	if err := p.checkSpaceRename(ctx, from, to); err != nil {
		return err
	}
	subs, err := p.SpaceSubScopes(ctx, from, nil, spaceSubScopeMax+1)
	if err != nil {
		return err
	}
	if len(subs) > spaceSubScopeMax {
		return ErrUnavailable
	}
	report.SubScopes = len(subs)
	m := newSpaceRenamer(from, to, subs)
	if err = p.planSpaceRename(ctx, from, m); err != nil {
		return err
	}
	report.IDsRemapped = len(m.ids)
	if err = p.moveSpaceTables(ctx, from, to, m, report); err != nil {
		return err
	}
	return p.moveSpaceRecords(ctx, from, m, report)
}

// checkSpaceRename refuses a target with any row and a source with a leased job.
func (p *Postgres) checkSpaceRename(ctx context.Context, from, to Scope) error {
	var used, leased bool
	err := p.q().QueryRow(ctx, `SELECT
 EXISTS (SELECT 1 FROM memory_records WHERE workspace_id=$1 AND project_id=$2)
 OR EXISTS (SELECT 1 FROM entities WHERE organization_id=$1 AND project_id=$2)
 OR EXISTS (SELECT 1 FROM execution_traces WHERE organization_id=$1 AND project_id=$2)
 OR EXISTS (SELECT 1 FROM code_repos WHERE organization_id=$1 AND project_id=$2)
 OR EXISTS (SELECT 1 FROM project_memberships WHERE organization_id=$1 AND project_id=$2)
 OR EXISTS (SELECT 1 FROM decision_daily_counts WHERE workspace_id=$1 AND project_id=$2),
 EXISTS (SELECT 1 FROM memory_records WHERE collection=$4 AND workspace_id=$1 AND project_id=$3 AND lease_until > now())`,
		to.WorkspaceID, to.ProjectID, from.ProjectID, colJobs).Scan(&used, &leased)
	switch {
	case err != nil:
		return pgErr(err)
	case used || leased:
		return ErrConflict
	}
	return nil
}

// planSpaceRename reads every record of the space once and maps the ids whose formula it knows.
func (p *Postgres) planSpaceRename(ctx context.Context, from Scope, m *spaceRenamer) error {
	links := []Record{}
	for col, id := "", ""; ; {
		rows, err := p.q().Query(ctx, `SELECT collection, id, payload FROM memory_records WHERE workspace_id=$1 AND project_id=$2 AND (collection,id) > ($3,$4) ORDER BY collection, id LIMIT $5`,
			from.WorkspaceID, from.ProjectID, col, id, spaceRenamePage)
		if err != nil {
			return pgErr(err)
		}
		n := 0
		for rows.Next() {
			var payload []byte
			if err = rows.Scan(&col, &id, &payload); err != nil {
				rows.Close()
				return ErrUnavailable
			}
			n++
			r, err := pgDecode(payload)
			if err != nil {
				rows.Close()
				return err
			}
			if col == colWikiLinks && r.Wiki != nil && r.Wiki.Link != nil {
				links = append(links, Record{ID: r.ID, Scope: r.Scope, Wiki: &WikiRecord{Link: r.Wiki.Link}})
				continue
			}
			m.plan(col, r)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return pgErr(err)
		}
		if n < spaceRenamePage {
			break
		}
	}
	for _, l := range links {
		m.planLink(l)
	}
	return nil
}

// moveSpaceTables moves the rows outside memory_records. Parent and child of
// each foreign key move in one statement, so the keys hold at its end.
func (p *Postgres) moveSpaceTables(ctx context.Context, from, to Scope, m *spaceRenamer, report *SpaceRenameReport) error {
	olds, news := make([]string, 0, len(m.ids)), make([]string, 0, len(m.ids))
	for o, n := range m.ids {
		olds, news = append(olds, o), append(news, n)
	}
	if _, err := p.q().Exec(ctx, `CREATE TEMP TABLE space_rename_ids (old text PRIMARY KEY, new text NOT NULL) ON COMMIT DROP`); err != nil {
		return pgErr(err)
	}
	if _, err := p.q().Exec(ctx, `INSERT INTO space_rename_ids SELECT * FROM unnest($1::text[], $2::text[])`, olds, news); err != nil {
		return pgErr(err)
	}
	statements := []struct {
		names []string
		sql   string
	}{
		{[]string{"entities", "relations"}, `WITH e AS (UPDATE entities SET project_id=$3, external_id=COALESCE((SELECT new FROM space_rename_ids WHERE old=external_id), external_id) WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 r AS (UPDATE relations SET project_id=$3, source_id=COALESCE((SELECT new FROM space_rename_ids WHERE old=source_id), source_id), target_id=COALESCE((SELECT new FROM space_rename_ids WHERE old=target_id), target_id) WHERE organization_id=$1 AND project_id=$2 RETURNING 1)
 SELECT (SELECT count(*) FROM e), (SELECT count(*) FROM r)`},
		{[]string{"execution_traces", "source_events"}, `WITH t AS (UPDATE execution_traces SET project_id=$3 WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 s AS (UPDATE source_events SET project_id=$3 WHERE organization_id=$1 AND project_id=$2 RETURNING 1)
 SELECT (SELECT count(*) FROM t), (SELECT count(*) FROM s)`},
		{[]string{"code_repos", "code_symbols", "code_edges"}, `WITH r AS (UPDATE code_repos SET project_id=$3 WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 s AS (UPDATE code_symbols SET project_id=$3 WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 e AS (UPDATE code_edges SET project_id=$3 WHERE organization_id=$1 AND project_id=$2 RETURNING 1)
 SELECT (SELECT count(*) FROM r), (SELECT count(*) FROM s), (SELECT count(*) FROM e)`},
		{[]string{"project_memberships", "principal_permissions"}, `WITH m AS (UPDATE project_memberships SET project_id=$3 WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 x AS (UPDATE principal_permissions SET project_id=$3 WHERE organization_id=$1 AND project_id=$2 RETURNING 1)
 SELECT (SELECT count(*) FROM m), (SELECT count(*) FROM x)`},
		{[]string{"decision_daily_counts"}, `WITH d AS (UPDATE decision_daily_counts SET project_id=$3 WHERE workspace_id=$1 AND project_id=$2 RETURNING 1) SELECT count(*) FROM d`},
	}
	for _, st := range statements {
		counts := make([]int64, len(st.names))
		dest := make([]any, len(counts))
		for i := range counts {
			dest[i] = &counts[i]
		}
		if err := p.q().QueryRow(ctx, st.sql, from.WorkspaceID, from.ProjectID, to.ProjectID).Scan(dest...); err != nil {
			return pgErr(err)
		}
		for i, name := range st.names {
			report.Tables[name] = counts[i]
		}
	}
	return nil
}

// moveSpaceRecords rewrites the records page by page; a moved row leaves the source, so each page starts over.
func (p *Postgres) moveSpaceRecords(ctx context.Context, from Scope, m *spaceRenamer, report *SpaceRenameReport) error {
	type row struct {
		col, id string
		payload []byte
	}
	for {
		rows, err := p.q().Query(ctx, `SELECT collection, id, payload FROM memory_records WHERE workspace_id=$1 AND project_id=$2 ORDER BY collection, id LIMIT $3`, from.WorkspaceID, from.ProjectID, spaceRenamePage)
		if err != nil {
			return pgErr(err)
		}
		page := []row{}
		for rows.Next() {
			var r row
			if err = rows.Scan(&r.col, &r.id, &r.payload); err != nil {
				rows.Close()
				return ErrUnavailable
			}
			page = append(page, r)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return pgErr(err)
		}
		for _, r := range page {
			if err = p.moveSpaceRecord(ctx, m, r.col, r.id, r.payload); err != nil {
				return err
			}
			report.Records[r.col]++
		}
		if len(page) < spaceRenamePage {
			return nil
		}
	}
}

func (p *Postgres) moveSpaceRecord(ctx context.Context, m *spaceRenamer, col, id string, payload []byte) error {
	b, err := m.rewrite(payload)
	if err != nil {
		return err
	}
	rec, err := pgDecode(b)
	newID := m.id(id)
	if err != nil || rec.ID != newID || rec.Scope.ProjectID != m.to {
		return ErrUnavailable // a document whose own scope or id did not follow: stop before half a space moves
	}
	tag, err := p.q().Exec(ctx, `UPDATE memory_records SET id=$3, project_id=$4, payload=$5, doc=$6 WHERE collection=$1 AND id=$2`, col, id, newID, m.to, b, json.RawMessage(b))
	if err != nil {
		return pgErr(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
