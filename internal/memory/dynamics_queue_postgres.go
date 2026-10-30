package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

type dynamicsLoadQueries interface {
	DynamicsLoadLock(context.Context, Scope, string) (Record, error)
	DynamicsLoadLedgerBytes(context.Context, Scope) (int64, error)
	BlobStorageIdentity(context.Context) (string, error)
}

func dynamicsCoordinatorID(scope Scope) string {
	return "sys05_coordinator_" + ContentHash(structuredText(scope))
}
func dynamicsEntryID(scope Scope, ordinal int64) string {
	return "sys05_entry_" + ContentHash(structuredText(scope)) + fmt.Sprintf("_%020d", ordinal)
}
func (p *Postgres) DynamicsLoadLock(ctx context.Context, scope Scope, id string) (Record, error) {
	if p.tx == nil {
		return Record{}, ErrInvalid
	}
	return p.row(ctx, colJobs, scope, id, true)
}
func (p *Postgres) DynamicsLoadLedgerBytes(ctx context.Context, scope Scope) (int64, error) {
	if p.tx == nil || !validScope(scope) {
		return 0, ErrInvalid
	}
	var size int64
	err := p.q().QueryRow(ctx, `SELECT COALESCE(SUM(octet_length(payload)),0) FROM memory_records WHERE collection='jobs' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind=ANY($5) AND status IN ('done','failed')`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, []string{dynamicsLoadPrefix + "entry", dynamicsLoadPrefix + "coordinator", dynamicsLoadPrefix + "recovery"}).Scan(&size)
	return size, pgErr(err)
}
func dynamicsRow(scope Scope, role, id string, p DynamicsLoadPayload) Record {
	p.Protocol = dynamicsLoadProtocol
	p.Role = role
	now := time.Now().UTC().Truncate(time.Millisecond)
	return Record{ID: id, Scope: scope, Kind: dynamicsLoadPrefix + role, Status: JobStatusPending, Revision: 1, DynamicsLoad: &p, CreatedAt: now, UpdatedAt: now}
}
func dynamicsValidateRow(row Record, scope Scope, role string) error {
	if row.Scope != scope || row.Kind != dynamicsLoadPrefix+role || row.DynamicsLoad == nil || row.DynamicsLoad.Protocol != dynamicsLoadProtocol || row.DynamicsLoad.Role != role {
		return ErrConflict
	}
	return nil
}
func dynamicsReplace(ctx context.Context, tx Tx, row Record) error {
	if row.Revision == math.MaxInt64 {
		return ErrConflict
	}
	rev := row.Revision
	row.Revision++
	row.UpdatedAt = time.Now().UTC()
	return tx.Replace(ctx, colJobs, row, rev)
}
func dynamicsLockCoordinator(ctx context.Context, tx Tx, scope Scope) (Record, error) {
	if q, ok := tx.(dynamicsLoadQueries); ok {
		row, err := q.DynamicsLoadLock(ctx, scope, dynamicsCoordinatorID(scope))
		if err != nil {
			return row, err
		}
		return row, dynamicsValidateRow(row, scope, "coordinator")
	}
	// Legacy fixture repositories still support the known-ID absent check.
	row, err := tx.Get(ctx, colJobs, scope, dynamicsCoordinatorID(scope))
	if err != nil {
		return row, err
	}
	return row, ErrUnavailable
}
func (s *Service) dynamicsBinding(ctx context.Context, tx Tx, scope Scope, row Record) error {
	r := s.DynamicsLoad
	if !dynamicsCoordinatorCounters(row.DynamicsLoad) {
		return ErrConflict
	}
	if !r.configured(scope) || row.DynamicsLoad.ConfigSHA256 != r.hash {
		return ErrDynamicsRuntimeBinding
	}
	q, ok := tx.(dynamicsLoadQueries)
	if !ok {
		return ErrUnavailable
	}
	identity, err := q.BlobStorageIdentity(ctx)
	if err != nil {
		return err
	}
	if identity != row.DynamicsLoad.StorageIdentity {
		return ErrDynamicsRuntimeBinding
	}
	return nil
}

// Reservation is after canonical idempotence, before insertion, in the same Tx.
// A durable binding is checked even when the restarting process has no runtime.
func (s *Service) reserveDynamicsOutcomeTx(ctx context.Context, tx Tx, in MemoryOutcomeInput, evaluation Ref, stamp *NativeOutcomeCaptureStamp) (*Record, error) {
	if s.DynamicsLoad != nil && !s.DynamicsLoad.ready {
		return nil, ErrInvalid
	}
	if dynamicsLegacyScopeTx(s.DynamicsLoad, tx, in.Scope) {
		return nil, nil
	}
	coordinator, err := dynamicsLockCoordinator(ctx, tx, in.Scope)
	if errors.Is(err, ErrNotFound) {
		if !s.DynamicsLoad.configured(in.Scope) {
			return nil, nil
		}
		if stamp == nil || stamp.Mode != "enrolled" {
			return nil, nil
		}
		coordinator, err = s.createDynamicsCoordinator(ctx, tx, in.Scope)
	}
	if err != nil {
		return nil, err
	}
	if err = s.dynamicsBinding(ctx, tx, in.Scope, coordinator); err != nil {
		return nil, err
	}
	if stamp == nil || stamp.Mode != "enrolled" {
		return nil, ErrDynamicsRuntimeBinding
	}
	p := coordinator.DynamicsLoad
	if p.Withheld {
		return nil, ErrDynamicsRuntimeBinding
	}
	l := s.DynamicsLoad.config.Limits
	if p.Tail == math.MaxInt64 || p.Count >= l.QueueCountLimit {
		return nil, ErrDynamicsQueueFull
	}
	entry := dynamicsRow(in.Scope, "entry", dynamicsEntryID(in.Scope, p.Tail+1), DynamicsLoadPayload{ConfigSHA256: p.ConfigSHA256, StorageIdentity: p.StorageIdentity, Ordinal: p.Tail + 1, Capture: Ref{Collection: colJobs, ID: nativeCaptureID(in.Scope, evaluation), Revision: 1}, AcceptedNS: dynamicsNowNS()})
	// Reserve a storage-codec upper bound for this entry's entire lifecycle,
	// including lease tokens and terminal timestamps. ActiveBytes is reserved
	// capacity, while observation reports actual retained terminal payload bytes.
	if e := dynamicsReserveEntryBytes(&entry, l); e != nil {
		return nil, e
	}

	size := entry.DynamicsLoad.EntryBytes
	if err = dynamicsAdmissionCapacityTx(ctx, tx, in.Scope, p, l, size); err != nil {
		return nil, err
	}
	p.Tail++
	p.Count++
	p.ActiveBytes += size
	if err = dynamicsReplace(ctx, tx, coordinator); err != nil {
		return nil, err
	}
	return &entry, nil
}
func (s *Service) createDynamicsCoordinator(ctx context.Context, tx Tx, scope Scope) (Record, error) {
	q, ok := tx.(nativeDynamicsTxQueries)
	if !ok {
		return Record{}, ErrUnavailable
	}
	pending, err := q.NativeDynamicsPendingCaptures(ctx, scope, 1)
	if err != nil {
		return Record{}, err
	}
	if len(pending) > 0 {
		return Record{}, ErrDynamicsUnsequenced
	}
	own, ok := tx.(dynamicsLoadQueries)
	if !ok {
		return Record{}, ErrUnavailable
	}
	identity, err := own.BlobStorageIdentity(ctx)
	if err != nil {
		return Record{}, err
	}
	row := dynamicsRow(scope, "coordinator", dynamicsCoordinatorID(scope), DynamicsLoadPayload{ConfigSHA256: s.DynamicsLoad.hash, StorageIdentity: identity})
	if err = tx.Insert(ctx, colJobs, row); err != nil {
		return Record{}, err
	}
	return row, nil
}
func finalizeDynamicsOutcomeTx(ctx context.Context, tx Tx, entry *Record) error {
	if entry == nil {
		return nil
	}
	capture, err := nativeGetTx(ctx, tx, entry.Scope, entry.DynamicsLoad.Capture.ID, "capture")
	if err != nil {
		return err
	}
	if nativeRef(capture) != entry.DynamicsLoad.Capture {
		return ErrConflict
	}
	return tx.Insert(ctx, colJobs, *entry)
}

func (s *Service) claimDynamics(ctx context.Context, scope Scope) (Record, error) {
	var claim Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		claim, e = s.claimDynamicsTx(ctx, tx, scope)
		return e
	})
	if err == nil && claim.DynamicsLoad == nil {
		return Record{}, ErrDynamicsRuntimeBinding
	}
	return claim, err
}
func (s *Service) dynamicsClaimFence(ctx context.Context, tx Tx, scope Scope, claim Record) (Record, Record, error) {
	c, err := dynamicsLockCoordinator(ctx, tx, scope)
	if err != nil {
		return Record{}, Record{}, err
	}
	if err = s.dynamicsBinding(ctx, tx, scope, c); err != nil {
		return c, Record{}, err
	}
	row, err := tx.(dynamicsLoadQueries).DynamicsLoadLock(ctx, scope, claim.ID)
	if err != nil {
		return c, row, err
	}
	if err = dynamicsValidateRow(row, scope, "entry"); err != nil {
		return c, row, err
	}
	p, ep, expected := c.DynamicsLoad, row.DynamicsLoad, claim.DynamicsLoad
	if expected == nil || row.Revision != claim.Revision || ep.Ordinal != p.Head+1 || ep.LeaseToken != expected.LeaseToken || ep.LeaseGeneration != expected.LeaseGeneration || p.LeaseToken != expected.LeaseToken || p.LeaseGeneration != expected.LeaseGeneration || p.LeaseUntilNS <= dynamicsNowNS() {
		return c, row, ErrConflict
	}
	return c, row, nil
}

func dynamicsReserveEntryBytes(entry *Record, l DynamicsLoadLimits) error {
	worst := *entry
	payload := *entry.DynamicsLoad
	worst.DynamicsLoad = &payload
	worst.Revision = math.MaxInt64
	worst.Status = JobStatusRunning
	payload.LeaseGeneration = math.MaxInt64
	payload.LeaseUntilNS = math.MaxInt64
	payload.StartedNS = math.MaxInt64
	payload.AppliedNS = math.MaxInt64
	payload.DueNS = math.MaxInt64
	payload.Attempts = l.RetryLimit
	payload.LeaseToken = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	payload.Reason = "native_admission_conflict"
	for {
		encoded, err := storeMarshal(worst)
		if err != nil {
			return err
		}
		size := int64(len(encoded))
		if payload.EntryBytes == size {
			entry.DynamicsLoad.EntryBytes = size
			return nil
		}
		payload.EntryBytes = size
	}
}

func dynamicsAdmissionCapacityTx(ctx context.Context, tx Tx, scope Scope, p *DynamicsLoadPayload, l DynamicsLoadLimits, size int64) error {
	if size > l.QueueActiveBytesLimit-p.ActiveBytes {
		return ErrDynamicsQueueFull
	}
	total, err := tx.(dynamicsLoadQueries).DynamicsLoadLedgerBytes(ctx, scope)
	if err != nil {
		return err
	}
	if size > l.TerminalLedgerRetentionBudget-total-p.ActiveBytes {
		return ErrDynamicsQueueFull
	}
	return nil
}

func (s *Service) claimDynamicsTx(ctx context.Context, tx Tx, scope Scope) (Record, error) {
	c, err := dynamicsLockCoordinator(ctx, tx, scope)
	if err != nil {
		return Record{}, err
	}
	if err = s.dynamicsBinding(ctx, tx, scope, c); err != nil {
		return Record{}, err
	}
	if err = nativeCheckRestorePublicationTx(ctx, tx); err != nil {
		return Record{}, err
	}
	p := c.DynamicsLoad
	now := dynamicsNowNS()
	if p.Withheld {
		return Record{}, ErrDynamicsRuntimeBinding
	}
	if p.Count == 0 {
		return Record{}, ErrNotFound
	}
	if p.LeaseUntilNS > now {
		return Record{}, ErrNotFound
	}
	entry, err := tx.(dynamicsLoadQueries).DynamicsLoadLock(ctx, scope, dynamicsEntryID(scope, p.Head+1))
	if err != nil {
		return Record{}, err
	}
	if err = dynamicsValidateRow(entry, scope, "entry"); err != nil {
		return Record{}, err
	}
	ep := entry.DynamicsLoad
	if ep.Ordinal != p.Head+1 || ep.ConfigSHA256 != p.ConfigSHA256 {
		return Record{}, ErrConflict
	}
	if ep.DueNS > now {
		return Record{}, ErrNotFound
	}
	if now < ep.AcceptedNS {
		return Record{}, ErrConflict
	}
	if ep.Attempts >= s.DynamicsLoad.config.Limits.RetryLimit || now-ep.AcceptedNS > s.DynamicsLoad.config.Limits.MaxWaitNS {
		if err = finishDynamicsTx(ctx, tx, c, entry, false, "queue_wait_exhausted"); err != nil {
			return Record{}, err
		}
		return Record{}, nil
	}
	return dynamicsLeaseEntryTx(ctx, tx, c, entry, now, s.DynamicsLoad.config.Limits.LeaseNS)

}

func dynamicsLeaseEntryTx(ctx context.Context, tx Tx, c, entry Record, now, leaseNS int64) (Record, error) {
	p, ep := c.DynamicsLoad, entry.DynamicsLoad
	var err error
	if p.LeaseGeneration == math.MaxInt64 {
		return Record{}, ErrConflict
	}
	token, err := newLeaseToken()
	if err != nil {
		return Record{}, err
	}
	p.LeaseGeneration++
	p.LeaseToken = token
	p.LeaseUntilNS = now + leaseNS
	ep.LeaseGeneration = p.LeaseGeneration
	ep.LeaseToken = token
	ep.LeaseUntilNS = p.LeaseUntilNS
	ep.StartedNS = now
	ep.Attempts++
	entry.Status = JobStatusRunning
	if err = dynamicsReplace(ctx, tx, entry); err != nil {
		return Record{}, err
	}
	entry.Revision++
	if err = dynamicsReplace(ctx, tx, c); err != nil {
		return Record{}, err
	}
	return entry, nil
}

func dynamicsCoordinatorCounters(p *DynamicsLoadPayload) bool {
	if p == nil || p.Head < 0 || p.Tail < p.Head || p.Count != p.Tail-p.Head || p.Count > 500 || p.ActiveBytes < 0 {
		return false
	}
	if p.Count == 0 && p.ActiveBytes != 0 {
		return false
	}
	if p.LeaseGeneration < 0 || p.PublicationGeneration < 0 || p.AsOfNS < 0 || p.PublishedNS < 0 {
		return false
	}
	return true
}

func dynamicsLegacyScopeTx(runtime *DynamicsLoadRuntime, tx Tx, scope Scope) bool {
	if runtime.configured(scope) {
		return false
	}
	_, canonical := tx.(dynamicsLoadQueries)
	return !canonical
}
