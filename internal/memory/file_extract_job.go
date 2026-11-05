package memory

import (
	"context"
	"errors"
	"slices"
	"time"
)

func viewExtractJob(r Record) FileExtractJobView {
	return FileExtractJobView{ID: r.ID, Revision: r.Revision, Status: r.Status, Job: *r.FileExtract, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// createExtractJob records one upload as a new job (a stopped job of an
// earlier upload is never reused) and points the document at it; with
// nothing fresh to extract the job is done at once.
func (s *Service) createExtractJob(ctx context.Context, d Decision, scope Scope, label, docID string, fresh, kept []FileExtractChunk, delta FileExtractDelta, skipped []ExtractSkip) (Record, error) {
	id, status := "file-extract-job-"+ID(), JobStatusPending
	if len(fresh) == 0 {
		status = JobStatusDone
	}
	now := time.Now().UTC()
	out := Record{ID: id, Scope: scope, Kind: fileExtractJobKind, Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
		FileExtract: &FileExtractJob{Label: label, Document: docID, Sources: extractSourceIDs(append(slices.Clone(kept), fresh...)), Chunks: fresh, Kept: kept,
			Total: len(fresh), Delta: delta, Skipped: skipped, RequestedBy: d.PrincipalID}}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, "jobs", out); err != nil {
			return err
		}
		target := &Ref{Collection: "jobs", ID: id, Revision: 1}
		doc, err := tx.Get(ctx, "jobs", scope, docID)
		switch {
		case errors.Is(err, ErrNotFound):
			err = tx.Insert(ctx, "jobs", Record{ID: docID, Scope: scope, Kind: fileExtractDocKind, Status: JobStatusDone, Revision: 1, Target: target, CreatedAt: now, UpdatedAt: now})
		case err == nil && doc.Kind == fileExtractDocKind:
			doc.Target, doc.UpdatedAt, doc.Revision = target, now, doc.Revision+1
			err = tx.Replace(ctx, "jobs", doc, doc.Revision-1)
		case err == nil:
			err = ErrConflict
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, scope, d.PrincipalID, "file_extract:job_created", id)
	})
	return out, err
}

// extractDocState returns the chunks the document's last upload has
// extracted so far, minus any whose source was deleted since.
func (s *Service) extractDocState(ctx context.Context, scope Scope, docID string) ([]FileExtractChunk, error) {
	doc, err := s.Repo.Get(ctx, "jobs", scope, docID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if doc.Kind != fileExtractDocKind || doc.Target == nil {
		return nil, ErrConflict
	}
	last, err := s.extractJob(ctx, s.Repo, scope, doc.Target.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job := last.FileExtract
	done := append(slices.Clone(job.Kept), job.Chunks[:min(job.Next, len(job.Chunks))]...)
	live, out := map[string]bool{}, []FileExtractChunk{}
	for _, ch := range done {
		ok, seen := live[ch.Source.ID]
		if !seen {
			src, err := s.Repo.Get(ctx, "sources", scope, ch.Source.ID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			ok = err == nil && src.Status == "active"
			live[ch.Source.ID] = ok
		}
		if ok {
			out = append(out, ch)
		}
	}
	return out, nil
}

func (s *Service) extractJob(ctx context.Context, tx Tx, scope Scope, id string) (Record, error) {
	r, err := tx.Get(ctx, "jobs", scope, id)
	if err == nil && (r.Kind != fileExtractJobKind || r.FileExtract == nil) {
		err = ErrNotFound
	}
	return r, err
}

// GetFileExtractJob shows a job's progress; chunk text never leaves the server.
func (s *Service) GetFileExtractJob(ctx context.Context, token string, scope Scope, id string) (FileExtractJobView, error) {
	if !validRunLabel(id, 256) {
		return FileExtractJobView{}, ErrInvalid
	}
	if _, err := s.wikiAuthorize(ctx, token, "read:memory_candidates", scope, id, ""); err != nil {
		return FileExtractJobView{}, err
	}
	r, err := s.extractJob(ctx, s.Repo, scope, id)
	if err != nil {
		return FileExtractJobView{}, err
	}
	return viewExtractJob(r), nil
}

// extractJobClaimable lists the states a run may pick up; any other state
// (done, or one an operator set such as a cancel) is left alone.
func extractJobClaimable(r Record, now time.Time) bool {
	switch r.Status {
	case JobStatusPending, JobStatusFailed, JobStatusRetry:
		return true
	case JobStatusRunning:
		return !r.LeaseUntil.After(now)
	}
	return false
}

// claimExtractJob takes the job's lease for the person who asked for it; a
// finished, leased or stopped job is returned unclaimed.
func (s *Service) claimExtractJob(ctx context.Context, d Decision, scope Scope, id string) (Record, bool, error) {
	var claimed Record
	ok := false
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := s.extractJob(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		claimed = r
		// The run captures candidates under the runner's name, so only the
		// person who uploaded the file may run it.
		if r.FileExtract.RequestedBy != d.PrincipalID {
			return ErrDenied
		}
		now := time.Now().UTC()
		if !extractJobClaimable(r, now) {
			return nil
		}
		r.Status, r.LeaseUntil, r.LeaseToken, r.UpdatedAt = JobStatusRunning, now.Add(fileExtractJobLease), ID(), now
		r.Attempts++
		r.FileExtract.Error = ""
		prev := r.Revision
		r.Revision++
		if err = tx.Replace(ctx, "jobs", r, prev); err != nil {
			return err
		}
		claimed, ok = r, true
		return nil
	})
	return claimed, ok, err
}

// saveExtractJob writes progress while the lease is still ours.
func (s *Service) saveExtractJob(ctx context.Context, r Record) (Record, error) {
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, err := s.extractJob(ctx, tx, r.Scope, r.ID)
		if err != nil {
			return err
		}
		if current.LeaseToken != r.LeaseToken || current.Revision != r.Revision {
			return ErrConflict
		}
		r.UpdatedAt = time.Now().UTC()
		if r.Status == JobStatusRunning { // each finished chunk renews the lease
			r.LeaseUntil = r.UpdatedAt.Add(fileExtractJobLease)
		}
		r.Revision++
		return tx.Replace(ctx, "jobs", r, current.Revision)
	})
	return r, err
}

// RunFileExtractJob works through the remaining chunks. A budget stop or an
// error leaves the job failed at the chunk it reached; running it again
// resumes there.
func (s *Service) RunFileExtractJob(ctx context.Context, token string, scope Scope, id string) (FileExtractJobView, error) {
	if !validRunLabel(id, 256) {
		return FileExtractJobView{}, ErrInvalid
	}
	d, err := s.extractActor(ctx, token, "write:memory_candidates", scope)
	if err != nil {
		return FileExtractJobView{}, err
	}
	r, ok, err := s.claimExtractJob(ctx, d, scope, id)
	if err != nil || !ok {
		return viewExtractJob(r), err
	}
	r, _, err = s.runExtractJob(ctx, token, d, r)
	if err != nil {
		return FileExtractJobView{}, err
	}
	return viewExtractJob(r), nil
}

// runExtractJob runs a claimed job; runErr is the chunk failure the job was
// saved as failed with, err a failure to save. A done chunk drops its text.
func (s *Service) runExtractJob(ctx context.Context, token string, d Decision, r Record) (Record, error, error) {
	job := r.FileExtract
	budget := &WikiJob{MaxTotalTokens: fileExtractJobTokens}
	var err error
	for job.Next < len(job.Chunks) {
		ids, skipped, runErr := s.extractAndCapture(ctx, token, d, r.Scope, budget, job.Chunks[job.Next])
		job.Calls, job.Tokens = job.Calls+budget.Calls, job.Tokens+budget.Tokens
		budget.Calls, budget.Tokens = 0, 0
		if runErr != nil {
			code, _ := errorResponse(runErr)
			job.Error, r.Status = code, JobStatusFailed
			r, err = s.saveExtractJob(ctx, r)
			return r, runErr, err
		}
		job.CandidatesCreated += len(ids)
		job.Skipped = append(job.Skipped, skipped...)
		job.Chunks[job.Next].Text, job.Chunks[job.Next].Candidates = "", ids
		job.Next++
		if job.Next == len(job.Chunks) {
			r.Status = JobStatusDone
		}
		if r, err = s.saveExtractJob(ctx, r); err != nil {
			return r, nil, err
		}
		job = r.FileExtract
	}
	return r, nil, nil
}

// ExtractKnowledgeMemories turns an existing knowledge source into candidates,
// with the knowledge file's source anchor as their evidence.
func (s *Service) ExtractKnowledgeMemories(ctx context.Context, token, id string, in KnowledgeExtractInput) (FileMemoriesResult, error) {
	if !validRunLabel(id, 256) {
		return FileMemoriesResult{}, ErrInvalid
	}
	d, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	src, err := s.GetKnowledgeSource(ctx, token, id, in.Scope)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	text, err := s.knowledgeSourceText(ctx, in.Scope, src)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	anchor, err := s.Get(ctx, token, "sources", in.Scope, src.SourceEventID)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	if err = s.defendSpace(ctx, "", defenseGuard{action: "write:memory_candidates"}, in.Scope, &text); err != nil {
		return FileMemoriesResult{}, err
	}
	chunks, redacted, err := extractFileChunks([]string{text}, false)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	out := FileMemoriesResult{Skipped: extractRedactionNote(redacted)}
	if in.DryRun {
		out.Status = JobStatusDone
		return out, s.previewChunks(ctx, token, d, in.Scope, chunks, &out)
	}
	for i := range chunks {
		chunks[i].Source = Ref{Collection: "sources", ID: anchor.ID, Revision: anchor.Revision}
	}
	return s.extractStored(ctx, token, d, in.Scope, src.Knowledge.DisplayName, "knowledge:"+src.ID, chunks, out)
}
