package memory

import (
	"cmp"
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
// job is the upload's plan with its document, chunks and delta filled in.
func (s *Service) createExtractJob(ctx context.Context, d Decision, scope Scope, job FileExtractJob) (Record, error) {
	id, status, docID := "file-extract-job-"+ID(), JobStatusPending, job.Document
	if len(job.Chunks) == 0 {
		status = JobStatusDone
	}
	now := time.Now().UTC()
	job.Sources, job.RequestedBy = extractSourceIDs(append(slices.Clone(job.Kept), job.Chunks...)), d.PrincipalID
	out := Record{ID: id, Scope: scope, Kind: fileExtractJobKind, Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
		FileExtract: &job}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		target := &Ref{Collection: "jobs", ID: id, Revision: 1}
		doc, err := tx.Get(ctx, "jobs", scope, docID)
		switch {
		case errors.Is(err, ErrNotFound):
			err = tx.Insert(ctx, "jobs", Record{ID: docID, Scope: scope, Kind: fileExtractDocKind, Status: JobStatusDone, Revision: 1, Target: target, CreatedAt: now, UpdatedAt: now,
				CreatedBy: d.PrincipalID, CreatedByType: d.Role, Tags: job.Tags})
		case err == nil && doc.Kind == fileExtractDocKind:
			if doc.Target != nil {
				out.FileExtract.Previous = doc.Target.ID
			}
			out.FileExtract.Tags = doc.Tags
			doc.Target, doc.UpdatedAt, doc.Revision = target, now, doc.Revision+1
			err = tx.Replace(ctx, "jobs", doc, doc.Revision-1)
		case err == nil:
			err = ErrConflict
		}
		if err != nil {
			return err
		}
		if err = tx.Insert(ctx, "jobs", out); err != nil {
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

// GetFileExtractJob shows a job's progress; chunk text never leaves the server. The
// owner's poll of a job waiting on a provider batch resumes following it in the background.
func (s *Service) GetFileExtractJob(ctx context.Context, token string, scope Scope, id string) (FileExtractJobView, error) {
	if !validRunLabel(id, 256) {
		return FileExtractJobView{}, ErrInvalid
	}
	d, err := s.wikiAuthorize(ctx, token, "read:memory_candidates", scope, id, "")
	if err != nil {
		return FileExtractJobView{}, err
	}
	r, err := s.extractJob(ctx, s.Repo, scope, id)
	if err == nil && !ownsJob(d, r) {
		err = ErrNotFound // an agent sees only the jobs it started
	}
	if err != nil {
		return FileExtractJobView{}, err
	}
	out := viewExtractJob(r)
	// A batch whose poll loop a restart ended is picked up again by its owner's poll.
	if extractBatchWaiting(out) && r.FileExtract.RequestedBy == d.PrincipalID {
		s.followExtractJob(context.WithoutCancel(ctx), token, scope, id)
	}
	return out, nil
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
		if !ownsJob(d, r) {
			return ErrNotFound // an agent is not told another principal's job exists
		}
		// The worker (a service) may only resume a job waiting on a provider batch (ResumeExtractBatches).
		if r.FileExtract.RequestedBy != d.PrincipalID && (d.Role != roleService || r.FileExtract.Batch == nil) {
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
	d, settings, err := s.extractActor(ctx, token, "write:memory_candidates", scope, roleHuman, roleAgent)
	if err != nil {
		return FileExtractJobView{}, err
	}
	return s.runClaimedExtractJob(ctx, token, d, settings, scope, id)
}

// runClaimedExtractJob claims the job for d and runs it.
func (s *Service) runClaimedExtractJob(ctx context.Context, token string, d Decision, settings SpaceConfig, scope Scope, id string) (FileExtractJobView, error) {
	r, ok, err := s.claimExtractJob(ctx, d, scope, id)
	if err != nil || !ok {
		return viewExtractJob(r), err
	}
	if r.FileExtract.Settings.Mode == "" { // queued before settings were stored with the job
		r.FileExtract.Settings, r.FileExtract.Zone, r.FileExtract.Language = settings.extractBase(), settings.zone().String(), settings.MemoryLanguage
	}
	r, _, err = s.runExtractJob(ctx, token, d, r)
	if err != nil {
		return FileExtractJobView{}, err
	}
	return viewExtractJob(r), nil
}

// runExtractJob runs a claimed job; runErr is the chunk failure the job was
// saved as failed with, err a failure to save. A done chunk drops its text.
func (s *Service) runExtractJob(ctx context.Context, token string, d Decision, r Record) (_ Record, runErr, err error) {
	ctx, sp := startJobSpan(ctx, "job.file_extract", r.ID)
	defer func() { sp.end(cmp.Or(runErr, err)) }()
	job := r.FileExtract
	budget := &WikiJob{MaxTotalTokens: fileExtractJobTokens}
	var answers map[int]ExtractPreviewResult
	if b := s.extractBatcher(ctx, r.Scope, job); b != nil {
		answers, err = s.extractBatchAnswers(ctx, token, d, r.Scope, b, job)
		if errors.Is(err, errExtractBatchPending) {
			r.Status, job.Error = JobStatusRetry, extractBatchPending
			r, err = s.saveExtractJob(ctx, r)
			return r, nil, err
		}
		if err != nil {
			return s.failExtractJob(ctx, r, err)
		}
	}
	for job.Next < len(job.Chunks) {
		if _, ok := answers[job.Next]; answers != nil && !ok {
			return s.runExtractJob(ctx, token, d, r) // the batch ended before the job: send the next one
		}
		ids, skipped, runErr := s.nextExtractChunk(ctx, token, d, r.Scope, budget, job, answers)
		job.Calls, job.Tokens = job.Calls+budget.Calls, job.Tokens+budget.Tokens
		budget.Calls, budget.Tokens = 0, 0
		if runErr != nil {
			return s.failExtractJob(ctx, r, runErr)
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

// nextExtractChunk extracts the job's next chunk: from the batch's answer when there is
// one, else by calling the model.
func (s *Service) nextExtractChunk(ctx context.Context, token string, d Decision, scope Scope, budget *WikiJob, job *FileExtractJob, answers map[int]ExtractPreviewResult) ([]string, []ExtractSkip, error) {
	ch := job.Chunks[job.Next]
	res, ok := answers[job.Next]
	if !ok {
		return s.extractAndCapture(ctx, token, d, scope, budget, job, ch)
	}
	return s.captureChunk(ctx, token, d, scope, job, ch, res)
}

// failExtractJob saves the job as failed with runErr's code; running it again resumes.
func (s *Service) failExtractJob(ctx context.Context, r Record, runErr error) (Record, error, error) {
	code, _ := errorResponse(runErr)
	r.FileExtract.Error, r.Status = code, JobStatusFailed
	r, err := s.saveExtractJob(ctx, r)
	return r, runErr, err
}

// ExtractKnowledgeMemories turns an existing knowledge source into candidates,
// with the knowledge file's source anchor as their evidence.
func (s *Service) ExtractKnowledgeMemories(ctx context.Context, token, id string, in KnowledgeExtractInput) (FileMemoriesResult, error) {
	return s.extractKnowledge(ctx, token, id, in, "")
}

func (s *Service) extractKnowledge(ctx context.Context, token, id string, in KnowledgeExtractInput, round string) (FileMemoriesResult, error) {
	if !validRunLabel(id, 256) {
		return FileMemoriesResult{}, ErrInvalid
	}
	d, settings, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope, roleHuman)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	plan, err := s.extractPlan(settings, ExtractOptions{}, nil)
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
	chunks, redacted, err := extractFileChunks([]string{text}, false, plan.Settings)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	out := FileMemoriesResult{Skipped: extractRedactionNote(redacted)}
	if in.DryRun {
		out.Status = JobStatusDone
		return out, s.previewChunks(ctx, token, d, in.Scope, &plan, chunks, &out)
	}
	for i := range chunks {
		chunks[i].Source = Ref{Collection: "sources", ID: anchor.ID, Revision: anchor.Revision}
	}
	plan.Label = src.Knowledge.DisplayName
	return s.extractStored(ctx, token, d, in.Scope, plan, "knowledge:"+src.ID, round, chunks, out, fileExtractSyncChunks, false)
}
