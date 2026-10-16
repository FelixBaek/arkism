package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	privacyDeletionKind      = "privacy_deletion"
	privacyImpactLimit       = 1000
	privacyRuntimeLease      = 2 * time.Minute
	retentionExternal        = "external_backup_default_v1"
	retentionSynthetic       = "synthetic_disposable_v1"
	privacyHoldNone          = "none"
	privacyHoldHeld          = "held"
	privacyHoldReleased      = "released"
	privacyRequested         = "requested"
	privacyAuthorized        = "authorized"
	privacyQuarantined       = "quarantined"
	privacyDiscoveringImpact = "discovering_impact"
	privacyPurgingRuntime    = "purging_runtime"
	privacyPurgingDerived    = "purging_derived"
	privacyWaiting           = "waiting_for_retention_or_backup"
	privacyHeld              = "held"
	privacyVerifying         = "verifying"
	privacyCompleted         = "completed"
	privacyPartial           = "partial"
	privacyFailed            = "failed"
	privacyCancelled         = "cancelled"
	// JobStatusWaiting parks a privacy job at waiting_for_retention_or_backup:
	// no worker action is due until backup expiry evidence arrives.
	JobStatusWaiting = "waiting"
)

// PrivacyDeletion is the durable saga payload stored in the existing jobs
// outbox. Record.Status remains the queue/lease state; State is the privacy
// lifecycle and the two must never be conflated.
type PrivacyDeletion struct {
	State              string                 `json:"state" bson:"state"`
	RequesterType      string                 `json:"requester_type" bson:"requester_type"`
	RequesterID        string                 `json:"requester_id" bson:"requester_id"`
	TargetType         string                 `json:"target_type" bson:"target_type"`
	TargetIDs          []string               `json:"target_ids" bson:"target_ids"`
	Reason             string                 `json:"reason" bson:"reason"`
	LegalHoldStatus    string                 `json:"legal_hold_status" bson:"legal_hold_status"`
	LegalHoldPolicyRef string                 `json:"legal_hold_policy_ref,omitempty" bson:"legal_hold_policy_ref,omitempty"`
	RetentionPolicyRef string                 `json:"retention_policy_ref" bson:"retention_policy_ref"`
	RequestedAt        time.Time              `json:"requested_at" bson:"requested_at"`
	AuthorizedAt       *time.Time             `json:"authorized_at,omitempty" bson:"authorized_at,omitempty"`
	CompletedAt        *time.Time             `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
	ErrorSummary       string                 `json:"error_summary,omitempty" bson:"error_summary,omitempty"`
	IdempotencyKey     string                 `json:"idempotency_key" bson:"idempotency_key"`
	CorrelationID      string                 `json:"correlation_id" bson:"correlation_id"`
	PurgeReportRef     string                 `json:"purge_report_ref,omitempty" bson:"purge_report_ref,omitempty"`
	RequestHash        string                 `json:"request_hash" bson:"request_hash"`
	Impact             PrivacyImpact          `json:"impact" bson:"impact"`
	RuntimeReceipt     *PrivacyRuntimeReceipt `json:"runtime_receipt,omitempty" bson:"runtime_receipt,omitempty"`
	RuntimeLeaseHash   string                 `json:"-" bson:"runtime_lease_hash,omitempty"`
	RuntimeLeaseRev    int64                  `json:"-" bson:"runtime_lease_revision,omitempty"`
	Report             PrivacyPurgeReport     `json:"report" bson:"report"`
}

type PrivacyImpact struct {
	SourceEventIDs []string `json:"source_event_ids" bson:"source_event_ids"`
	TraceIDs       []string `json:"trace_ids" bson:"trace_ids"`
	ThreadIDs      []string `json:"thread_ids" bson:"thread_ids"`
	ExpectedRunIDs []string `json:"expected_run_ids" bson:"expected_run_ids"`
	Records        []Ref    `json:"records" bson:"records"`
}

type PrivacyRuntimeCounts struct {
	Runs        int `json:"runs" bson:"runs"`
	Checkpoints int `json:"checkpoints" bson:"checkpoints"`
	Writes      int `json:"writes" bson:"writes"`
	Blobs       int `json:"blobs" bson:"blobs"`
	Journals    int `json:"journals" bson:"journals"`
}

type PrivacyRuntimeReceipt struct {
	RequestHash    string               `json:"request_hash" bson:"request_hash"`
	Quarantined    bool                 `json:"quarantined" bson:"quarantined"`
	Verified       bool                 `json:"verified" bson:"verified"`
	ExpectedRunIDs []string             `json:"expected_run_ids" bson:"expected_run_ids"`
	MatchedRunIDs  []string             `json:"matched_run_ids" bson:"matched_run_ids"`
	AffectedRunIDs []string             `json:"affected_run_ids" bson:"affected_run_ids"`
	AbsentRunIDs   []string             `json:"absent_run_ids" bson:"absent_run_ids"`
	Counts         PrivacyRuntimeCounts `json:"counts" bson:"counts"`
	Held           bool                 `json:"held" bson:"held"`
	Errors         []string             `json:"errors" bson:"errors"`
}

type PrivacyPurgeReport struct {
	LiveAccessRemoved   bool `json:"live_access_removed" bson:"live_access_removed"`
	RuntimePurged       bool `json:"runtime_purged" bson:"runtime_purged"`
	DerivedPurged       bool `json:"derived_purged" bson:"derived_purged"`
	ArtifactsPurged     bool `json:"artifacts_purged" bson:"artifacts_purged"`
	BackupExpiryPending bool `json:"backup_expiry_pending" bson:"backup_expiry_pending"`
	// BackupOldestRetainedAt is the oldest backup still kept when expiry was
	// accepted; every retained backup was taken after the local purge.
	BackupOldestRetainedAt *time.Time `json:"backup_oldest_retained_at,omitempty" bson:"backup_oldest_retained_at,omitempty"`
}

type PrivacyDeletionRequest struct {
	Scope              Scope    `json:"scope"`
	IdempotencyKey     string   `json:"idempotency_key"`
	TargetType         string   `json:"target_type"`
	TargetIDs          []string `json:"target_ids"`
	Reason             string   `json:"reason"`
	RetentionPolicyRef string   `json:"retention_policy_ref"`
	LegalHoldStatus    string   `json:"legal_hold_status,omitempty"`
	ExpectedRevision   int64    `json:"expected_revision,omitempty"`
}

type PrivacyDeletionStatus struct {
	ID              string          `json:"id"`
	JobStatus       string          `json:"job_status"`
	Revision        int64           `json:"revision"`
	PrivacyDeletion PrivacyDeletion `json:"privacy_deletion"`
}

type PrivacyScopeInput struct {
	Scope Scope `json:"scope"`
}
type PrivacyRevisionInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
	// BackupOldestRetainedAt is required only to leave waiting_for_retention_or_backup.
	BackupOldestRetainedAt *time.Time `json:"backup_oldest_retained_at,omitempty"`
}
type PrivacyHoldInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
	PolicyRef        string `json:"policy_ref"`
}
type PrivacyRuntimeReceiptInput struct {
	Scope            Scope                 `json:"scope"`
	ExpectedRevision int64                 `json:"expected_revision"`
	LeaseToken       string                `json:"lease_token"`
	Receipt          PrivacyRuntimeReceipt `json:"receipt"`
}
type PrivacyRuntimeAction struct {
	DeletionRequestID string   `json:"deletion_request_id"`
	Revision          int64    `json:"revision"`
	LeaseToken        string   `json:"lease_token"`
	RequestHash       string   `json:"request_hash"`
	Scope             Scope    `json:"scope"`
	SourceEventIDs    []string `json:"source_event_ids"`
	TraceIDs          []string `json:"trace_ids"`
	HoldStatus        string   `json:"hold_status"`
	ExpectedRunIDs    []string `json:"expected_run_ids"`
}
type PrivacyFenceInput struct {
	Scope          Scope    `json:"scope"`
	SourceEventIDs []string `json:"source_event_ids"`
	TraceIDs       []string `json:"trace_ids"`
	ExpectedRunIDs []string `json:"expected_run_ids"`
}
type PrivacyFenceResult struct {
	Allowed            bool     `json:"allowed"`
	DeletionRequestIDs []string `json:"deletion_request_ids"`
}

type privacyStore interface {
	DiscoverPrivacyImpact(context.Context, Scope, string, []string, int) (PrivacyImpact, error)
	QuarantinePrivacyImpact(context.Context, Scope, PrivacyImpact) error
	VerifyPrivacyPurge(context.Context, Scope, PrivacyImpact) (PrivacyPurgeReport, error)
	ClaimPrivacyDeletion(context.Context, Scope, time.Time, time.Duration) (Record, error)
	NextPrivacyAdvance(context.Context, time.Time) (Record, error)
	PrivacyBackupWaiting(context.Context, time.Time) ([]Record, error)
	PrivacyFence(context.Context, PrivacyFenceInput) (PrivacyFenceResult, error)
}

// legacySourceDeleteTestStore is implemented only by the in-package test
// repository. Runtime repositories must implement privacyStore.
type legacySourceDeleteTestStore interface {
	allowLegacySourceDeleteForTest()
}

func privacyStatus(r Record) (PrivacyDeletionStatus, error) {
	if r.Kind != privacyDeletionKind || r.PrivacyDeletion == nil || r.Revision < 1 {
		return PrivacyDeletionStatus{}, ErrUnavailable
	}
	return PrivacyDeletionStatus{ID: r.ID, JobStatus: r.Status, Revision: r.Revision, PrivacyDeletion: *r.PrivacyDeletion}, nil
}

func privacyJobID(scope Scope, principal, key string) string {
	return "privacy-deletion-" + ContentHash(structuredText(scope)+"\n"+principal+"\n"+key)
}

var privacyStateTransitions = map[string][]string{
	privacyRequested:         {privacyAuthorized, privacyCancelled},
	privacyAuthorized:        {privacyDiscoveringImpact, privacyHeld},
	privacyDiscoveringImpact: {privacyQuarantined, privacyFailed, privacyHeld},
	privacyQuarantined:       {privacyPurgingRuntime, privacyHeld},
	privacyPurgingRuntime:    {privacyPurgingDerived, privacyFailed, privacyHeld},
	privacyPurgingDerived:    {privacyVerifying, privacyPartial, privacyFailed, privacyHeld},
	privacyVerifying:         {privacyCompleted, privacyWaiting, privacyPartial, privacyFailed, privacyHeld},
	privacyWaiting:           {privacyVerifying, privacyHeld},
	privacyHeld:              {privacyQuarantined},
	privacyPartial:           {privacyPurgingRuntime, privacyHeld},
}

func privacyTransitionAllowed(from, to string) bool {
	return slices.Contains(privacyStateTransitions[from], to)
}

func validatePrivacyRequest(in *PrivacyDeletionRequest) error {
	if !validScope(in.Scope) || !slices.Contains([]string{"source_event", "trace", "subject", "work_checkpoint"}, in.TargetType) || len(in.TargetIDs) == 0 || len(in.TargetIDs) > 64 || !privacyLabel(in.IdempotencyKey, 128) || !privacyLabel(in.Reason, 256) {
		return ErrInvalid
	}
	if in.RetentionPolicyRef == "" {
		in.RetentionPolicyRef = retentionExternal
	}
	if in.RetentionPolicyRef != retentionExternal && in.RetentionPolicyRef != retentionSynthetic || in.LegalHoldStatus != "" && in.LegalHoldStatus != privacyHoldNone {
		return ErrInvalid
	}
	if in.TargetType == "subject" && (in.Scope.UserID == "" || len(in.TargetIDs) != 1 || in.TargetIDs[0] != in.Scope.UserID) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range in.TargetIDs {
		if !validRunLabel(id, 256) || containsSecret(id) || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	slices.Sort(in.TargetIDs)
	if in.LegalHoldStatus == "" {
		in.LegalHoldStatus = privacyHoldNone
	}
	return nil
}

func privacyLabel(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0 && !containsSecret(value)
}

func (s *Service) RequestPrivacyDeletion(ctx context.Context, token string, in PrivacyDeletionRequest) (PrivacyDeletionStatus, error) {
	if err := validatePrivacyRequest(&in); err != nil || s.Repo == nil {
		if err != nil {
			return PrivacyDeletionStatus{}, err
		}
		return PrivacyDeletionStatus{}, ErrUnavailable
	}
	action := "delete:sources"
	if in.TargetType == "work_checkpoint" {
		action = "delete:work_checkpoints"
	}
	who, err := s.authorize(ctx, token, action, in.Scope, "", "")
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	if who.Role != "human" {
		if in.TargetType == "work_checkpoint" {
			return PrivacyDeletionStatus{}, ErrDenied
		}
		who, err = s.authorize(ctx, token, "retention:delete:sources", in.Scope, "", "")
		if err != nil || who.Role != "service" {
			return PrivacyDeletionStatus{}, ErrDenied
		}
	}
	if in.TargetType == "work_checkpoint" {
		for _, id := range in.TargetIDs {
			perTarget, targetErr := s.authorize(ctx, token, action, in.Scope, id, "")
			if targetErr != nil {
				return PrivacyDeletionStatus{}, targetErr
			}
			if perTarget.Role != "human" || perTarget.PrincipalID != who.PrincipalID {
				return PrivacyDeletionStatus{}, ErrDenied
			}
		}
	}
	if in.TargetType == "subject" && in.Scope.UserID != who.PrincipalID {
		admin, adminErr := s.authorize(ctx, token, "delete:subjects", in.Scope, in.Scope.UserID, "")
		if adminErr != nil || admin.Role != "human" || admin.PrincipalID != who.PrincipalID {
			return PrivacyDeletionStatus{}, ErrDenied
		}
	}
	if in.RetentionPolicyRef == retentionSynthetic {
		local, localErr := s.authorize(ctx, token, "retention:synthetic_disposable", in.Scope, "", "")
		if localErr != nil || local.PrincipalID != who.PrincipalID || local.Role != who.Role {
			return PrivacyDeletionStatus{}, ErrDenied
		}
	}
	if _, ok := s.Repo.(privacyStore); !ok {
		return PrivacyDeletionStatus{}, ErrUnavailable
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	hash := ContentHash(structuredText(in))
	id := privacyJobID(in.Scope, who.PrincipalID, in.IdempotencyKey)
	var saved Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if current, getErr := tx.Get(ctx, colJobs, in.Scope, id); getErr == nil {
			if current.Kind != privacyDeletionKind || current.PrivacyDeletion == nil || current.PrivacyDeletion.RequestHash != hash {
				return ErrConflict
			}
			saved = current
			return nil
		} else if !errors.Is(getErr, ErrNotFound) {
			return getErr
		}
		privacyTx, ok := tx.(privacyStore)
		if !ok {
			return ErrUnavailable
		}
		impact, discoverErr := privacyTx.DiscoverPrivacyImpact(ctx, in.Scope, in.TargetType, in.TargetIDs, privacyImpactLimit)
		if discoverErr != nil {
			return discoverErr
		}
		if err := checkPrivacyOverlap(ctx, tx, in.Scope, id, in.TargetType, impact, false); err != nil {
			return err
		}
		if in.ExpectedRevision != 0 {
			if len(in.TargetIDs) != 1 || in.TargetType == "subject" {
				return ErrInvalid
			}
			matched := false
			collection := "sources"
			if in.TargetType == "trace" {
				collection = "execution_traces"
			}
			if in.TargetType == "work_checkpoint" {
				collection = colWorkCheckpoints
			}
			for _, ref := range impact.Records {
				if ref.Collection == collection && ref.ID == in.TargetIDs[0] {
					matched = ref.Revision == in.ExpectedRevision
				}
			}
			if !matched {
				return ErrConflict
			}
		}
		if err := privacyTx.QuarantinePrivacyImpact(ctx, in.Scope, impact); err != nil {
			return err
		}
		for _, ref := range impact.Records {
			if ref.Collection == colJobs || ref.Collection == "audit_events" {
				continue
			}
			record, getErr := tx.Get(ctx, ref.Collection, in.Scope, ref.ID)
			if getErr != nil {
				return getErr
			}
			if record.Revision != ref.Revision {
				return ErrConflict
			}
			if !record.PrivacyQuarantined {
				record.PrivacyQuarantined = true
				record.Revision++
				record.UpdatedAt = now
				if err := tx.Replace(ctx, ref.Collection, record, ref.Revision); err != nil {
					return err
				}
			}
		}
		authorized := now
		pd := &PrivacyDeletion{State: privacyQuarantined, RequesterType: who.Role, RequesterID: who.PrincipalID, TargetType: in.TargetType, TargetIDs: slices.Clone(in.TargetIDs), Reason: in.Reason, LegalHoldStatus: privacyHoldNone, RetentionPolicyRef: in.RetentionPolicyRef, RequestedAt: now, AuthorizedAt: &authorized, IdempotencyKey: in.IdempotencyKey, CorrelationID: auditRequestID(ctx), PurgeReportRef: "jobs/" + id + "#privacy_deletion.report", RequestHash: hash, Impact: impact, Report: PrivacyPurgeReport{LiveAccessRemoved: true, BackupExpiryPending: in.RetentionPolicyRef != retentionSynthetic}}
		saved = Record{ID: id, Scope: in.Scope, Kind: privacyDeletionKind, Status: JobStatusPending, Revision: 1, CreatedBy: who.PrincipalID, CreatedByType: who.Role, CreatedAt: now, UpdatedAt: now, RequestHash: hash, PrivacyDeletion: pd}
		if err := tx.Insert(ctx, colJobs, saved); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, who.PrincipalID, "request:privacy_deletion", id)
	})
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	return privacyStatus(saved)
}

func (s *Service) GetPrivacyDeletion(ctx context.Context, token, id string, scope Scope) (PrivacyDeletionStatus, error) {
	who, err := s.authorize(ctx, token, "read:privacy_deletion", scope, id, "")
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	if who.Role != "human" && who.Role != "service" {
		return PrivacyDeletionStatus{}, ErrDenied
	}
	r, err := s.Repo.Get(ctx, colJobs, scope, id)
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	return privacyStatus(r)
}

func (s *Service) ClaimPrivacyDeletion(ctx context.Context, token string, in PrivacyScopeInput) (PrivacyRuntimeAction, error) {
	who, err := s.authorize(ctx, token, "purge:runtime", in.Scope, "", "")
	if err != nil {
		return PrivacyRuntimeAction{}, err
	}
	if who.Role != "service" {
		return PrivacyRuntimeAction{}, ErrDenied
	}
	store, ok := s.Repo.(privacyStore)
	if !ok {
		return PrivacyRuntimeAction{}, ErrUnavailable
	}
	r, err := store.ClaimPrivacyDeletion(ctx, in.Scope, time.Now().UTC(), privacyRuntimeLease)
	if err != nil {
		return PrivacyRuntimeAction{}, err
	}
	pd := r.PrivacyDeletion
	if pd == nil {
		return PrivacyRuntimeAction{}, ErrUnavailable
	}
	return PrivacyRuntimeAction{DeletionRequestID: r.ID, Revision: r.Revision, LeaseToken: r.LeaseToken, RequestHash: pd.RequestHash, Scope: r.Scope, SourceEventIDs: slices.Clone(pd.Impact.SourceEventIDs), TraceIDs: slices.Clone(pd.Impact.TraceIDs), HoldStatus: pd.LegalHoldStatus, ExpectedRunIDs: slices.Clone(pd.Impact.ExpectedRunIDs)}, nil
}

func validRuntimeReceipt(pd *PrivacyDeletion, receipt PrivacyRuntimeReceipt) bool {
	if !validRuntimeReceiptEnvelope(pd, receipt) || !receipt.Quarantined || len(receipt.Errors) != 0 || !subset(receipt.ExpectedRunIDs, append(slices.Clone(receipt.MatchedRunIDs), receipt.AbsentRunIDs...)) {
		return false
	}
	if receipt.Held {
		if pd.LegalHoldStatus != privacyHoldHeld || receipt.Verified {
			return false
		}
	} else if pd.LegalHoldStatus == privacyHoldHeld || !receipt.Verified {
		return false
	}
	return true
}

func validRuntimeReceiptEnvelope(pd *PrivacyDeletion, receipt PrivacyRuntimeReceipt) bool {
	if receipt.RequestHash != pd.RequestHash || !sameStrings(receipt.ExpectedRunIDs, pd.Impact.ExpectedRunIDs) || len(receipt.Errors) > 16 || !subset(receipt.MatchedRunIDs, receipt.AffectedRunIDs) || receipt.Counts.Runs != len(receipt.AffectedRunIDs) {
		return false
	}
	for _, values := range [][]string{receipt.ExpectedRunIDs, receipt.MatchedRunIDs, receipt.AffectedRunIDs, receipt.AbsentRunIDs} {
		for _, id := range values {
			if !validRunLabel(id, 256) {
				return false
			}
		}
	}
	for _, message := range receipt.Errors {
		if len(message) > 512 || strings.IndexFunc(message, unicode.IsControl) >= 0 {
			return false
		}
	}
	return receipt.Counts.Runs >= 0 && receipt.Counts.Checkpoints >= 0 && receipt.Counts.Writes >= 0 && receipt.Counts.Blobs >= 0 && receipt.Counts.Journals >= 0
}

func failedRuntimeReceipt(receipt PrivacyRuntimeReceipt) (PrivacyRuntimeReceipt, string) {
	code := "runtime_verification_failed"
	if len(receipt.Errors) != 0 {
		code = "runtime_purge_failed"
	}
	receipt.Errors = []string{code}
	receipt.Verified = false
	return receipt, code
}

func sameStrings(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
func subset(a, b []string) bool {
	set := map[string]bool{}
	for _, id := range b {
		set[id] = true
	}
	for _, id := range a {
		if !set[id] {
			return false
		}
	}
	return true
}

func (s *Service) RecordPrivacyRuntimeReceipt(ctx context.Context, token, id string, in PrivacyRuntimeReceiptInput) (PrivacyDeletionStatus, error) {
	who, err := s.authorize(ctx, token, "purge:runtime", in.Scope, id, "")
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	if who.Role != "service" || in.ExpectedRevision < 1 || in.LeaseToken == "" {
		return PrivacyDeletionStatus{}, ErrDenied
	}
	var saved Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, getErr := tx.Get(ctx, colJobs, in.Scope, id)
		if getErr != nil {
			return getErr
		}
		incoming := in.Receipt
		if len(incoming.Errors) != 0 || !incoming.Verified && !incoming.Held {
			incoming, _ = failedRuntimeReceipt(incoming)
		}
		if r.Kind == privacyDeletionKind && r.PrivacyDeletion != nil && r.PrivacyDeletion.RuntimeReceipt != nil && r.Revision >= in.ExpectedRevision+1 && r.LeaseToken == "" && r.PrivacyDeletion.RuntimeLeaseRev == in.ExpectedRevision && r.PrivacyDeletion.RuntimeLeaseHash == ContentHash(in.LeaseToken) && structuredText(*r.PrivacyDeletion.RuntimeReceipt) == structuredText(incoming) {
			saved = r
			return nil
		}
		if r.Kind != privacyDeletionKind || r.PrivacyDeletion == nil || r.Revision != in.ExpectedRevision || r.LeaseToken != in.LeaseToken || !r.LeaseUntil.After(time.Now()) || r.Status != JobStatusRunning || r.PrivacyDeletion.State != privacyPurgingRuntime && r.PrivacyDeletion.State != privacyHeld {
			return ErrConflict
		}
		if !validRuntimeReceiptEnvelope(r.PrivacyDeletion, in.Receipt) {
			return ErrInvalid
		}
		previous := r.Revision
		r.PrivacyDeletion.RuntimeLeaseHash = ContentHash(in.LeaseToken)
		r.PrivacyDeletion.RuntimeLeaseRev = in.ExpectedRevision
		if len(in.Receipt.Errors) != 0 || !in.Receipt.Verified && !in.Receipt.Held {
			receipt, code := failedRuntimeReceipt(in.Receipt)
			r.PrivacyDeletion.RuntimeReceipt = &receipt
			r.PrivacyDeletion.ErrorSummary = code
			if r.Attempts >= maxJobAttempts {
				r.PrivacyDeletion.State, r.Status = privacyFailed, JobStatusFailed
			} else {
				r.PrivacyDeletion.State, r.Status = privacyPartial, JobStatusRetry
				next := time.Now().UTC().Add(retryDelay(r.Attempts))
				r.RunAt = &next
			}
		} else if !validRuntimeReceipt(r.PrivacyDeletion, in.Receipt) {
			return ErrInvalid
		} else if in.Receipt.Held {
			r.PrivacyDeletion.RuntimeReceipt = &in.Receipt
			r.PrivacyDeletion.State, r.Status = privacyHeld, JobStatusDone
		} else {
			r.PrivacyDeletion.RuntimeReceipt = &in.Receipt
			r.PrivacyDeletion.ErrorSummary = ""
			r.PrivacyDeletion.Report.RuntimePurged = true
			r.PrivacyDeletion.State, r.Status = privacyPurgingDerived, JobStatusPending
		}
		r.LeaseToken, r.LeaseUntil = "", time.Time{}
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if err := tx.Replace(ctx, colJobs, r, previous); err != nil {
			return err
		}
		if err := audit(ctx, tx, in.Scope, who.PrincipalID, "receipt:privacy_runtime", id); err != nil {
			return err
		}
		saved = r
		return nil
	})
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	return privacyStatus(saved)
}

func (s *Service) AdvancePrivacyDeletion(ctx context.Context, token, id string, in PrivacyRevisionInput) (PrivacyDeletionStatus, error) {
	who, err := s.authorize(ctx, token, "purge:canonical", in.Scope, id, "")
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	if who.Role != "service" || in.ExpectedRevision < 1 {
		return PrivacyDeletionStatus{}, ErrDenied
	}
	var saved Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, getErr := tx.Get(ctx, colJobs, in.Scope, id)
		if getErr != nil {
			return getErr
		}
		if r.Kind != privacyDeletionKind || r.PrivacyDeletion == nil || r.Revision != in.ExpectedRevision || r.PrivacyDeletion.LegalHoldStatus == privacyHoldHeld {
			return ErrConflict
		}
		pd := r.PrivacyDeletion
		if err := checkPrivacyOverlap(ctx, tx, in.Scope, id, pd.TargetType, pd.Impact, false); err != nil {
			return err
		}
		previous := r.Revision
		switch pd.State {
		case privacyPurgingDerived:
			if pd.RuntimeReceipt == nil || !validRuntimeReceipt(pd, *pd.RuntimeReceipt) {
				return ErrConflict
			}
			for _, sourceID := range pd.Impact.SourceEventIDs {
				source, sourceErr := tx.Get(ctx, "sources", in.Scope, sourceID)
				if errors.Is(sourceErr, ErrNotFound) {
					continue
				}
				if sourceErr != nil || !source.PrivacyQuarantined {
					if sourceErr != nil {
						return sourceErr
					}
					return ErrConflict
				}
				if err := tx.Delete(ctx, "sources", in.Scope, sourceID, source.Revision); err != nil {
					return err
				}
				now := time.Now().UTC().Truncate(time.Millisecond)
				receipt := Record{ErasureParentID: r.ID, ID: sourceDeletionID(in.Scope, sourceID), Scope: in.Scope, Kind: "source_deletion", Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: "sources", ID: sourceID, Revision: source.Revision}}
				// Check first: on Postgres a duplicate-key insert aborts the whole transaction.
				if err := insertIfAbsent(ctx, tx, colJobs, receipt); err != nil {
					return err
				}
				if _, err := tx.Get(ctx, colJobs, in.Scope, sourceMemoryErasureID(in.Scope, sourceID)); errors.Is(err, ErrNotFound) {
					if err = enqueueSourceMemoryErasureAs(ctx, tx, in.Scope, sourceID, source.Revision, who.PrincipalID, who.Role, r.ID); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
			}
			if err := eraseWorkPrivacyImpact(ctx, tx, in.Scope, pd.Impact, r.ID); err != nil {
				return err
			}
			pd.State, r.Status, r.RunAt = privacyVerifying, JobStatusPending, nil
		case privacyVerifying:
			privacyTx, ok := tx.(privacyStore)
			if !ok {
				return ErrUnavailable
			}
			report, verifyErr := privacyTx.VerifyPrivacyPurge(ctx, in.Scope, pd.Impact)
			if verifyErr != nil {
				if errors.Is(verifyErr, ErrConflict) {
					next := time.Now().UTC().Add(2 * time.Second)
					r.Status, r.RunAt = JobStatusRetry, &next
					break
				}
				pd.ErrorSummary = "canonical_verification_failed"
				if r.Attempts >= maxJobAttempts {
					pd.State, r.Status = privacyFailed, JobStatusFailed
				} else {
					pd.State, r.Status = privacyPartial, JobStatusRetry
					next := time.Now().UTC().Add(retryDelay(r.Attempts))
					r.RunAt = &next
				}
				break
			}
			pd.ErrorSummary = ""
			r.Status, r.RunAt = JobStatusPending, nil
			report.RuntimePurged = pd.Report.RuntimePurged
			report.BackupOldestRetainedAt = pd.Report.BackupOldestRetainedAt
			report.BackupExpiryPending = pd.RetentionPolicyRef != retentionSynthetic && report.BackupOldestRetainedAt == nil
			pd.Report = report
			if !report.LiveAccessRemoved || !report.RuntimePurged || !report.DerivedPurged || !report.ArtifactsPurged {
				// A re-purge must wait again: backups taken before it can still hold what it removes.
				pd.State = privacyPartial
				pd.Report.BackupOldestRetainedAt = nil
				pd.Report.BackupExpiryPending = pd.RetentionPolicyRef != retentionSynthetic
			} else if report.BackupExpiryPending {
				pd.State, r.Status = privacyWaiting, JobStatusWaiting
			} else {
				now := time.Now().UTC().Truncate(time.Millisecond)
				pd.State, pd.CompletedAt, r.Status = privacyCompleted, &now, JobStatusDone
			}
		case privacyWaiting:
			// UpdatedAt is when verification parked the job; a backup taken
			// before then can still hold the deleted rows.
			oldest := in.BackupOldestRetainedAt
			if oldest == nil || !oldest.After(r.UpdatedAt) {
				return ErrConflict
			}
			at := oldest.UTC().Truncate(time.Millisecond)
			pd.Report.BackupOldestRetainedAt = &at
			pd.State, r.Status, r.RunAt = privacyVerifying, JobStatusPending, nil
		default:
			return ErrConflict
		}
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if err := tx.Replace(ctx, colJobs, r, previous); err != nil {
			return err
		}
		if err := audit(ctx, tx, in.Scope, who.PrincipalID, "advance:privacy_deletion:"+pd.State, id); err != nil {
			return err
		}
		saved = r
		return nil
	})
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	return privacyStatus(saved)
}

// ExpirePrivacyBackups sends every waiting deletion that finished before the
// oldest retained backup back through verification, which then completes it.
func (s *Service) ExpirePrivacyBackups(ctx context.Context, token string, oldest time.Time) (int, error) {
	store, ok := s.Repo.(privacyStore)
	if !ok || oldest.IsZero() {
		return 0, ErrUnavailable
	}
	rows, err := store.PrivacyBackupWaiting(ctx, oldest)
	if err != nil {
		return 0, err
	}
	// One row that cannot advance (a held overlap, a scope the token cannot act on) must not block the rest.
	advanced := 0
	var first error
	for _, r := range rows {
		if _, err := s.AdvancePrivacyDeletion(ctx, token, r.ID, PrivacyRevisionInput{Scope: r.Scope, ExpectedRevision: r.Revision, BackupOldestRetainedAt: &oldest}); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		advanced++
	}
	return advanced, first
}

func (s *Service) HoldPrivacyDeletion(ctx context.Context, token, id string, in PrivacyHoldInput) (PrivacyDeletionStatus, error) {
	return s.changePrivacyHold(ctx, token, id, in.Scope, in.ExpectedRevision, in.Reason, in.PolicyRef, true)
}
func (s *Service) ReleasePrivacyDeletionHold(ctx context.Context, token, id string, in PrivacyHoldInput) (PrivacyDeletionStatus, error) {
	return s.changePrivacyHold(ctx, token, id, in.Scope, in.ExpectedRevision, in.Reason, in.PolicyRef, false)
}
func (s *Service) changePrivacyHold(ctx context.Context, token, id string, scope Scope, revision int64, reason, policy string, held bool) (PrivacyDeletionStatus, error) {
	action := "hold:privacy_deletion"
	if !held {
		action = "release:legal_hold"
	}
	who, err := s.authorize(ctx, token, action, scope, id, "")
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	if who.Role != "human" || revision < 1 || !privacyLabel(reason, 256) || !privacyLabel(policy, 256) {
		return PrivacyDeletionStatus{}, ErrDenied
	}
	var saved Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, getErr := tx.Get(ctx, colJobs, scope, id)
		if getErr != nil {
			return getErr
		}
		if r.Kind != privacyDeletionKind || r.PrivacyDeletion == nil || r.Revision != revision {
			return ErrConflict
		}
		previous := r.Revision
		if r.Status == JobStatusRunning {
			return ErrConflict
		}
		if held {
			if err := checkPrivacyOverlap(ctx, tx, scope, id, r.PrivacyDeletion.TargetType, r.PrivacyDeletion.Impact, true); err != nil {
				return err
			}
			// Once runtime purge was claimed, even an expired lease leaves an
			// unknown irreversible boundary. Do not claim that a late hold retained
			// bytes which a crashed worker may already have removed.
			if !slices.Contains([]string{privacyRequested, privacyAuthorized, privacyDiscoveringImpact, privacyQuarantined}, r.PrivacyDeletion.State) {
				return ErrConflict
			}
			for _, ref := range r.PrivacyDeletion.Impact.Records {
				if ref.Collection == colJobs || ref.Collection == "audit_events" {
					continue
				}
				current, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
				if errors.Is(err, ErrNotFound) || err == nil && current.ContentErased {
					return ErrConflict
				}
				if err != nil {
					return err
				}
			}
			r.PrivacyDeletion.LegalHoldStatus, r.PrivacyDeletion.LegalHoldPolicyRef, r.PrivacyDeletion.State = privacyHoldHeld, policy, privacyHeld
		} else {
			if r.PrivacyDeletion.State != privacyHeld || r.PrivacyDeletion.LegalHoldStatus != privacyHoldHeld {
				return ErrConflict
			}
			r.PrivacyDeletion.LegalHoldStatus, r.PrivacyDeletion.State, r.Status = privacyHoldReleased, privacyQuarantined, JobStatusPending
		}
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if err := tx.Replace(ctx, colJobs, r, previous); err != nil {
			return err
		}
		if err := audit(ctx, tx, scope, who.PrincipalID, action, id); err != nil {
			return err
		}
		saved = r
		return nil
	})
	if err != nil {
		return PrivacyDeletionStatus{}, err
	}
	return privacyStatus(saved)
}

func (s *Service) CheckPrivacyFence(ctx context.Context, token string, in PrivacyFenceInput) (PrivacyFenceResult, error) {
	who, err := s.authorize(ctx, token, "read:privacy_fence", in.Scope, "", "")
	if err != nil {
		return PrivacyFenceResult{}, err
	}
	if !slices.Contains([]string{"agent", "pipeline", "service"}, who.Role) {
		return PrivacyFenceResult{}, ErrDenied
	}
	store, ok := s.Repo.(privacyStore)
	if !ok {
		return PrivacyFenceResult{}, ErrUnavailable
	}
	return store.PrivacyFence(ctx, in)
}
