package memory

import (
	"context"
	"time"
)

// A content/lifecycle replacement and its old-ref fence share the original CAS
// transaction. A pending candidate does not replace the canonical memory.
func nativeInvalidateReplacedRecordTx(ctx context.Context, tx Tx, collection string, old, current Record) error {
	if old.Revision == current.Revision {
		return nil
	}
	switch collection {
	case colMemories, "sources", "evaluations", "agent_runs", "execution_traces":
		reason := "dependency_revision_changed"
		if collection == colMemories && nativeMetadataOnlyRevision(old, current) {
			reason = "dependency_metadata_revision_changed"
		}
		return invalidateNativeDynamicsDependencyTx(ctx, tx, old.Scope, Ref{Collection: collection, ID: old.ID, Revision: old.Revision}, reason)
	default:
		return nil
	}
}

// General Eval references have their own lifecycle contract. The native v1
// admission profile must not silently omit them or acquire new qualification.
func nativeLifecycleProofSupported(record Record, collection string) bool {
	return len(evaluationRefs(record.Evaluation)) == 0 && !isEvalRecordCollection(collection)
}

// This classification is observational only: metadata changes still install the
// exact old-reference fence and purge intent. It never rebinds accepted evidence.
func nativeMetadataOnlyRevision(old, current Record) bool {
	clean := func(r Record) Record {
		r.Revision = 0
		r.UpdatedAt = time.Time{}
		r.ReviewDueAt = nil
		r.MemoryReferenceFence = 0
		r.MemoryContext = copyMemoryContext(r.MemoryContext)
		if r.MemoryContext != nil {
			r.MemoryContext.ObservedAt = nil
		}
		return r
	}
	return structuredText(clean(old)) == structuredText(clean(current))
}
