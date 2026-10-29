package crewrun

// Runtime capture stores only refs, hashes and accounting. It does not grant
// dispatch, effects or memory-outcome authority.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"
	"sync"
	"time"
)

type observationRuntime struct {
	mu          sync.Mutex
	evidence    ObservationEvidence
	starts      map[string]time.Time
	unallocated []UnallocatedUsageObservation
}

func runtimeObserver(ctx context.Context) *AttemptObserver {
	if !AttemptObserverEnabled(ctx) {
		return nil
	}
	o, _ := ctx.Value(attemptObserverKey{}).(*AttemptObserver)
	return o
}

func observationRuntimeFor(ctx context.Context) *observationRuntime {
	o := runtimeObserver(ctx)
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.runtime == nil {
		o.runtime = &observationRuntime{starts: map[string]time.Time{}, evidence: ObservationEvidence{
			ParentLinks: map[string][]byte{}, ParentSHA256: map[string]string{},
			Closures: []ObservationDispatchClosure{}, SenderJournals: map[string][]byte{}, SenderSHA256: map[string]string{},
		}}
	}
	return o.runtime
}

// observationDeliveryPending prevents an unfinished asynchronous trace delivery
// from being mistaken for complete capture. It does not wait for that delivery.
func observationDeliveryPending(ctx context.Context) func() {
	o := runtimeObserver(ctx)
	if o == nil {
		return func() {}
	}
	o.mu.Lock()
	o.pending++
	o.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			o.pending--
			o.mu.Unlock()
		})
	}
}

type observationDirectKey struct{}
type observationRequestKey struct{}

func withDirectObservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, observationDirectKey{}, true)
}

// ObservationEvidenceFromContext returns an independent copy of captured local
// integrity evidence. Caller pins are not authentication or run completeness.
func ObservationEvidenceFromContext(ctx context.Context) ObservationEvidence {
	r := observationRuntimeFor(ctx)
	if r == nil {
		return ObservationEvidence{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := ObservationEvidence{ParentLinks: map[string][]byte{}, ParentSHA256: maps.Clone(r.evidence.ParentSHA256),
		SenderJournals: map[string][]byte{}, SenderSHA256: maps.Clone(r.evidence.SenderSHA256), Closures: []ObservationDispatchClosure{}}
	for key, raw := range r.evidence.ParentLinks {
		result.ParentLinks[key] = bytes.Clone(raw)
	}
	for key, raw := range r.evidence.SenderJournals {
		result.SenderJournals[key] = bytes.Clone(raw)
	}
	for _, closure := range r.evidence.Closures {
		closure.MemberAttemptIDs = append([]string{}, closure.MemberAttemptIDs...)
		result.Closures = append(result.Closures, closure)
	}
	return result
}

func observationScopeFrom(ctx context.Context, client *Client) (ObservationScope, bool) {
	prior, exists := AttemptContextFrom(ctx)
	if client != nil {
		s := client.Scope
		scope := ObservationScope(s)
		if validateObservation(scope) == nil {
			if exists && validateObservation(prior.Run.Scope) == nil && prior.Run.Scope != scope {
				MarkAttemptObservationIncomplete(ctx, "identity_unavailable")
				return ObservationScope{}, false
			}
			return scope, true
		}
	}
	if exists && validateObservation(prior.Run.Scope) == nil {
		return prior.Run.Scope, true
	}
	MarkAttemptObservationIncomplete(ctx, "identity_unavailable")
	return ObservationScope{}, false
}

func observationDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func observationExecutionContext(ctx context.Context, run ObservationRun, input, definition any, nodeID string, nodeAttempt int64) context.Context {
	if !AttemptObserverEnabled(ctx) {
		return ctx
	}
	previous, hasPrevious := AttemptContextFrom(ctx)
	next := AttemptContext{Run: run, NodeID: nodeID, NodeAttempt: nodeAttempt}
	if hasPrevious {
		next.Task, next.ParentAttemptID = previous.Task, previous.ParentAttemptID
		next.FallbackParentCallKey = previous.FallbackParentCallKey
	}
	if !hasPrevious || validateObservation(next.Task) != nil {
		inputHash, inputErr := observationDigest(input)
		specHash, specErr := observationDigest(definition)
		if inputErr != nil || specErr != nil {
			return unavailableObservationContext(ctx)
		}
		next.Task = ObservationTask{Namespace: "crew_experiment", TaskID: run.RunRef.ID,
			TaskVersion: "native-crew-observation-v2", InputSHA256: inputHash, SpecSHA256: specHash}
	}
	if hasPrevious && validateObservation(previous.Run.Scope) == nil && previous.Run.Scope != run.Scope {
		return unavailableObservationContext(ctx)
	}
	probe := WithAttemptContext(ctx, next)
	if _, err := AttemptIdentityFromContext(probe, "main", "identity-check", 0, nil); err != nil {
		return unavailableObservationContext(ctx)
	}
	return probe
}

func unavailableObservationContext(ctx context.Context) context.Context {
	MarkAttemptObservationIncomplete(ctx, "identity_unavailable")
	return WithAttemptContext(ctx, AttemptContext{})
}

func observationLatest(ctx context.Context, id string) (AttemptObservation, bool) {
	o := runtimeObserver(ctx)
	if o != nil {
		for _, record := range o.Snapshot().Latest {
			if record.Identity.AttemptID == id {
				return record, true
			}
		}
	}
	return AttemptObservation{}, false
}

func observationRecord(ctx context.Context, record AttemptObservation) (AttemptObservation, bool) {
	if !AttemptObserverEnabled(ctx) {
		return record, false
	}
	sealed, err := SealAttemptObservation(record)
	if err != nil {
		MarkAttemptObservationIncomplete(ctx, "invalid_observation")
		return record, false
	}
	RecordAttempt(ctx, sealed)
	accepted, ok := observationLatest(ctx, sealed.Identity.AttemptID)
	if !ok || accepted.RecordSHA256 != sealed.RecordSHA256 {
		return sealed, false
	}
	if sealed.Identity.ParentAttemptID != nil {
		observationParentEvidence(ctx, sealed)
	}
	return sealed, true
}

func observationParentEvidence(ctx context.Context, child AttemptObservation) {
	r := observationRuntimeFor(ctx)
	id := child.Identity.AttemptID
	r.mu.Lock()
	_, exists := r.evidence.ParentLinks[id]
	r.mu.Unlock()
	if exists {
		return
	}
	parent, ok := observationLatest(ctx, *child.Identity.ParentAttemptID)
	if !ok {
		MarkAttemptObservationIncomplete(ctx, "host_attempts_incomplete")
		return
	}
	root := child.Identity.Run
	if root.RootRunCollection == nil || root.RootRunID == nil {
		MarkAttemptObservationIncomplete(ctx, "identity_unavailable")
		return
	}
	sourceHash, err := observationDigest(parent)
	if err != nil {
		MarkAttemptObservationIncomplete(ctx, "invalid_observation")
		return
	}
	relation := "same_execution_dispatch"
	if root.RunRef.Collection != parent.Identity.Run.RunRef.Collection || root.RunRef.ID != parent.Identity.Run.RunRef.ID {
		relation = "subflow"
		if root.RunRef.Collection == "crew_meetings" {
			relation = "linked_meeting"
		}
	}
	proof, err := SealObservationParentLink(ObservationParentLinkEvidence{ContractID: CrewObservationContractID,
		Scope: root.Scope, Task: child.Task, RootRunCollection: *root.RootRunCollection, RootRunID: *root.RootRunID,
		ParentAttemptID: parent.Identity.AttemptID, ChildAttemptID: id, ParentRunRef: parent.Identity.Run.RunRef,
		ChildRunRef: root.RunRef, Relationship: relation, SourceSHA256: sourceHash})
	if err != nil {
		MarkAttemptObservationIncomplete(ctx, "invalid_observation")
		return
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		MarkAttemptObservationIncomplete(ctx, "invalid_observation")
		return
	}
	sum := sha256.Sum256(raw)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.evidence.ParentLinks[id]; !exists {
		r.evidence.ParentLinks[id] = raw
		r.evidence.ParentSHA256[id] = hex.EncodeToString(sum[:])
	}
}

func observationAnswerOnly(answer aiAnswer) aiAnswer {
	return aiAnswer{observationID: answer.observationID, observationCallKey: answer.observationCallKey, observationUsage: answer.observationUsage}
}

func observationAnswerParent(ctx context.Context, answer aiAnswer, fallback bool) context.Context {
	if !AttemptObserverEnabled(ctx) || answer.observationID == "" {
		return ctx
	}
	parent, ok := observationLatest(ctx, answer.observationID)
	if !ok {
		return ctx
	}
	current, ok := AttemptContextFrom(ctx)
	if !ok {
		return ctx
	}
	id := parent.Identity.AttemptID
	current.ParentAttemptID = &id
	if fallback {
		key := parent.Identity.CallKey
		current.FallbackParentCallKey = &key
	}
	return WithAttemptContext(ctx, current)
}

func observationCallIdentity(ctx context.Context, retry int, canonicalID *string) (AttemptIdentity, error) {
	label, _ := ctx.Value(flowCallLabelKey{}).(string)
	if label == "" {
		label = "main"
	}
	phase := "main"
	for _, part := range strings.Split(label, "/") {
		switch {
		case part == "plan":
			phase = "plan"
		case strings.HasPrefix(part, "worker"):
			phase = "worker"
		case part == "synthesis":
			phase = "synthesis"
		case strings.HasPrefix(part, "pick"):
			phase = "moderator"
		case part == "brief", part == "conclusion":
			phase = "scribe"
		case strings.HasPrefix(part, "turn"):
			phase = "meeting_turn"
		}
	}
	return AttemptIdentityFromContext(ctx, phase, fmt.Sprintf("%s/retry%d", label, retry), int64(retry), canonicalID)
}

func (e *Engine) observationRefused(ctx context.Context, _ aiSpec, retry int, canonicalID *string, replay bool, _ error) {
	if !AttemptObserverEnabled(ctx) {
		return
	}
	identity, err := observationCallIdentity(ctx, retry, canonicalID)
	if err != nil {
		MarkAttemptObservationIncomplete(ctx, "identity_unavailable")
		return
	}
	if _, exists := observationLatest(ctx, identity.AttemptID); exists {
		return
	}
	ac, _ := AttemptContextFrom(ctx)
	record := AttemptObservation{ContractID: CrewObservationContractID, Identity: identity, Task: ac.Task,
		Status: "cancelled_before_dispatch", DispatchState: "not_dispatched", Usage: UnknownUsageObservation(),
		ObservationRevision: 1, UsageRole: "transport_aggregate", LeafCoverage: "unknown"}
	if replay {
		record.Status, record.DispatchState = "unknown", "unknown"
		MarkAttemptObservationIncomplete(ctx, "host_attempts_incomplete")
	}
	observationRecord(ctx, record)
}

func (e *Engine) observationDispatch(ctx context.Context, _ aiSpec, retry int, canonicalID *string, dispatch func(context.Context) (aiAnswer, error)) (aiAnswer, error) {
	if !AttemptObserverEnabled(ctx) {
		return dispatch(ctx)
	}
	completed := observationDeliveryPending(ctx)
	defer completed()
	identity, err := observationCallIdentity(ctx, retry, canonicalID)
	if err != nil {
		MarkAttemptObservationIncomplete(ctx, "identity_unavailable")
		return dispatch(ctx)
	}
	ac, _ := AttemptContextFrom(ctx)
	role, coverage := "transport_aggregate", "unknown"
	if direct, _ := ctx.Value(observationDirectKey{}).(bool); direct {
		role, coverage = "provider_leaf", "not_applicable"
	}
	record, accepted := observationRecord(ctx, AttemptObservation{ContractID: CrewObservationContractID, Identity: identity, Task: ac.Task,
		Status: "reserved", DispatchState: "not_dispatched", Usage: UnknownUsageObservation(), ObservationRevision: 1,
		UsageRole: role, LeafCoverage: coverage})
	if !accepted {
		MarkAttemptObservationIncomplete(ctx, "host_attempts_incomplete")
		return dispatch(ctx)
	}
	start := time.Now()
	var transport *ObservationTransport
	callCtx := ctx
	if role == "transport_aggregate" {
		transport, err = NewObservationTransport(record)
		if err == nil {
			callCtx = context.WithValue(ctx, observationRequestKey{}, transport.Request())
		} else {
			MarkAttemptObservationIncomplete(ctx, "host_attempts_incomplete")
		}
	}
	answer, callErr := dispatch(callCtx)
	usage := observationAnswerUsage(ctx, answer)
	if transport != nil {
		usage, record.LeafCoverage = observationReadTransport(ctx, transport, record, usage)
	}
	finish := time.Now()
	startNS, finishNS, durationNS := start.UnixNano(), finish.UnixNano(), finish.Sub(start).Nanoseconds()
	// A wall-clock adjustment must not invalidate observed monotonic duration.
	if finishNS < startNS {
		finishNS = startNS
	}
	usage.StartedAtNS, usage.FinishedAtNS, usage.DurationNS, usage.DurationBasis = &startNS, &finishNS, &durationNS, "observed_monotonic"
	record.Usage, record.Status, record.DispatchState, record.ObservationRevision = usage, "succeeded", "dispatched", 2
	if callErr != nil {
		record.Status, record.ErrorCode = "failed", "execution_error"
		if errors.Is(callErr, context.Canceled) {
			record.Status, record.ErrorCode = "cancelled", "cancelled"
		}
		if errors.Is(callErr, context.DeadlineExceeded) {
			record.Status, record.ErrorCode = "timeout", "timeout"
		}
		var nodeErr *nodeError
		if errors.As(callErr, &nodeErr) && nodeErr.code == "timeout" {
			record.Status, record.ErrorCode = "timeout", "timeout"
		}
	}
	observationRecord(ctx, record)
	answer.observationID, answer.observationCallKey, answer.observationUsage = identity.AttemptID, identity.CallKey, &usage
	return answer, callErr
}

func observationTransportChild(parent, child AttemptObservation) bool {
	return ValidateAttemptObservation(child) == nil && observationChildMatches(parent, child)
}

func observationAnswerUsage(ctx context.Context, answer aiAnswer) UsageObservation {
	if answer.observationUsage != nil {
		if ValidateUsageObservation(*answer.observationUsage) == nil {
			return cloneObservationUsage(*answer.observationUsage)
		}
		MarkAttemptObservationIncomplete(ctx, "usage_invalid")
	}
	usage := UnknownUsageObservation()
	if answer.Usage == nil {
		return usage
	}
	legacy := answer.Usage
	// Legacy integer zeros have no field-presence evidence. Unlabelled numbers
	// must not be promoted into provider-reported accounting.
	if legacy.Estimated {
		if legacy.InputTokens > 0 {
			value := legacy.InputTokens
			usage.InputTokens, usage.InputBasis = &value, "estimated"
		}
		if legacy.OutputTokens > 0 {
			value := legacy.OutputTokens
			usage.OutputTokens, usage.OutputBasis = &value, "estimated"
		}
		usage.UsageBasis = usageOverall(usage.InputBasis, usage.OutputBasis)
		if legacy.CostUSD != nil && !math.IsNaN(*legacy.CostUSD) && !math.IsInf(*legacy.CostUSD, 0) && *legacy.CostUSD >= 0 {
			value := *legacy.CostUSD
			usage.CostUSD, usage.CostBasis = &value, "estimated"
		}
	} else {
		observationRetainUnallocated(ctx, legacy)
	}
	if ValidateUsageObservation(usage) != nil {
		MarkAttemptObservationIncomplete(ctx, "usage_invalid")
		return UnknownUsageObservation()
	}
	return usage
}

func cloneObservationUsage(usage UsageObservation) UsageObservation {
	raw, err := json.Marshal(usage)
	if err != nil {
		return UnknownUsageObservation()
	}
	copy, err := DecodeUsageObservation(raw)
	if err != nil {
		return UnknownUsageObservation()
	}
	return copy
}

// UnallocatedUsageObservation preserves legacy numeric evidence whose provenance
// cannot be established. These receipts are never summed with leaf/aggregate totals.
type UnallocatedUsageObservation struct {
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd"`
	Reason       string   `json:"reason"`
}

func observationRetainUnallocated(ctx context.Context, legacy *aiUsage) {
	if legacy.InputTokens < 0 || legacy.OutputTokens < 0 || (legacy.CostUSD != nil && (math.IsNaN(*legacy.CostUSD) || math.IsInf(*legacy.CostUSD, 0) || *legacy.CostUSD < 0)) {
		MarkAttemptObservationIncomplete(ctx, "usage_invalid")
		return
	}
	if legacy.InputTokens == 0 && legacy.OutputTokens == 0 && legacy.CostUSD == nil {
		return
	}
	r := observationRuntimeFor(ctx)
	value := UnallocatedUsageObservation{InputTokens: legacy.InputTokens, OutputTokens: legacy.OutputTokens, Reason: "legacy_provenance_unavailable"}
	if legacy.CostUSD != nil {
		cost := *legacy.CostUSD
		value.CostUSD = &cost
	}
	r.mu.Lock()
	r.unallocated = append(r.unallocated, value)
	r.mu.Unlock()
	MarkAttemptObservationIncomplete(ctx, "host_attempts_incomplete")
}

// UnallocatedUsageFromContext is a nonadditive diagnostic view, not a cost total.
func UnallocatedUsageFromContext(ctx context.Context) []UnallocatedUsageObservation {
	r := observationRuntimeFor(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]UnallocatedUsageObservation{}, r.unallocated...)
	for i := range out {
		if out[i].CostUSD != nil {
			value := *out[i].CostUSD
			out[i].CostUSD = &value
		}
	}
	return out
}

func observationMergeEvidence(ctx context.Context, incoming ObservationEvidence) {
	r := observationRuntimeFor(ctx)
	merge := func(raws map[string][]byte, hashes map[string]string, kind string) {
		for id, raw := range raws {
			sum := sha256.Sum256(raw)
			if hashes[id] != hex.EncodeToString(sum[:]) {
				MarkAttemptObservationIncomplete(ctx, "invalid_observation")
				continue
			}
			var err error
			if kind == "parent" {
				_, err = DecodeObservationParentLink(raw)
			} else {
				_, err = DecodeObservationSenderJournal(raw)
			}
			if err != nil {
				MarkAttemptObservationIncomplete(ctx, "invalid_observation")
				continue
			}
			r.mu.Lock()
			target, pins := r.evidence.ParentLinks, r.evidence.ParentSHA256
			if kind != "parent" {
				target, pins = r.evidence.SenderJournals, r.evidence.SenderSHA256
			}
			old, exists := target[id]
			conflict := exists && !bytes.Equal(old, raw)
			if !exists {
				target[id], pins[id] = bytes.Clone(raw), hashes[id]
			}
			r.mu.Unlock()
			if conflict {
				MarkAttemptObservationIncomplete(ctx, "observation_conflict")
			}
		}
	}
	merge(incoming.ParentLinks, incoming.ParentSHA256, "parent")
	merge(incoming.SenderJournals, incoming.SenderSHA256, "sender")
	for _, closure := range incoming.Closures {
		raw, err := json.Marshal(closure)
		if err != nil {
			MarkAttemptObservationIncomplete(ctx, "invalid_observation")
			continue
		}
		validated, err := DecodeObservationDispatchClosure(raw)
		if err != nil {
			MarkAttemptObservationIncomplete(ctx, "invalid_observation")
			continue
		}
		r.mu.Lock()
		exists, conflict := false, false
		for _, old := range r.evidence.Closures {
			if old.ParentAttemptID == validated.ParentAttemptID && old.SenderBoundaryID == validated.SenderBoundaryID {
				exists = true
				conflict = old.RecordSHA256 != validated.RecordSHA256
				break
			}
		}
		if !exists {
			r.evidence.Closures = append(r.evidence.Closures, validated)
		}
		r.mu.Unlock()
		if conflict {
			MarkAttemptObservationIncomplete(ctx, "observation_conflict")
		}
	}
}

func observationReadTransport(ctx context.Context, transport *ObservationTransport, record AttemptObservation, usage UsageObservation) (UsageObservation, string) {
	result, readErr := transport.Read()
	if readErr != nil || len(result.Issues) > 0 {
		MarkAttemptObservationIncomplete(ctx, "host_attempts_incomplete")
	}
	observationMergeEvidence(ctx, result.Evidence)
	for _, child := range result.Records {
		if observationTransportChild(record, child) {
			RecordAttempt(ctx, child)
		} else {
			MarkAttemptObservationIncomplete(ctx, "invalid_observation")
		}
	}
	if ValidateUsageObservation(result.Aggregate) == nil && len(result.Records) > 0 {
		usage = result.Aggregate
	}
	record.LeafCoverage = result.Coverage
	if record.LeafCoverage == "complete" && (readErr != nil || len(result.Issues) > 0) {
		record.LeafCoverage = "partial"
	}
	if !observationContains([]string{"unknown", "partial", "complete"}, record.LeafCoverage) {
		record.LeafCoverage = "unknown"
	}
	if closeErr := transport.Close(); closeErr != nil {
		MarkAttemptObservationIncomplete(ctx, "host_attempts_incomplete")
	}
	return usage, record.LeafCoverage
}

// AttemptObservationReport is an immutable, locally pinned capture. It refers
// only to Snapshot, not subsequent observations, authentication or whole-run
// coverage. Original host journals remain available through the evidence getter.
type AttemptObservationReport struct {
	Snapshot        AttemptLedgerSnapshot
	Evidence        ObservationEvidence
	Qualification   ObservationQualification
	RecordSetSHA256 string
}

// FinalizeAttemptObservationReport checks original sender boundaries before
// constructing a distinct controller-boundary view of their causal descendants.
// It performs no I/O and never changes the collector or its original evidence.
func FinalizeAttemptObservationReport(ctx context.Context) (AttemptObservationReport, error) {
	var report AttemptObservationReport
	observer := runtimeObserver(ctx)
	if observer == nil {
		return report, observationError("observer_disabled", "$")
	}
	report.Snapshot = observer.Snapshot()
	original := ObservationEvidenceFromContext(ctx)
	before, err := observationDigest(struct {
		Snapshot AttemptLedgerSnapshot
		Evidence ObservationEvidence
	}{report.Snapshot, original})
	if err != nil {
		return report, err
	}
	latest := map[string]AttemptObservation{}
	for _, record := range report.Snapshot.Latest {
		latest[record.Identity.AttemptID] = record
	}
	if err = qualifyObservationParents(latest, original); err != nil {
		return report, err
	}
	report.Evidence = ObservationEvidence{ParentLinks: original.ParentLinks, ParentSHA256: original.ParentSHA256,
		SenderJournals: map[string][]byte{}, SenderSHA256: map[string]string{}, Closures: []ObservationDispatchClosure{}}
	report.RecordSetSHA256, err = observationDigest(report.Snapshot.Records)
	if err != nil {
		return report, err
	}
	for _, parent := range report.Snapshot.Latest {
		if parent.Status == "reserved" {
			return report, observationError("pending_attempt", "$")
		}
		if parent.UsageRole == "provider_leaf" {
			continue
		}
		if err = observationVerifyOriginalBoundary(parent, latest, original); err != nil {
			return report, err
		}
		if err = observationControllerBoundary(&report, parent, latest); err != nil {
			return report, err
		}
	}
	report.Qualification, err = QualifyObservationLedger(report.Snapshot, report.Evidence)
	if err != nil {
		return report, err
	}
	after, err := observationDigest(struct {
		Snapshot AttemptLedgerSnapshot
		Evidence ObservationEvidence
	}{observer.Snapshot(), ObservationEvidenceFromContext(ctx)})
	if err != nil {
		return report, err
	}
	if before != after {
		return report, observationError("snapshot_changed", "$")
	}
	return report, nil
}

func observationVerifyOriginalBoundary(parent AttemptObservation, latest map[string]AttemptObservation, original ObservationEvidence) error {
	if parent.UsageRole != "transport_aggregate" || parent.LeafCoverage != "complete" {
		return observationError("incomplete_leaf_coverage", "$")
	}
	var closure *ObservationDispatchClosure
	for _, candidate := range original.Closures {
		if candidate.ParentAttemptID == parent.Identity.AttemptID {
			if closure != nil {
				return observationError("duplicate_closure", "$")
			}
			copy := candidate
			closure = &copy
		}
	}
	if closure == nil {
		return observationError("unproven_leaf_coverage", "$")
	}
	// A host journal proves exactly its own directly observed leaf boundary.
	// Future causal retry/worker calls are covered by the separate controller view.
	local := map[string]AttemptObservation{parent.Identity.AttemptID: parent}
	for id, record := range latest {
		if record.Identity.ParentAttemptID != nil && *record.Identity.ParentAttemptID == parent.Identity.AttemptID && record.Identity.IdentityBasis == "observed_host_leaf" {
			if err := qualifyObservationParent(record, parent, original.ParentLinks[id], original.ParentSHA256[id]); err != nil {
				return err
			}
			local[id] = record
		}
	}
	_, err := qualifyObservationClosure(parent, *closure, local, original)
	return err
}

func observationControllerBoundary(report *AttemptObservationReport, parent AttemptObservation, latest map[string]AttemptObservation) error {
	members, err := observationDescendants(parent.Identity.AttemptID, latest)
	if err != nil {
		return err
	}
	opening := ""
	for _, record := range report.Snapshot.Records {
		if record.Identity.AttemptID == parent.Identity.AttemptID && record.ObservationRevision == 1 && record.Status == "reserved" && record.DispatchState == "not_dispatched" {
			opening = record.RecordSHA256
			break
		}
	}
	if opening == "" {
		return observationError("opening_event_missing", "$")
	}
	// This digest binds this report's captured terminal revisions. The frozen
	// journal qualifier proves membership; callers must keep this snapshot pair.
	closing := report.RecordSetSHA256
	run := parent.Identity.Run
	boundary := "controller-" + parent.Identity.AttemptID
	journal, err := SealObservationSenderJournal(ObservationSenderJournal{ContractID: CrewObservationContractID, Scope: run.Scope, Task: parent.Task,
		RootRunCollection: *run.RootRunCollection, RootRunID: *run.RootRunID, ParentAttemptID: parent.Identity.AttemptID,
		SenderBoundaryID: boundary, State: "closed", MemberAttemptIDs: members, OpeningEventSHA256: opening, ClosingEventSHA256: &closing})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	pin := observationBytesSHA(raw)
	membership, err := ObservationMembershipHash(members)
	if err != nil {
		return err
	}
	closure, err := SealObservationDispatchClosure(ObservationDispatchClosure{ContractID: CrewObservationContractID, Scope: run.Scope, Task: parent.Task,
		RootRunCollection: *run.RootRunCollection, RootRunID: *run.RootRunID, ParentAttemptID: parent.Identity.AttemptID, SenderBoundaryID: boundary,
		MemberAttemptIDs: members, MembershipSHA256: membership, SenderJournalSHA256: pin})
	if err != nil {
		return err
	}
	report.Evidence.SenderJournals[boundary], report.Evidence.SenderSHA256[boundary] = raw, pin
	report.Evidence.Closures = append(report.Evidence.Closures, closure)
	return nil
}
