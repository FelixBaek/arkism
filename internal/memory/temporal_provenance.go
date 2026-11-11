package memory

import "time"

// recordObservationAnchor preserves an explicit source collection or observation
// instant through retrieval. Creation/effective dates are not substitutes: an
// imported conversation may have been observed years before database ingestion.
func recordObservationAnchor(r Record) *time.Time {
	at := r.CollectedAt
	if (at == nil || at.IsZero()) && r.MemoryContext != nil && originalObservationRefs(r.Sources) {
		at = r.MemoryContext.ObservedAt
	}
	if at == nil || at.IsZero() {
		return nil
	}
	anchor := *at
	return &anchor
}

// A file locator describes evidence read later, not the original utterance time.
func originalObservationRefs(refs []SourceRef) bool {
	for _, ref := range refs {
		if ref.Locator != "" && ref.Locator != "original" {
			return false
		}
	}
	return true
}
