package memory

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

const candidateEvaluationErasureKind = "candidate_evaluation_erasure"

const memoryEvaluationErasureKind = "memory_evaluation_erasure"

const memoryRelationErasureKind = "memory_relation_erasure"

const sourceMemoryErasureKind = "source_memory_erasure"
const sourceRecordErasureKind = "source_record_erasure"
const sourceSkillErasureKind = "source_skill_erasure"
const sourceMemoryDependentsErasureKind = "source_memory_dependents_erasure"
const sourceRecordsMemoryDependents = "source_records_memory_dependents"
const sourceRelationErasureKind = "source_relation_erasure"
const sourceRecordsSkillsRelations = "source_records_skills_relations"
const sourceRecordsAndSkills = "source_records_and_skills"
const sourceMemoryErasureBatch = 100

var erasureJobKinds = []string{sourceMemoryErasureKind, sourceRecordErasureKind, sourceSkillErasureKind, sourceRelationErasureKind, sourceMemoryDependentsErasureKind, memoryRelationErasureKind, memoryEvaluationErasureKind, candidateEvaluationErasureKind}

const sourceDirectRecords = "source_direct_records"

type artifactCleanup struct {
	ID     string `bson:"id"`
	Digest string `bson:"digest"`
}

type SourceMemoryErasure struct {
	DerivedSourceJobs   []string          `json:"-" bson:"derived_source_jobs,omitempty"`
	QueuedSourceJobs    int64             `json:"queued_source_jobs" bson:"queued_source_jobs,omitempty"`
	PendingArtifacts    []artifactCleanup `json:"-" bson:"pending_artifacts,omitempty"`
	QueuedCandidateJobs int64             `json:"queued_candidate_jobs" bson:"queued_candidate_jobs,omitempty"`
	QueuedMemoryJobs    int64             `json:"queued_memory_jobs" bson:"queued_memory_jobs,omitempty"`
	RelationCursor      string            `json:"-" bson:"relation_cursor,omitempty"`
	RedactedConflicts   int64             `json:"redacted_conflicts" bson:"redacted_conflicts,omitempty"`
	DeletedRecords      map[string]int64  `json:"deleted_records" bson:"deleted_records,omitempty"`
	DeletedMemories     int64             `json:"deleted_memories" bson:"deleted_memories"`
	DeletedCandidates   int64             `json:"deleted_candidates" bson:"deleted_candidates"`
}
type SourceErasureStatus struct {
	PendingArtifactCleanup int                     `json:"pending_artifact_cleanup"`
	DependentCleanup       *DependentErasureStatus `json:"dependent_cleanup,omitempty"`
	Revision               int64                   `json:"revision"`
	JobID                  string                  `json:"job_id"`
	Target                 Ref                     `json:"target"`
	Status                 string                  `json:"status"`
	Attempts               int                     `json:"attempts"`
	SourceMemoryErasure
	UpdatedAt           time.Time `json:"updated_at"`
	Coverage            string    `json:"coverage"`
	FullErasureVerified bool      `json:"full_erasure_verified"`
}

func sourceMemoryErasureID(scope Scope, id string) string {
	return "source-memory-erasure-" + ContentHash(structuredText(scope)+"\n"+id)
}
func enqueueSourceMemoryErasure(ctx context.Context, tx Tx, scope Scope, id string, revision int64, actor string) error {
	return enqueueSourceMemoryErasureAs(ctx, tx, scope, id, revision, actor, "human", "")
}
func enqueueSourceMemoryErasureAs(ctx context.Context, tx Tx, scope Scope, id string, revision int64, actor, actorType, parentID string) error {
	now := time.Now().UTC()
	return tx.Insert(ctx, "jobs", Record{ErasureParentID: parentID, ID: sourceMemoryErasureID(scope, id), Scope: scope, Kind: sourceMemoryDependentsErasureKind, Status: JobStatusPending, Revision: 1, CreatedBy: actor, CreatedByType: actorType, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: "sources", ID: id, Revision: revision}, SourceMemoryErasure: &SourceMemoryErasure{}})
}

// This write must be in the same transaction as inserting a source-backed record.

func insertSourceRecord(ctx context.Context, tx Tx, col string, r Record) error {
	if !knownCollection(col) || col == "sources" || col == "jobs" || col == "audit_events" {
		return ErrInvalid
	}
	if e := rejectErasedRecord(ctx, tx, col, r); e != nil {
		return e
	}
	if len(r.Sources) == 0 || len(r.Sources) > 64 {
		return ErrInvalid
	}
	sources := r.Sources
	if col == colSkillVersions {
		parent, e := tx.Get(ctx, colSkills, r.Scope, r.SkillID)
		if e != nil {
			return e
		}
		if !live(parent) {
			return ErrConflict
		}
		if e = checkSources(ctx, tx, parent); e != nil {
			return e
		}
		sources = append(append([]SourceRef{}, r.Sources...), parent.Sources...)
	}
	if e := fenceRecordSources(ctx, tx, r.Scope, sources); e != nil {
		return e
	}
	if e := fenceRecordRelationMembers(ctx, tx, col, r); e != nil {
		return e
	}
	return tx.Insert(ctx, col, r)
}

func rejectErasedRecord(ctx context.Context, tx Tx, col string, r Record) error {
	if col == "memories" || col == "memory_candidates" {
		job, e := tx.Get(ctx, "jobs", r.Scope, deletionJobID(col, r.Scope, r.ID))
		if e == nil {
			if !validSourceErasureJob(job) || !isMemoryErasureKind(job.Kind) || job.Target.Collection != col {
				return ErrUnavailable
			}
			return ErrConflict
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
	}
	return nil
}

func fenceRecordSources(ctx context.Context, tx Tx, scope Scope, sources []SourceRef) error {
	ids := map[string]bool{}
	for _, src := range sources {
		if src.SourceID == "" {
			return ErrInvalid
		}
		ids[src.SourceID] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		if e := tx.FenceSource(ctx, scope, id); e != nil {
			return e
		}
	}
	return nil
}

func fenceRecordRelationMembers(ctx context.Context, tx Tx, col string, r Record) error {
	if col == "memory_relations" {
		members := map[[2]string]bool{}
		for _, ref := range r.Relations {
			if ref.Collection != "memories" && ref.Collection != "memory_candidates" {
				continue
			}
			key := [2]string{ref.Collection, ref.ID}
			if members[key] {
				continue
			}
			members[key] = true
			if e := tx.FenceRelationMember(ctx, r.Scope, ref.Collection, ref.ID); e != nil {
				return e
			}
		}
	}
	if col == "evaluations" && r.Target != nil && (r.Target.Collection == "memories" || r.Target.Collection == "memory_candidates") {
		if e := tx.FenceRelationMember(ctx, r.Scope, r.Target.Collection, r.Target.ID); e != nil {
			return e
		}
	}
	return nil
}

func deletionJobID(col string, scope Scope, id string) string {
	if col == "memory_candidates" {
		return "candidate-erasure-" + ContentHash(structuredText(scope)+"\n"+id)
	}
	if col == "memories" {
		return "memory-relation-erasure-" + ContentHash(structuredText(scope)+"\n"+id)
	}
	return sourceMemoryErasureID(scope, id)
}
func validSourceErasureJob(job Record) bool {
	if job.Target == nil || !validRunLabel(job.Target.ID, 256) || job.Target.Revision < 1 || job.CreatedBy == "" || !validErasureActor(job) || job.SourceMemoryErasure == nil {
		return false
	}
	if len(job.SourceMemoryErasure.PendingArtifacts) > sourceMemoryErasureBatch {
		return false
	}
	for _, pending := range job.SourceMemoryErasure.PendingArtifacts {
		if job.Kind != sourceMemoryDependentsErasureKind || !sourceHashPattern.MatchString(pending.Digest) || pending.ID != artifactID(job.Scope, job.Target.ID, pending.Digest) {
			return false
		}
	}
	if job.Kind == candidateEvaluationErasureKind {
		return validCandidateErasureJob(job)
	}
	if isMemoryErasureKind(job.Kind) {
		return validMemoryErasureJob(job)
	}
	return job.Target.Collection == "sources" && job.ID == sourceMemoryErasureID(job.Scope, job.Target.ID) && sourceErasureCollections(job.Kind) != nil
}

func validCandidateErasureJob(job Record) bool {
	return job.Target.Collection == "memory_candidates" && job.ID == deletionJobID("memory_candidates", job.Scope, job.Target.ID) && job.Proposal == nil && (job.SourceMemoryErasure.RelationCursor == "" || validRunLabel(job.SourceMemoryErasure.RelationCursor, 256))
}

func validMemoryErasureJob(job Record) bool {
	return job.Target.Collection == "memories" && job.ID == deletionJobID("memories", job.Scope, job.Target.ID) && (job.Proposal == nil || job.Proposal.Collection == "memory_candidates" && job.Proposal.ID == job.Target.ID && job.Proposal.Revision > 0) && (job.SourceMemoryErasure.RelationCursor == "" || validRunLabel(job.SourceMemoryErasure.RelationCursor, 256))
}

func validErasureActor(job Record) bool {
	if job.CreatedByType == "human" {
		return true
	}
	if job.CreatedByType != "service" || job.ErasureParentID == "" {
		return false
	}
	prefix := "source-memory-erasure-"
	if job.Target != nil && job.Target.Collection == "sources" {
		prefix = "privacy-deletion-"
	}
	digest := strings.TrimPrefix(job.ErasureParentID, prefix)
	return digest != job.ErasureParentID && sourceHashPattern.MatchString(digest)
}
func erasureBatch(ctx context.Context, tx Tx, job Record) ([]Ref, error) {
	if isMemoryErasureKind(job.Kind) {
		return tx.MemoryErasureBatch(ctx, job.Scope, job.Target.ID, job.Proposal != nil, job.SourceMemoryErasure.RelationCursor, job.Kind)
	}
	return tx.SourceErasureBatch(ctx, job.Scope, job.Target.ID, job.Kind)
}
func erasedTargetAbsent(ctx context.Context, tx Tx, job Record) error {
	if job.Target.Collection == "sources" {
		if _, e := sourceDeletion(ctx, tx, job.Scope, job.Target.ID); e != nil {
			return e
		}
	}
	targets := []Ref{*job.Target}
	if isMemoryErasureKind(job.Kind) && job.Proposal != nil {
		targets = append(targets, *job.Proposal)
	}
	for _, ref := range targets {
		if _, e := tx.Get(ctx, ref.Collection, job.Scope, ref.ID); !errors.Is(e, ErrNotFound) {
			if e != nil {
				return e
			}
			return ErrConflict
		}
	}
	return nil
}
func (s *Service) eraseSourceMemories(ctx context.Context, token string, job Record) error {
	if !validSourceErasureJob(job) {
		return ErrInvalid
	}
	action := sourceErasureAction(job.Kind)
	actor, e := s.authorize(ctx, token, action, job.Scope, job.Target.ID, "")
	if e != nil {
		return e
	}
	if actor.Role != "service" {
		return ErrDenied
	}
	if len(job.SourceMemoryErasure.PendingArtifacts) > 0 {
		return s.erasePendingArtifacts(ctx, token, job, actor, action)
	}
	rows, e := erasureBatch(ctx, s.Repo, job)
	if e != nil {
		return e
	}
	if len(rows) > sourceMemoryErasureBatch {
		return ErrUnavailable
	}
	for _, ref := range rows {
		if !slices.Contains(sourceErasureCollections(job.Kind), ref.Collection) || !validRunLabel(ref.ID, 256) || ref.Revision < 1 {
			return ErrUnavailable
		}
	}
	parents, authRows, e := s.erasureSkillParents(ctx, job, rows)
	if e != nil {
		return e
	}
	// Keep provider latency outside the database transaction and cap concurrency.
	for start := 0; start < len(authRows); start += 8 {
		batch := authRows[start:min(start+8, len(authRows))]
		results := make(chan error, len(batch))
		for _, ref := range batch {
			go func() {
				d, err := s.authorize(ctx, token, "erase:"+ref.Collection, job.Scope, ref.ID, "")
				if err == nil && (d.PrincipalID != actor.PrincipalID || d.Role != "service") {
					err = ErrDenied
				}
				results <- err
			}()
		}
		var failed error
		for range batch {
			if err := <-results; err != nil && failed == nil {
				failed = err
			}
		}
		if failed != nil {
			return failed
		}
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		lease, e := erasureLease(ctx, tx, job)
		if e != nil {
			return e
		}
		current, e := erasureBatch(ctx, tx, lease)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(rows, current) {
			return ErrConflict
		}
		impact := PrivacyImpact{Records: append(slices.Clone(authRows), *job.Target)}
		if job.Target.Collection == "sources" {
			impact.SourceEventIDs = []string{job.Target.ID}
		}
		if err := checkPrivacyOverlap(ctx, tx, job.Scope, "", "", impact, false); err != nil {
			if !errors.Is(err, ErrConflict) {
				return err
			}
			return deferHeldErasure(ctx, tx, lease, actor.PrincipalID, action)
		}
		currentParents, e := currentErasureSkillParents(ctx, tx, job, parents)
		if e != nil {
			return e
		}
		progress := *lease.SourceMemoryErasure
		progress.DeletedRecords = maps.Clone(progress.DeletedRecords)
		progress.PendingArtifacts = slices.Clone(progress.PendingArtifacts)
		progress.DerivedSourceJobs = slices.Clone(progress.DerivedSourceJobs)
		if progress.DeletedRecords == nil {
			progress.DeletedRecords = map[string]int64{}
		}
		for _, ref := range rows {
			if e := eraseErasureRow(ctx, tx, job, ref, currentParents, &progress, actor.PrincipalID); e != nil {
				return e
			}
		}
		lease.SourceMemoryErasure = &progress
		lease.Status = JobStatusPending
		if len(rows) == 0 {
			pending, e := nativePurgePendingTx(ctx, tx, job.Scope)
			if e != nil {
				return e
			}
			if !pending {
				lease.Status = JobStatusDone
			}
		}
		lease.LeaseToken = ""
		lease.LeaseUntil = time.Time{}
		lease.Attempts = 0
		lease.UpdatedAt = time.Now().UTC()
		lease.Revision++
		if e = tx.Replace(ctx, "jobs", lease, job.Revision); e != nil {
			return e
		}
		return audit(ctx, tx, job.Scope, actor.PrincipalID, action+"_batch", job.ID)
	})
}
func sourceErasureAction(kind string) string {
	action := "erase:source_memories"
	if isMemoryErasureKind(kind) {
		action = "erase:memory_relations"
	}
	if kind == memoryEvaluationErasureKind || kind == candidateEvaluationErasureKind {
		action = "erase:memory_records"
	}
	if kind == sourceRecordErasureKind {
		action = "erase:source_records"
	}
	if kind == sourceSkillErasureKind {
		action = "erase:source_skills"
	}
	if kind == sourceRelationErasureKind {
		action = "erase:source_relations"
	}
	if kind == sourceMemoryDependentsErasureKind {
		action = "erase:source_memory_dependents"
	}
	return action
}

func (s *Service) erasureSkillParents(ctx context.Context, job Record, rows []Ref) (map[string]Record, []Ref, error) {
	parents := map[string]Record{}
	authRows := append([]Ref{}, rows...)
	for _, ref := range rows {
		if ref.Collection != colSkillVersions {
			continue
		}
		r, e := s.Repo.Get(ctx, colSkillVersions, job.Scope, ref.ID)
		if e != nil {
			return nil, nil, e
		}
		if r.Revision != ref.Revision {
			return nil, nil, ErrConflict
		}
		if _, seen := parents[r.SkillID]; seen {
			continue
		}
		parent, e := s.Repo.Get(ctx, colSkills, job.Scope, r.SkillID)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return nil, nil, e
		}
		parents[r.SkillID] = parent
		if e == nil {
			authRows = append(authRows, Ref{Collection: colSkills, ID: parent.ID, Revision: parent.Revision})
		}
	}
	return parents, authRows, nil
}

func currentErasureSkillParents(ctx context.Context, tx Tx, job Record, parents map[string]Record) (map[string]Record, error) {
	currentParents := maps.Clone(parents)
	for id, parent := range parents {
		current, e := tx.Get(ctx, colSkills, job.Scope, id)
		if parent.ID == "" {
			if !errors.Is(e, ErrNotFound) {
				if e != nil {
					return nil, e
				}
				return nil, ErrConflict
			}
		} else {
			if e != nil {
				return nil, e
			}
			if current.Revision != parent.Revision {
				return nil, ErrConflict
			}
			currentParents[id] = current
		}
	}
	return currentParents, nil
}

func eraseErasureRow(ctx context.Context, tx Tx, job Record, ref Ref, currentParents map[string]Record, progress *SourceMemoryErasure, principalID string) error {
	r, e := tx.Get(ctx, ref.Collection, job.Scope, ref.ID)
	if e != nil {
		return e
	}
	if r.Revision != ref.Revision {
		return ErrConflict
	}
	found, e := erasureRowReferencesTarget(ctx, tx, job, ref, r, currentParents, progress)
	if e != nil {
		return e
	}
	if !found {
		return ErrConflict
	}
	if e = invalidateNativeDynamicsDependencyTx(ctx, tx, job.Scope, ref, "dependency_erased"); e != nil {
		return e
	}
	redacted, e := redactSurvivingConflict(ctx, tx, job, ref, r)
	if e != nil {
		return e
	}
	if redacted {
		progress.RedactedConflicts++
		return nil
	}
	if e = enqueueErasureDependents(ctx, tx, job, ref, r, progress); e != nil {
		return e
	}
	// A lesson-derived source may itself be input to later skill versions.
	// Deleting that source must enqueue its descendants before it vanishes.
	if ref.Collection == "sources" && r.Kind == proposedSkillSourceKind {
		if e = enqueueDerivedSourceErasure(ctx, tx, job, r, progress); e != nil {
			return e
		}
	}
	if ref.Collection == colArtifacts {
		if e = detachArtifactMetadata(ctx, tx, r, principalID); e != nil {
			return e
		}
		progress.PendingArtifacts = append(progress.PendingArtifacts, artifactCleanup{ID: r.ID, Digest: r.Artifact.Digest})
	} else if ref.Collection == "jobs" {
		if e = scrubUsageReceipt(ctx, tx, r, job.Target.ID); e != nil {
			return e
		}
		progress.DeletedRecords["context_usage"]++
		return nil
	} else if e = tx.Delete(ctx, ref.Collection, job.Scope, ref.ID, ref.Revision); e != nil {
		return e
	}
	if ref.Collection == "memories" {
		progress.DeletedMemories++
	} else if ref.Collection == "memory_candidates" {
		progress.DeletedCandidates++
	} else {
		progress.DeletedRecords[ref.Collection]++
	}
	return nil
}

// erasureRowReferencesTarget also clears a parent skill's active version when
// the erased row is that version.
func erasureRowReferencesTarget(ctx context.Context, tx Tx, job Record, ref Ref, r Record, currentParents map[string]Record, progress *SourceMemoryErasure) (bool, error) {
	found := false
	if isMemoryErasureKind(job.Kind) {
		if ref.Collection == "evaluations" {
			found = memoryEvaluationReferences(r, job.Target.ID, job.Proposal != nil, job.Kind)
		} else if ref.Collection == "jobs" {
			found = usageReceiptReferences(r, job.Target.ID)
		} else {
			found = memoryRelationReferences(r, job.Target.ID, job.Proposal != nil, job.Kind)
			if ref.Collection == "memory_relations" {
				progress.RelationCursor = ref.ID
			}
		}
	}
	for _, src := range r.Sources {
		if job.Target.Collection == "sources" && src.SourceID == job.Target.ID {
			found = true
		}
	}
	if job.Target.Collection == "sources" && r.SourceEventID == job.Target.ID && r.ID != job.Target.ID {
		found = true
	}
	if ref.Collection == colSkillVersions {
		parent, ok := currentParents[r.SkillID]
		if !ok {
			return false, ErrConflict
		}
		for _, src := range parent.Sources {
			if job.Target.Collection == "sources" && src.SourceID == job.Target.ID {
				found = true
			}
		}
		if !found {
			return false, ErrConflict
		}
		if parent.ID != "" && parent.ActiveVersion == r.ID {
			prev := parent.Revision
			parent.ActiveVersion = ""
			parent.Revision++
			parent.UpdatedAt = time.Now().UTC()
			if e := tx.Replace(ctx, colSkills, parent, prev); e != nil {
				return false, e
			}
		}
	}
	return found, nil
}

func redactSurvivingConflict(ctx context.Context, tx Tx, job Record, ref Ref, r Record) (bool, error) {
	if ref.Collection == "memory_relations" && r.Kind == "conflicts_with" && r.Status == "active" {
		control, e := conflictControlRecord(r)
		if e != nil {
			return false, e
		}
		survivor := false
		for _, member := range control.Relations {
			_, e := tx.Get(ctx, member.Collection, job.Scope, member.ID)
			if e == nil {
				survivor = true
				break
			}
			if !errors.Is(e, ErrNotFound) {
				return false, e
			}
		}
		if survivor {
			control.Revision++
			control.UpdatedAt = time.Now().UTC()
			// Explicit erasure exception: strip provenance and all payload,
			// without weakening the immutable-provenance Replace contract.
			if e = tx.Delete(ctx, ref.Collection, job.Scope, ref.ID, ref.Revision); e != nil {
				return false, e
			}
			if e = tx.Insert(ctx, ref.Collection, control); e != nil {
				return false, e
			}
			return true, nil
		}
	}
	return false, nil
}

func enqueueErasureDependents(ctx context.Context, tx Tx, job Record, ref Ref, r Record, progress *SourceMemoryErasure) error {
	if job.Kind == sourceMemoryDependentsErasureKind && ref.Collection == "memory_candidates" {
		if e := enqueueMemoryErasureAs(ctx, tx, "memory_candidates", r.Scope, r.ID, r.Revision, job.CreatedBy, job.CreatedByType, nil, job.ID); e != nil {
			return e
		}
		progress.QueuedCandidateJobs++
	}
	if job.Kind == sourceMemoryDependentsErasureKind && ref.Collection == colMemories {
		var proposal *Ref
		if p := r.Proposal; p != nil {
			if p.Collection != "memory_candidates" || p.ID != r.ID || p.Revision < 1 {
				return ErrUnavailable
			}
			_, err := tx.Get(ctx, p.Collection, r.Scope, p.ID)
			if errors.Is(err, ErrNotFound) {
				proposal = p
			} else if err != nil {
				return err
			}
		}
		if e := enqueueMemoryErasureAs(ctx, tx, colMemories, r.Scope, r.ID, r.Revision, job.CreatedBy, job.CreatedByType, proposal, job.ID); e != nil {
			return e
		}
		progress.QueuedMemoryJobs++
	}
	return nil
}

func enqueueDerivedSourceErasure(ctx context.Context, tx Tx, job Record, r Record, progress *SourceMemoryErasure) error {
	// The DELETE below serializes with insertion fences. The ordinary
	// source-read fence would reject a quarantined source being erased.
	parentID, rootErr := derivedSourceErasureParent(ctx, tx, job)
	if rootErr != nil {
		return rootErr
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	receipt := Record{ErasureParentID: parentID, ID: sourceDeletionID(r.Scope, r.ID), Scope: r.Scope, Kind: "source_deletion", Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: "sources", ID: r.ID, Revision: r.Revision}}
	if e := insertIfAbsent(ctx, tx, colJobs, receipt); e != nil {
		return e
	}
	if old, getErr := tx.Get(ctx, colJobs, r.Scope, sourceMemoryErasureID(r.Scope, r.ID)); errors.Is(getErr, ErrNotFound) {
		if e := enqueueSourceMemoryErasureAs(ctx, tx, r.Scope, r.ID, r.Revision, job.CreatedBy, job.CreatedByType, parentID); e != nil {
			return e
		}
	} else if getErr != nil {
		return getErr
	} else if !validSourceErasureJob(old) || old.Target.Collection != "sources" || old.Target.ID != r.ID {
		return ErrConflict
	}
	childID := sourceMemoryErasureID(r.Scope, r.ID)
	if !slices.Contains(progress.DerivedSourceJobs, childID) {
		progress.DerivedSourceJobs = append(progress.DerivedSourceJobs, childID)
		progress.QueuedSourceJobs++
	}
	return nil
}

func (s *Service) SourceDeletionStatus(ctx context.Context, token string, scope Scope, id string) (SourceErasureStatus, error) {
	return s.deletionStatus(ctx, token, "sources", scope, id)
}
func (s *Service) deletionStatus(ctx context.Context, token, col string, scope Scope, id string) (SourceErasureStatus, error) {
	if !validScope(scope) || !validRunLabel(id, 256) {
		return SourceErasureStatus{}, ErrInvalid
	}
	who, e := s.authorize(ctx, token, "read:deletion", scope, id, "")
	if e != nil {
		return SourceErasureStatus{}, e
	}
	if s.Repo == nil {
		return SourceErasureStatus{}, ErrUnavailable
	}
	var out SourceErasureStatus
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		job, e := tx.Get(ctx, "jobs", scope, deletionJobID(col, scope, id))
		if e != nil {
			return e
		}
		if !validSourceErasureJob(job) {
			return ErrUnavailable
		}
		out = SourceErasureStatus{PendingArtifactCleanup: len(job.SourceMemoryErasure.PendingArtifacts), Revision: job.Revision, JobID: job.ID, Target: *job.Target, Status: job.Status, Attempts: job.Attempts, SourceMemoryErasure: *job.SourceMemoryErasure, UpdatedAt: job.UpdatedAt, Coverage: sourceErasureCoverage(job.Kind)}
		if job.Kind == sourceMemoryDependentsErasureKind || job.Kind == memoryEvaluationErasureKind || job.Kind == candidateEvaluationErasureKind {
			out.DependentCleanup, e = dependentErasureStatus(ctx, tx, job)
			if e != nil {
				return e
			}
		}
		if out.DeletedRecords == nil {
			out.DeletedRecords = map[string]int64{}
		}
		action := "read:source_deletion"
		if col == "memories" {
			action = "read:memory_deletion"
		} else if col == "memory_candidates" {
			action = "read:candidate_deletion"
		}
		return audit(ctx, tx, scope, who.PrincipalID, action, job.ID)
	})
	if e != nil {
		return SourceErasureStatus{}, e
	}
	return out, nil
}

type RetrySourceDeletionInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func (s *Service) RetrySourceDeletion(ctx context.Context, token, id string, in RetrySourceDeletionInput) (Ref, error) {
	return s.retryDeletion(ctx, token, "sources", id, in)
}
func (s *Service) retryDeletion(ctx context.Context, token, col, id string, in RetrySourceDeletionInput) (Ref, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 {
		return Ref{}, ErrInvalid
	}
	who, e := s.authorize(ctx, token, "retry:deletion", in.Scope, id, "")
	if e != nil {
		return Ref{}, e
	}
	if who.Role != "human" {
		return Ref{}, ErrDenied
	}
	if s.Repo == nil {
		return Ref{}, ErrUnavailable
	}
	var out Ref
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		job, e := tx.Get(ctx, "jobs", in.Scope, deletionJobID(col, in.Scope, id))
		if e != nil {
			return e
		}
		if !validSourceErasureJob(job) {
			return ErrUnavailable
		}
		if job.Revision != in.ExpectedRevision || job.Status != JobStatusFailed {
			return ErrConflict
		}
		if e = erasedTargetAbsent(ctx, tx, job); e != nil {
			return e
		}
		// Clearing the old backoff makes this explicit retry immediately claimable.
		job.Status = JobStatusPending
		job.RunAt = nil
		job.Attempts = 0
		job.LeaseToken = ""
		job.LeaseUntil = time.Time{}
		job.UpdatedAt = time.Now().UTC()
		job.Revision++
		if e = tx.Replace(ctx, "jobs", job, in.ExpectedRevision); e != nil {
			return e
		}
		if e = audit(ctx, tx, in.Scope, who.PrincipalID, "retry:deletion", job.ID); e != nil {
			return e
		}
		out = Ref{Collection: "jobs", ID: job.ID, Revision: job.Revision}
		return nil
	})
	if e != nil {
		return Ref{}, e
	}
	return out, nil
}

func sourceErasureCollections(kind string) []string {
	switch kind {
	case memoryEvaluationErasureKind, candidateEvaluationErasureKind:
		return []string{"evaluations", "memory_relations", colSkillVersions, "sources", "jobs"}
	case memoryRelationErasureKind:
		return []string{"memory_relations"}
	case sourceMemoryErasureKind:
		return []string{"memory_candidates", "memories", colTopicDigests, colUserDigests}
	case sourceRecordErasureKind:
		return []string{"sources", "memory_candidates", "memories", "reflections", "evaluations", "tool_calls", "agent_runs", "execution_traces", colConsultations, colHandoffs, colKnowledgeSources, colKnowledgeDocuments, colKnowledgeChunks, colKnowledgeBindings, colRetrievalLogs, colTopicDigests, colUserDigests, colEvalTasks, colEvalRuns, colEvalTrials, colJudgeCalibrations}
	case sourceMemoryDependentsErasureKind:
		return append(sourceErasureCollections(sourceRelationErasureKind), colArtifacts)
	case sourceRelationErasureKind:
		return append(sourceErasureCollections(sourceSkillErasureKind), "memory_relations")
	case sourceSkillErasureKind:
		return []string{"sources", "memory_candidates", "memories", "reflections", "evaluations", "tool_calls", "agent_runs", "execution_traces", colConsultations, colHandoffs, colKnowledgeSources, colKnowledgeDocuments, colKnowledgeChunks, colKnowledgeBindings, colRetrievalLogs, colSkillVersions, colSkills, colTopicDigests, colUserDigests, colEvalTasks, colEvalRuns, colEvalTrials, colJudgeCalibrations}
	default:
		return nil
	}
}
func sourceErasureCoverage(kind string) string {
	if kind == candidateEvaluationErasureKind {
		return "candidate_relations_evaluations"
	}
	if kind == sourceMemoryDependentsErasureKind {
		return sourceRecordsMemoryDependents
	}
	if kind == memoryEvaluationErasureKind {
		return "memory_relations_evaluations"
	}
	if kind == memoryRelationErasureKind {
		return "memory_relations"
	}
	if kind == sourceRelationErasureKind {
		return sourceRecordsSkillsRelations
	}
	if kind == sourceSkillErasureKind {
		return sourceRecordsAndSkills
	}
	if kind == sourceMemoryErasureKind {
		return "source_memories"
	}
	return sourceDirectRecords
}

func erasureLease(ctx context.Context, tx Tx, job Record) (Record, error) {
	lease, e := tx.Get(ctx, "jobs", job.Scope, job.ID)
	if e != nil {
		return Record{}, e
	}
	if !validSourceErasureJob(lease) || lease.Status != JobStatusRunning || lease.Revision != job.Revision || lease.LeaseToken != job.LeaseToken || !lease.LeaseUntil.After(time.Now()) || *lease.Target != *job.Target || lease.Kind != job.Kind || !reflect.DeepEqual(lease.Proposal, job.Proposal) || !reflect.DeepEqual(lease.SourceMemoryErasure, job.SourceMemoryErasure) {
		return Record{}, ErrConflict
	}
	if e = erasedTargetAbsent(ctx, tx, lease); e != nil {
		return Record{}, e
	}
	return lease, nil
}

// A held batch has not attempted deletion. Preserve past failure history while
// undoing only this lease's attempt, and keep other queue work eligible.
func deferHeldErasure(ctx context.Context, tx Tx, lease Record, actor, action string) error {
	previous := lease.Revision
	next := time.Now().UTC().Add(time.Minute)
	lease.Status, lease.RunAt = JobStatusPending, &next
	lease.LeaseToken, lease.LeaseUntil = "", time.Time{}
	lease.Attempts = max(0, lease.Attempts-1)
	lease.Revision++
	lease.UpdatedAt = time.Now().UTC()
	if err := tx.Replace(ctx, colJobs, lease, previous); err != nil {
		return err
	}
	return audit(ctx, tx, lease.Scope, actor, action+"_held", lease.ID)
}

// usageReceiptReferences: a context_usage receipt handed out this memory.
func usageReceiptReferences(r Record, memoryID string) bool {
	return r.Kind == "context_usage" && r.Usage != nil && usedRef(r.Usage, memoryID)
}

// scrubUsageReceipt takes the erased memory out of a receipt and deletes a receipt with nothing left.
func scrubUsageReceipt(ctx context.Context, tx Tx, r Record, memoryID string) error {
	u := *r.Usage
	u.Items = slices.DeleteFunc(slices.Clone(u.Items), func(it MemoryUse) bool { return it.Memory.ID == memoryID })
	if len(u.Items) == 0 {
		return tx.Delete(ctx, "jobs", r.Scope, r.ID, r.Revision)
	}
	prev := r.Revision
	r.Usage, r.Revision, r.UpdatedAt = &u, r.Revision+1, time.Now().UTC()
	return tx.Replace(ctx, "jobs", r, prev)
}
