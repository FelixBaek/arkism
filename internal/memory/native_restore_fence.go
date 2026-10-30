package memory

import "context"

// The entry check protects early previews. Publication/admission must also
// inspect the existing gate in the authoritative transaction snapshot, so a
// review or pause committed before that transaction cannot use an earlier
// entry-only grant. Noncanonical repositories have no restored database.
func nativeCheckRestorePublicationTx(ctx context.Context, tx Tx) error {
	if store, ok := tx.(restoreReviewStore); ok {
		return store.CheckRestorePublication(ctx)
	}
	return nil
}
