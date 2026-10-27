package memory

import "context"

// A linked meeting exposes the pinned team and member instructions of its
// parent. Authorize those sources before returning or changing that snapshot.
func (s *Service) authorizeLinkedMeetingSources(ctx context.Context, token string, r Record, reader flowRecordReader) error {
	if r.CrewMeeting == nil || r.ContentErased || r.PrivacyQuarantined {
		return ErrNotFound
	}
	if r.CrewMeeting.ParentID == "" {
		return nil
	}
	parent, err := reader.Get(ctx, colCrewRuns, r.Scope, r.CrewMeeting.ParentID)
	if err != nil {
		return err
	}
	if !readableCrewRun(parent) || parent.CrewRun.InvocationMode != "flow" || !linkedMeetingBelongs(parent, r) {
		return ErrNotFound
	}
	return s.authorizeFlowSourcesFrom(ctx, token, parent, reader)
}

func linkedMeetingBelongs(parent, meeting Record) bool {
	for _, ref := range parent.CrewRun.Children {
		if ref.ID == meeting.ID && flowChildBelongs(parent, ref, meeting) {
			return true
		}
	}
	return false
}

// A writer may request cancellation after source access is revoked. Keep the
// acknowledgement useful to existing clients without returning stored input,
// output, definitions, child references, or a fabricated terminal status.
func (s *Service) flowStopResponse(ctx context.Context, token string, r Record, reader flowRecordReader) Record {
	if s.authorizeFlowSourcesFrom(ctx, token, r, reader) == nil {
		return r
	}
	return Record{ID: r.ID, Scope: r.Scope, Revision: r.Revision, CrewRun: &CrewRun{
		Status: r.CrewRun.Status, CancelRequested: r.CrewRun.CancelRequested, Nodes: map[string]CrewRunNode{},
	}}
}
