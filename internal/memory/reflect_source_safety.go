package memory

import (
	"context"
	"slices"
	"unicode/utf8"
)

type reflectSourceSnapshot struct {
	record   Record
	transfer bool
	dateOnly bool
	expanded bool
}

func (run *reflectRun) keepSources(records []Record, transfer bool) {
	if run.sourceRecords == nil {
		run.sourceRecords = map[string]reflectSourceSnapshot{}
	}
	for _, r := range records {
		old, exists := run.sourceRecords[r.ID]
		if !exists {
			old.record = r
		}
		// Earlier prompt text may still depend on the first admitted version.
		// Upgrade its obligations without replacing the snapshot that binds it.
		old.transfer = transfer || old.transfer
		old.dateOnly = false
		run.sourceRecords[r.ID] = old
	}
}

func (run *reflectRun) noteSourceFailure(tool, reason string) {
	for _, note := range run.sourceNotes {
		if note.Tool == tool && note.Error == reason {
			return
		}
	}
	run.sourceNotes = append(run.sourceNotes, ReflectToolStep{Tool: tool, IDs: []string{}, Error: reason})
}

// Stored source text needs the same record and transfer checks as other evidence.
// The prompt-only passage is local; reread inputs always leave for their model.
func (s *Service) reflectSourceReadable(ctx context.Context, token string, in ReflectInput, r Record, transfer bool) bool {
	return s.reflectSourceReadableFor(ctx, token, in, r, transfer, false)
}

func (s *Service) reflectSourceReadableFor(ctx context.Context, token string, in ReflectInput, r Record, transfer, dateOnly bool) bool {
	kinds := []string{conversationSourceKind}
	if dateOnly {
		kinds = []string{conversationSourceKind, "message"}
	}
	return s.reflectSourceReadableKinds(ctx, token, in, r, transfer, dateOnly, kinds)
}

func (s *Service) reflectSourceReadableKinds(ctx context.Context, token string, in ReflectInput, r Record, transfer, dateOnly bool, kinds []string) bool {
	filters := reflectFilters(in)
	criteria, err := normalizeSearchCriteria(SearchCriteria{Kinds: kinds, Filters: &filters})
	if err != nil || r.Scope != in.Scope || !live(r) || r.ContentErased || r.PrivacyQuarantined || r.Sensitivity == "restricted" || !criteria.matchesRecord(r) {
		return false
	}
	if _, err = s.authorize(ctx, token, "read:sources", in.Scope, r.ID, structuredText(r)); err != nil {
		return false
	}
	if len(r.Sources) > 0 && checkSources(ctx, s.Repo, r) != nil {
		return false
	}
	if transfer {
		refs := append(slices.Clone(r.Sources), sourceReference(r, "reflect_source"))
		body := redactSessionPrivate(r.Text)
		if dateOnly {
			body = structuredText(r.CollectedAt)
		}
		_, err = s.authorizeTransfer(ctx, token, "context:read", in.Scope, r.ID, r.Sensitivity, wikiDestination, body, refs)
		return err == nil
	}
	return true
}

// Preserve source snapshots through the model call and prompt-only delivery.
// A later erasure/change must reject delivery, including when it keeps revision.
func (s *Service) verifyReflectSources(ctx context.Context, token string, run *reflectRun) error {
	if len(run.sourceRecords) == 0 {
		return nil
	}
	for _, snapshot := range run.sourceRecords {
		if !s.reflectSnapshotReadable(ctx, token, run.in, snapshot) {
			return ErrDenied
		}
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		for id, snapshot := range run.sourceRecords {
			if err := verifyReflectSourceTx(ctx, tx, run.in.Scope, id, snapshot); err != nil {
				return err
			}
		}
		return nil
	})
}

// Expanded tools may read any source kind, but retain every admission and transfer gate.
func (s *Service) reflectSnapshotReadable(ctx context.Context, token string, in ReflectInput, snapshot reflectSourceSnapshot) bool {
	if snapshot.expanded {
		return s.reflectSourceReadableKinds(ctx, token, in, snapshot.record, snapshot.transfer, false, nil)
	}
	return s.reflectSourceReadableFor(ctx, token, in, snapshot.record, snapshot.transfer, snapshot.dateOnly)
}

// Verify and fence one source plus its dependencies inside the delivery transaction.
func verifyReflectSourceTx(ctx context.Context, tx Tx, scope Scope, id string, snapshot reflectSourceSnapshot) error {
	current, err := tx.Get(ctx, "sources", scope, id)
	if err != nil {
		return err
	}
	original := snapshot.record
	current.MemoryReferenceFence, original.MemoryReferenceFence = 0, 0
	if current.Scope != scope || !live(current) || current.ContentErased || current.PrivacyQuarantined || structuredText(current) != structuredText(original) {
		return ErrConflict
	}
	checked := searchReadTx{Tx: tx}
	if len(current.Sources) > 0 {
		if err := checkSources(ctx, checked, current); err != nil {
			return err
		}
	}
	if snapshot.transfer {
		refs := append(slices.Clone(current.Sources), sourceReference(current, "reflect_source"))
		if err := checkTransferSources(ctx, checked, current.Scope, current.Sensitivity, refs); err != nil {
			return err
		}
	}
	return tx.FenceSource(ctx, scope, id)
}

// Optional sources must fit the combined prompt, including tools, directives and
// reread lines. Drop whole optional blocks before consuming the answer's budget.
func (run *reflectRun) boundSources() {
	for {
		system, user := reflectPrompt(run, false)
		if utf8.RuneCountInString(system+user)/3 <= reflectMaxPromptTokens {
			return
		}
		if len(run.passages) > 0 {
			run.passages = run.passages[:len(run.passages)-1]
			run.noteSourceFailure("conversation_passages", "prompt_budget")
		} else if len(run.sourceLines) > 0 {
			run.sourceLines = run.sourceLines[:len(run.sourceLines)-1]
			run.noteSourceFailure("source_reread", "prompt_budget")
		} else {
			return
		}
	}
}
