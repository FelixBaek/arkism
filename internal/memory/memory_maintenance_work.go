package memory

import (
	"context"
	"encoding/json"
	"slices"
)

func maintenanceWorkPage(ctx context.Context, p *Postgres, j *memoryMaintenance) error {
	imp := &j.Privacy.Impact
	var rows []Record
	var err error
	if j.Phase == 10 {
		evidence := imp.Records
		if evidence == nil {
			evidence = []Ref{}
		}
		refs, e := json.Marshal(evidence)
		if e != nil {
			return ErrInvalid
		}
		rows, err = p.selectRecords(ctx, colWorkCheckpoints, j.Scope, `(NOT COALESCE((doc->>'content_erased')::boolean,false) AND ($6 OR EXISTS (
 SELECT 1 FROM jsonb_array_elements(COALESCE(doc->'work_checkpoint'->'package'->'source_refs','[]'::jsonb)) w
 JOIN jsonb_array_elements($7::jsonb) evidence ON w->>'collection'=evidence->>'collection' AND w->>'id'=evidence->>'id'
 ) OR EXISTS (
 SELECT 1 FROM jsonb_array_elements(COALESCE(memory_records.doc->'work_checkpoint'->'archive_messages','[]'::jsonb)) w
 JOIN memory_records basis ON basis.workspace_id=$2 AND basis.project_id=$3 AND basis.user_id=$4 AND basis.agent_id=$5
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(basis.doc->'archive_messages','[]'::jsonb) || COALESCE(basis.doc->'work_checkpoint'->'archive_messages','[]'::jsonb)) a
 WHERE EXISTS (SELECT 1 FROM jsonb_array_elements($7::jsonb) evidence WHERE evidence->>'collection'=basis.collection AND evidence->>'id'=basis.id)
 AND w->>'archive_id'=a->>'archive_id' AND COALESCE(w->>'sqlite_step','')=COALESCE(a->>'sqlite_step','')
 AND ((COALESCE(w->>'record_key','')<>'' AND w->>'record_key'=a->>'record_key' AND COALESCE(w->>'message_sha256','')<>'' AND w->>'message_sha256'=a->>'message_sha256')
 OR (w->>'chunk_index'=a->>'chunk_index' AND w->>'chunk_offset'=a->>'chunk_offset'
 AND CASE WHEN COALESCE(w->>'message_sha256','')<>'' AND COALESCE(a->>'message_sha256','')<>'' THEN w->>'message_sha256'=a->>'message_sha256' ELSE w->>'sha256'=a->>'sha256' END))
 ))) AND id>$8`, []any{j.Request.TargetType == "subject", string(refs), j.Cursor}, "id ASC", 100)
	} else {
		ids := []string{}
		for _, ref := range imp.Records {
			if ref.Collection == colWorkCheckpoints {
				ids = append(ids, ref.ID)
			}
		}
		rows, err = p.selectRecords(ctx, colWorkCheckpointReceipts, j.Scope, `doc->'work_checkpoint'->>'receipt_target_id'=ANY($6) AND id>$7`, []any{ids, j.Cursor}, "id ASC", 100)
	}
	if err != nil {
		return err
	}
	for _, r := range rows {
		col := colWorkCheckpoints
		if j.Phase == 11 {
			col = colWorkCheckpointReceipts
		}
		ref := Ref{Collection: col, ID: r.ID, Revision: r.Revision}
		if !slices.Contains(imp.Records, ref) {
			imp.Records = append(imp.Records, ref)
		}
		j.Cursor = r.ID
		j.Scanned++
	}
	if len(rows) < 100 {
		j.Cursor = ""
		if j.Phase == 10 && j.PassSize != len(imp.Records) {
			j.PassSize = len(imp.Records)
			return nil
		}
		if j.Phase == 10 {
			j.Phase = 11
		} else {
			j.Phase = 6
			j.Offset = 0
			if j.Request.ExpectedRevision != 0 {
				return checkPrivacyExpectedRevision(*j.Request, *imp)
			}
		}
	}
	return nil
}
