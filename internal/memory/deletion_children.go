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
		expected, ok := erasureExpectedJobs(p)
		if !ok || expected > math.MaxInt64-out.ExpectedJobs {
			return ErrUnavailable
		}
		out.ExpectedJobs += expected
		if !erasureEdgeTracked(parent.Kind, p) {
			out.TrackingComplete = false
		}
		children, err := tx.ErasureChildren(ctx, parent.Scope, parent.ID)
		if err != nil {
			return err
		}
		children, missing, err := derivedErasureChildren(ctx, tx, parent.Scope, p, children)
		if err != nil {
			return err
		}
		if missing {
			out.TrackingComplete = false
		}
		if int64(len(children)) > expected {
			return ErrUnavailable
		}
		if !erasureChildCountsMatch(children, expected, p) {
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

// erasureExpectedJobs sums the queued job counts, rejecting negative or
// overflowing totals.
func erasureExpectedJobs(p *SourceMemoryErasure) (int64, bool) {
	expected := int64(0)
	for _, n := range []int64{p.QueuedMemoryJobs, p.QueuedCandidateJobs, p.QueuedSourceJobs} {
		if n < 0 || n > math.MaxInt64-expected {
			return 0, false
		}
		expected += n
	}
	return expected, true
}
func erasureEdgeTracked(kind string, p *SourceMemoryErasure) bool {
	if p.QueuedSourceJobs != int64(len(p.DerivedSourceJobs)) || p.QueuedSourceJobs != p.DeletedRecords["sources"] {
		return false
	}
	return kind != sourceMemoryDependentsErasureKind || p.QueuedMemoryJobs == p.DeletedMemories && p.QueuedCandidateJobs == p.DeletedCandidates
}

// derivedErasureChildren appends the recorded derived source jobs to children
// and reports whether any of them is missing.
func derivedErasureChildren(ctx context.Context, tx Tx, scope Scope, p *SourceMemoryErasure, children []Record) ([]Record, bool, error) {
	if len(children) > 4096 || len(p.DerivedSourceJobs) > 4096 {
		return nil, false, ErrUnavailable
	}
	local := map[string]bool{}
	for _, child := range children {
		local[child.ID] = true
	}
	missing := false
	for _, id := range p.DerivedSourceJobs {
		if !validRunLabel(id, 256) || local[id] {
			return nil, false, ErrUnavailable
		}
		local[id] = true
		child, e := tx.Get(ctx, colJobs, scope, id)
		if errors.Is(e, ErrNotFound) {
			missing = true
			continue
		}
		if e != nil {
			return nil, false, e
		}
		if child.Kind != sourceMemoryDependentsErasureKind {
			return nil, false, ErrUnavailable
		}
		children = append(children, child)
	}
	return children, missing, nil
}
func erasureChildCountsMatch(children []Record, expected int64, p *SourceMemoryErasure) bool {
	counts := map[string]int64{}
	for _, child := range children {
		counts[child.Kind]++
	}
	return int64(len(children)) == expected && counts[memoryEvaluationErasureKind] == p.QueuedMemoryJobs && counts[candidateEvaluationErasureKind] == p.QueuedCandidateJobs && counts[sourceMemoryDependentsErasureKind] == p.QueuedSourceJobs
}
