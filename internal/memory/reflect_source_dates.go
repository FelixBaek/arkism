package memory

import (
	"context"
	"time"
	"unicode/utf8"
)

// Source metadata is evidence too: only an exact original-message reference may
// supply a missing observation date, and every contributing source is fenced.
func (s *Service) reflectSourceAnchors(ctx context.Context, token string, run *reflectRun) {
	for i := range run.hits {
		h := &run.hits[i]
		if h.CollectedAt != nil || len(h.Sources) == 0 || len(h.Sources) > 10 {
			continue
		}
		at, records := s.reflectSourceAnchor(ctx, token, run.in, h.Sources)
		if at == nil {
			continue
		}
		h.CollectedAt = at
		if run.sourceRecords == nil {
			run.sourceRecords = map[string]reflectSourceSnapshot{}
		}
		for _, r := range records {
			old, exists := run.sourceRecords[r.ID]
			if exists {
				old.transfer = old.transfer || !run.in.PromptOnly
				run.sourceRecords[r.ID] = old
				continue
			}
			run.sourceRecords[r.ID] = reflectSourceSnapshot{record: r, transfer: !run.in.PromptOnly, dateOnly: true}
		}
	}
}

func (s *Service) reflectSourceAnchor(ctx context.Context, token string, in ReflectInput, refs []SourceRef) (*time.Time, []Record) {
	var at *time.Time
	var records []Record
	for _, ref := range refs {
		if ref.Locator != "original" || ref.Span == nil {
			return nil, nil
		}
		r, err := s.Repo.Get(ctx, "sources", in.Scope, ref.SourceID)
		if err != nil || r.CollectedAt == nil || r.CollectedAt.IsZero() || !s.reflectSourceReadableFor(ctx, token, in, r, !in.PromptOnly, true) {
			return nil, nil
		}
		if validateSourceSpan(ref.Span, ContentHash(r.Text), utf8.RuneCountInString(r.Text)) != nil {
			return nil, nil
		}
		if at != nil && !at.Equal(*r.CollectedAt) {
			return nil, nil
		}
		anchor := *r.CollectedAt
		at = &anchor
		records = append(records, r)
	}
	return at, records
}
