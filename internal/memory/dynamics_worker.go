package memory

// SYS-05: explicitly configured derived-work admission. No product defaults.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

const dynamicsLoadProtocol = "arkism-sys05-load-v1"
const dynamicsLoadPrefix = "sys05_load_"
const dynamicsLoadContractSHA256 = "398df795b2eb03fc421158a10ca4094cdb199f3577545f28715b3d2d8c9ea2bc"

var (
	ErrDynamicsQueueFull           = errors.New("dynamics queue full")
	ErrDynamicsRuntimeBinding      = errors.New("dynamics runtime binding required")
	ErrDynamicsUnsequenced         = errors.New("dynamics captures have no proven queue order")
	ErrDynamicsSnapshotUnavailable = errors.New("dynamics snapshot unavailable")
)

// All nanoseconds are persisted integers, independent of the store's time codec.
type DynamicsLoadLimits struct {
	QueueCountLimit               int64 `json:"queue_count_limit"`
	QueueActiveBytesLimit         int64 `json:"queue_active_bytes_limit"`
	MaxWaitNS                     int64 `json:"max_wait_ns"`
	BatchLimit                    int64 `json:"batch_limit"`
	ConcurrencyLimit              int64 `json:"concurrency_limit"`
	RetryLimit                    int64 `json:"retry_limit"`
	RetryBackoffNS                int64 `json:"retry_backoff_ns"`
	DeadlineNS                    int64 `json:"deadline_ns"`
	WorkerSubstepTimeoutNS        int64 `json:"worker_substep_timeout_ns"`
	LeaseNS                       int64 `json:"lease_ns"`
	FinishTimeoutNS               int64 `json:"finish_timeout_ns"`
	PollIntervalNS                int64 `json:"poll_interval_ns"`
	PublishIntervalNS             int64 `json:"publish_interval_ns"`
	AllowedFreshnessNS            int64 `json:"allowed_freshness_ns"`
	SnapshotMaxItems              int64 `json:"snapshot_max_items"`
	SnapshotMaxBytes              int64 `json:"snapshot_max_bytes"`
	MaxConfiguredScopes           int64 `json:"max_configured_scopes"`
	TelemetryMaxBytes             int64 `json:"telemetry_max_bytes"`
	TerminalLedgerRetentionBudget int64 `json:"terminal_ledger_retention_budget"`
}

type DynamicsLoadConfig struct {
	Protocol           string             `json:"protocol"`
	Profile            string             `json:"profile"`
	Mode               string             `json:"mode"`
	LoadContractSHA256 string             `json:"load_contract_sha256"`
	LoadConfigSHA256   string             `json:"load_config_sha256"`
	Scopes             []Scope            `json:"scopes"`
	Limits             DynamicsLoadLimits `json:"limits"`
}

type DynamicsLoadRuntime struct {
	ready     bool
	config    DynamicsLoadConfig
	hash      string
	mu        sync.RWMutex
	workerMu  sync.Mutex
	snapshots map[Scope]*DynamicsLoadSnapshot
	nextScope int
}

// NewDynamicsLoadRuntime accepts only a complete, caller-pinned closed config.
func NewDynamicsLoadRuntime(raw []byte) (*DynamicsLoadRuntime, error) {
	if !dyn.ValidJSONUnicode(raw) {
		return nil, ErrInvalid
	}
	var cfg DynamicsLoadConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if !dynamicsUniqueJSON(raw) || dec.Decode(&cfg) != nil || dec.Decode(new(any)) != io.EOF || !validDynamicsLoadConfig(cfg) {
		return nil, ErrInvalid
	}
	return &DynamicsLoadRuntime{ready: true, config: cfg, hash: ContentHash(string(raw)), snapshots: make(map[Scope]*DynamicsLoadSnapshot)}, nil
}
func validDynamicsLoadConfig(c DynamicsLoadConfig) bool {
	if c.Protocol != dynamicsLoadProtocol || !validRunLabel(c.Profile, 128) || (c.Mode != "async" && c.Mode != "sync") || c.LoadContractSHA256 != dynamicsLoadContractSHA256 || !sourceHashPattern.MatchString(c.LoadConfigSHA256) {
		return false
	}
	l := c.Limits
	values := []int64{l.QueueCountLimit, l.QueueActiveBytesLimit, l.MaxWaitNS, l.BatchLimit, l.ConcurrencyLimit, l.RetryLimit, l.RetryBackoffNS, l.DeadlineNS, l.WorkerSubstepTimeoutNS, l.LeaseNS, l.FinishTimeoutNS, l.PollIntervalNS, l.PublishIntervalNS, l.AllowedFreshnessNS, l.SnapshotMaxItems, l.SnapshotMaxBytes, l.MaxConfiguredScopes, l.TelemetryMaxBytes, l.TerminalLedgerRetentionBudget}
	for _, v := range values {
		if v <= 0 || v > math.MaxInt64/4 {
			return false
		}
	}
	// This implementation serializes one scope coordinator. Wider parallelism is unsupported.
	if l.QueueCountLimit > 500 || l.ConcurrencyLimit != 1 || l.BatchLimit > 500 || l.SnapshotMaxItems >= 500 || int64(len(c.Scopes)) > l.MaxConfiguredScopes || len(c.Scopes) == 0 {
		return false
	}
	seen := map[Scope]bool{}
	for _, scope := range c.Scopes {
		if !validScope(scope) || seen[scope] {
			return false
		}
		seen[scope] = true
	}
	return true
}
func (r *DynamicsLoadRuntime) configured(scope Scope) bool {
	if r == nil || !r.ready {
		return false
	}
	for _, v := range r.config.Scopes {
		if v == scope {
			return true
		}
	}
	return false
}
func dynamicsNowNS() int64 { return time.Now().UTC().UnixNano() }

// DynamicsLoadPayload is stored only in jobs. It never appears in Record JSON.
// Coordinator, entry, and recovery roles have a separate closed namespace.
type DynamicsLoadPayload struct {
	Protocol              string `bson:"protocol"`
	ConfigSHA256          string `bson:"config_sha256"`
	Role                  string `bson:"role"`
	StorageIdentity       string `bson:"storage_identity"`
	Head                  int64  `bson:"head"`
	Tail                  int64  `bson:"tail"`
	Count                 int64  `bson:"count"`
	ActiveBytes           int64  `bson:"active_bytes"`
	LeaseGeneration       int64  `bson:"lease_generation"`
	LeaseToken            string `bson:"lease_token"`
	LeaseUntilNS          int64  `bson:"lease_until_ns"`
	PublicationGeneration int64  `bson:"publication_generation"`
	AsOfNS                int64  `bson:"as_of_ns"`
	PublishedNS           int64  `bson:"published_ns"`
	Withheld              bool   `bson:"withheld"`
	Ordinal               int64  `bson:"ordinal"`
	Capture               Ref    `bson:"capture"`
	EntryBytes            int64  `bson:"entry_bytes"`
	AcceptedNS            int64  `bson:"accepted_ns"`
	StartedNS             int64  `bson:"started_ns"`
	AppliedNS             int64  `bson:"applied_ns"`
	DueNS                 int64  `bson:"due_ns"`
	Attempts              int64  `bson:"attempts"`
	Reason                string `bson:"reason"`
	RepairStartNS         *int64 `bson:"repair_start_ns"`
	QueueDigest           string `bson:"queue_digest"`
}

// WorkDynamicsOnce is also the bounded substep used by WorkOnce. The original
// privacy and embedding round always gets its opportunity before this substep.
func (s *Service) WorkDynamicsOnce(ctx context.Context, token string) error {
	if s.DynamicsLoad == nil {
		return nil
	}
	r := s.DynamicsLoad
	if !r.ready {
		return ErrInvalid
	}
	if !r.workerMu.TryLock() {
		return nil
	}
	defer r.workerMu.Unlock()
	bound, cancel := context.WithTimeout(ctx, time.Duration(r.config.Limits.WorkerSubstepTimeoutNS))
	defer cancel()
	var result error
	r.mu.Lock()
	start := r.nextScope
	r.nextScope = (r.nextScope + 1) % len(r.config.Scopes)
	r.mu.Unlock()
	remaining := r.config.Limits.BatchLimit
	for i := 0; i < len(r.config.Scopes) && remaining > 0; i++ {
		scope := r.config.Scopes[(start+i)%len(r.config.Scopes)]
		processed, err := s.workDynamicsScope(bound, token, scope, remaining)
		remaining -= processed
		if err != nil && !errors.Is(err, ErrNotFound) {
			result = errors.Join(result, err)
		}
	}
	return result
}
func (s *Service) workDynamicsScope(ctx context.Context, token string, scope Scope, limit int64) (int64, error) {
	ctx, err := s.nativeActor(ctx, token, scope, "write:jobs")
	if err != nil {
		return 0, err
	}
	for i := int64(0); i < limit; i++ {
		claim, err := s.claimDynamics(ctx, scope)
		if errors.Is(err, ErrNotFound) {
			return i, s.PublishDynamicsSnapshot(ctx, token, scope)
		}
		if err != nil {
			return i, err
		}
		if err = s.applyDynamicsClaim(ctx, token, scope, claim); err != nil {
			return i + 1, err
		}
	}
	return limit, s.PublishDynamicsSnapshot(ctx, token, scope)
}

func (s *Service) applyDynamicsClaim(ctx context.Context, token string, scope Scope, claim Record) error {
	bound, cancel := context.WithTimeout(ctx, time.Duration(s.DynamicsLoad.config.Limits.DeadlineNS))
	defer cancel()
	prepared, err := s.prepareNativeCapture(bound, token, scope, claim.DynamicsLoad.Capture)
	if err != nil {
		return s.failDynamicsClaim(ctx, scope, claim, err)
	}
	var blocked error
	err = s.Repo.Transaction(bound, func(ctx context.Context, tx Tx) error {
		c, row, e := s.dynamicsClaimFence(ctx, tx, scope, claim)
		if e != nil {
			return e
		}
		if e = nativeCheckRestorePublicationTx(ctx, tx); e != nil {
			return e
		}
		var receipt NativeDynamicsReceipt
		receipt, blocked, e = nativeProcessCaptureTx(ctx, tx, scope, claim.DynamicsLoad.Capture, prepared.payload, prepared.deps, prepared.priorDeps, prepared.statePreview)
		if e != nil {
			return e
		}
		if blocked != nil {
			return finishDynamicsTx(ctx, tx, c, row, false, "native_admission_conflict")
		}
		if receipt.ProcessingSeq == nil {
			return ErrConflict
		}
		return finishDynamicsTx(ctx, tx, c, row, true, "")
	})
	if err != nil {
		return s.failDynamicsClaim(ctx, scope, claim, err)
	}
	return blocked
}
func finishDynamicsTx(ctx context.Context, tx Tx, c, row Record, success bool, reason string) error {
	p, ep := c.DynamicsLoad, row.DynamicsLoad
	if p.Count < 1 || p.ActiveBytes < ep.EntryBytes || ep.Ordinal != p.Head+1 {
		return ErrConflict
	}
	ep.Reason = reason
	if success {
		row.Status = JobStatusDone
		ep.AppliedNS = dynamicsNowNS()
		p.AsOfNS = ep.AcceptedNS
	} else {
		row.Status = JobStatusFailed
		p.Withheld = true
	}
	p.Head++
	p.Count--
	p.ActiveBytes -= ep.EntryBytes
	p.LeaseToken = ""
	p.LeaseUntilNS = 0
	ep.LeaseToken = ""
	ep.LeaseUntilNS = 0
	if err := dynamicsReplace(ctx, tx, row); err != nil {
		return err
	}
	return dynamicsReplace(ctx, tx, c)
}
func (s *Service) failDynamicsClaim(ctx context.Context, scope Scope, claim Record, cause error) error {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(s.DynamicsLoad.config.Limits.FinishTimeoutNS))
	defer cancel()
	err := s.Repo.Transaction(finish, func(ctx context.Context, tx Tx) error {
		c, row, e := s.dynamicsClaimFence(ctx, tx, scope, claim)
		if e != nil {
			return e
		}
		p, ep := c.DynamicsLoad, row.DynamicsLoad
		l := s.DynamicsLoad.config.Limits
		if ep.Attempts >= l.RetryLimit || dynamicsNowNS()-ep.AcceptedNS >= l.MaxWaitNS {
			return finishDynamicsTx(ctx, tx, c, row, false, "worker_exhausted")
		}
		row.Status = JobStatusRetry
		ep.DueNS = dynamicsNowNS() + l.RetryBackoffNS
		ep.Reason = "worker_failed"
		p.LeaseToken = ""
		p.LeaseUntilNS = 0
		ep.LeaseToken = ""
		ep.LeaseUntilNS = 0
		if e = dynamicsReplace(ctx, tx, row); e != nil {
			return e
		}
		return dynamicsReplace(ctx, tx, c)
	})
	return errors.Join(cause, err)
}

// Synchronous mode uses the identical durable admission and native apply
// transaction. It requires explicit existing job permissions on the caller;
// this integration grants no additional authority to evaluation services.
func (s *Service) applyDynamicsSynchronousTx(ctx context.Context, tx Tx, entry *Record, preflight *nativeCapturePreparation) error {
	if entry == nil || s.DynamicsLoad == nil || s.DynamicsLoad.config.Mode != "sync" {
		return nil
	}
	if preflight == nil {
		return ErrDynamicsRuntimeBinding
	}
	claim, err := s.claimDynamicsTx(ctx, tx, entry.Scope)
	if err != nil {
		return err
	}
	if claim.DynamicsLoad == nil || claim.ID != entry.ID {
		return ErrConflict
	}
	prepared, err := prepareDynamicsSynchronousTx(ctx, tx, entry.Scope, claim.DynamicsLoad.Capture, preflight)
	if err != nil {
		return err
	}
	c, row, err := s.dynamicsClaimFence(ctx, tx, entry.Scope, claim)
	if err != nil {
		return err
	}
	_, blocked, err := nativeProcessCaptureTx(ctx, tx, entry.Scope, claim.DynamicsLoad.Capture, prepared.payload, prepared.deps, prepared.priorDeps, prepared.statePreview)
	if err != nil {
		return err
	}
	if blocked != nil {
		return blocked
	}
	return finishDynamicsTx(ctx, tx, c, row, true, "")
}
func prepareDynamicsSynchronousTx(ctx context.Context, tx Tx, scope Scope, capture Ref, preflight *nativeCapturePreparation) (nativeCapturePreparation, error) {
	row, err := nativeGetTx(ctx, tx, scope, capture.ID, "capture")
	if err != nil {
		return nativeCapturePreparation{}, err
	}
	payload, err := nativeCapturePreview(row, scope, capture)
	if err != nil {
		return nativeCapturePreparation{}, err
	}
	deps, err := nativeCollectProofTx(ctx, tx, scope, nativeRoots(payload.Evaluation, *payload.Input))
	if err != nil {
		return nativeCapturePreparation{}, err
	}
	existing := make([]NativeDynamicsDependency, 0, len(deps))
	for _, dep := range deps {
		if dep.Ref != payload.Evaluation {
			existing = append(existing, dep)
		}
	}
	nativeSortDeps(existing)
	if !reflect.DeepEqual(existing, preflight.deps) {
		return nativeCapturePreparation{}, ErrConflict
	}
	return nativeCapturePreparation{payload, deps, preflight.priorDeps, preflight.statePreview}, nil
}
func (s *Service) prepareDynamicsSynchronous(ctx context.Context, token string, in MemoryOutcomeInput, evaluation Ref) (*nativeCapturePreparation, error) {
	if !s.DynamicsLoad.configured(in.Scope) || s.DynamicsLoad.config.Mode != "sync" {
		return nil, nil
	}
	_, retryErr := s.Repo.Get(ctx, "evaluations", in.Scope, evaluation.ID)
	if retryErr == nil {
		return nil, nil
	}
	if !errors.Is(retryErr, ErrNotFound) {
		return nil, retryErr
	}
	bound, err := s.nativeActor(ctx, token, in.Scope, "write:jobs")
	if err != nil {
		return nil, err
	}
	if _, err = s.authorize(bound, token, "read:agent_runs", in.Scope, "", ""); err != nil {
		return nil, err
	}
	if _, err = s.authorize(bound, token, "read:evaluations", in.Scope, evaluation.ID, ""); err != nil {
		return nil, err
	}
	enrollment, err := s.Repo.Get(bound, colJobs, in.Scope, nativeEnrollmentID(in.Scope, in.Target, in.Dimension))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if enrollment.NativeDynamics == nil {
		return nil, ErrConflict
	}
	state, err := s.Repo.Get(bound, colJobs, in.Scope, nativeStateID(enrollment.NativeDynamics.Key))
	if err != nil {
		return nil, err
	}
	if state.NativeDynamics == nil {
		return nil, ErrConflict
	}
	roots := append(append([]Ref{}, in.Evidence...), in.Target, in.Trace)
	deps, err := s.nativeAuthorizeProof(bound, token, in.Scope, roots)
	if err != nil {
		return nil, err
	}
	prior, err := s.nativeAuthorizeProof(bound, token, in.Scope, nativeDepRefs(state.NativeDynamics.Dependencies))
	if err != nil {
		return nil, err
	}
	return &nativeCapturePreparation{deps: deps, priorDeps: prior, statePreview: state}, nil
}

func dynamicsUniqueJSON(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if dynamicsJSONValue(dec, 0) != nil {
		return false
	}
	return dec.Decode(new(any)) == io.EOF
}
func dynamicsJSONValue(dec *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrInvalid
	}
	token, err := dec.Token()
	if err != nil {
		return ErrInvalid
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for dec.More() {
		if delimiter == '{' {
			if err = dynamicsJSONMember(dec, seen); err != nil {
				return err
			}
		}
		if err = dynamicsJSONValue(dec, depth+1); err != nil {
			return err
		}
	}
	closing, err := dec.Token()
	if err != nil {
		return ErrInvalid
	}
	if (delimiter == '{' && closing != json.Delim('}')) || (delimiter == '[' && closing != json.Delim(']')) {
		return ErrInvalid
	}
	return nil
}

func dynamicsJSONMember(dec *json.Decoder, seen map[string]bool) error {
	key, err := dec.Token()
	if err != nil {
		return ErrInvalid
	}
	name, ok := key.(string)
	if !ok || !dynamicsCanonicalJSONKey(name) || seen[name] {
		return ErrInvalid
	}
	seen[name] = true
	return nil
}

// Every key in this closed protocol is lowercase ASCII snake case. Reject
// aliases before encoding/json's case-folding can map them to a declared field.
func dynamicsCanonicalJSONKey(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}
