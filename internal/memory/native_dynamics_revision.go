package memory

import "context"

// A content/lifecycle replacement and its old-ref fence share the original CAS
// transaction. A pending candidate does not replace the canonical memory.
func nativeInvalidateReplacedRecordTx(ctx context.Context, tx Tx, collection string, old, current Record) error {
	if old.Revision == current.Revision {
		return nil
	}
	switch collection {
	case colMemories, "sources", "evaluations", "agent_runs", "execution_traces":
		return invalidateNativeDynamicsDependencyTx(ctx, tx, old.Scope, Ref{Collection: collection, ID: old.ID, Revision: old.Revision}, "dependency_revision_changed")
	default:
		return nil
	}
}

// General Eval references have their own lifecycle contract. The native v1
// admission profile must not silently omit them or acquire new qualification.
func nativeLifecycleProofSupported(record Record, collection string) bool {
	return len(evaluationRefs(record.Evaluation)) == 0 && !isEvalRecordCollection(collection)
}
