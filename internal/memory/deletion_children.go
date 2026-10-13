package memory

import (
	"context"
	"errors"
	"math"
	"slices"
)

type DependentErasureStatus struct {
	ExpectedJobs     int64            `json:"expected_jobs"`
	ObservedJobs     int64            `json:"observed_jobs"`
	Statuses         map[string]int64 `json:"statuses"`
	TrackingComplete bool             `json:"tracking_complete"`
	Complete         bool             `json:"complete"`
}

// Each edge is recorded with its deletion. Shared downstream jobs count once;
// the privacy authorization parent is independent of these tracking edges.
func dependentErasureStatus(ctx context.Context, tx Tx, job Record) (*DependentErasureStatus, error) {
	states := []string{JobStatusPending, JobStatusRetry, JobStatusRunning, JobStatusDone, JobStatusFailed}
	out := &DependentErasureStatus{Statuses: map[string]int64{}, TrackingComplete: true}
	for _, state := range states {
		out.Statuses[state] = 0
	}
	seen := map[string]bool{job.ID: true}
	visiting := map[string]bool{}
	var visit func(Record) error
	visit = func(parent Record) error {
		if visiting[parent.ID] {
			return ErrUnavailable
		}
		visiting[parent.ID] = true
		defer delete(visiting, parent.ID)
		p := parent.SourceMemoryErasure
		if p == nil {
			return ErrUnavailable
		}
		expected := int64(0)
		for _, n := range []int64{p.QueuedMemoryJobs, p.QueuedCandidateJobs, p.QueuedSourceJobs} {
			if n < 0 || n > math.MaxInt64-expected {
				return ErrUnavailable
			}
			expected += n
		}
		if expected > math.MaxInt64-out.ExpectedJobs {
			return ErrUnavailable
		}
		out.ExpectedJobs += expected
		if p.QueuedSourceJobs != int64(len(p.DerivedSourceJobs)) || p.QueuedSourceJobs != p.DeletedRecords["sources"] {
			out.TrackingComplete = false
		}
		if parent.Kind == sourceMemoryDependentsErasureKind && (p.QueuedMemoryJobs != p.DeletedMemories || p.QueuedCandidateJobs != p.DeletedCandidates) {
			out.TrackingComplete = false
		}
		children, err := tx.ErasureChildren(ctx, parent.Scope, parent.ID)
		if err != nil {
			return err
		}
		if len(children) > 4096 || len(p.DerivedSourceJobs) > 4096 {
			return ErrUnavailable
		}
		local := map[string]bool{}
		for _, child := range children {
			local[child.ID] = true
		}
		for _, id := range p.DerivedSourceJobs {
			if !validRunLabel(id, 256) || local[id] {
				return ErrUnavailable
			}
			local[id] = true
			child, e := tx.Get(ctx, colJobs, parent.Scope, id)
			if errors.Is(e, ErrNotFound) {
				out.TrackingComplete = false
				continue
			}
			if e != nil {
				return e
			}
			if child.Kind != sourceMemoryDependentsErasureKind {
				return ErrUnavailable
			}
			children = append(children, child)
		}
		if int64(len(children)) > expected {
			return ErrUnavailable
		}
		counts := map[string]int64{}
		for _, child := range children {
			counts[child.Kind]++
		}
		if int64(len(children)) != expected || counts[memoryEvaluationErasureKind] != p.QueuedMemoryJobs || counts[candidateEvaluationErasureKind] != p.QueuedCandidateJobs || counts[sourceMemoryDependentsErasureKind] != p.QueuedSourceJobs {
			out.TrackingComplete = false
		}
		for _, child := range children {
			if !sameScope(parent.Scope, child.Scope) || !validSourceErasureJob(child) || !slices.Contains(states, child.Status) || visiting[child.ID] {
				return ErrUnavailable
			}
			if seen[child.ID] {
				out.ExpectedJobs--
				continue
			}
			if len(seen) >= 4097 {
				return ErrUnavailable
			}
			seen[child.ID] = true
			out.ObservedJobs++
			out.Statuses[child.Status]++
			if err = visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(job); err != nil {
		return nil, err
	}
	out.TrackingComplete = out.TrackingComplete && out.ExpectedJobs == out.ObservedJobs
	out.Complete = job.Status == JobStatusDone && out.TrackingComplete && out.Statuses[JobStatusDone] == out.ExpectedJobs
	return out, nil
}
