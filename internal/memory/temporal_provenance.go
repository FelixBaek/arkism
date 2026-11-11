package memory

import "time"

// recordObservationAnchor preserves an explicit source collection or observation
// instant through retrieval. Creation/effective dates are not substitutes: an
// imported conversation may have been observed years before database ingestion.
func recordObservationAnchor(r Record) *time.Time {
	at := r.CollectedAt
	if (at == nil || at.IsZero()) && r.MemoryContext != nil {
		at = r.MemoryContext.ObservedAt
	}
	if at == nil || at.IsZero() {
		return nil
	}
	copy := *at
	return &copy
}
