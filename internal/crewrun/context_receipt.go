package crewrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// SourceObservation is the closed fourteen-field v2 source receipt. A pinned
// historical read is data, not a perpetual permission grant or lifecycle check.
type SourceObservation struct {
	Scope             ObservationScope `json:"scope"`
	Collection        string           `json:"collection"`
	ID                string           `json:"id"`
	Revision          *int64           `json:"revision"`
	ContentSHA256     *string          `json:"content_sha256"`
	ReadAtNS          *int64           `json:"read_at_ns"`
	AvailableAtNS     *int64           `json:"available_at_ns"`
	AvailabilitySeq   *int64           `json:"availability_seq"`
	ProvenanceStatus  string           `json:"provenance_status"`
	ReadAuthorization string           `json:"read_authorization"`
	Sharing           string           `json:"sharing"`
	ObservationReason string           `json:"observation_reason"`
	ObservedAtNS      *int64           `json:"observed_at_ns"`
	Authority         string           `json:"authority"`
}

// ContextObservation preserves read, briefing, summary and consumption as
// separate stages. SummaryRef is a real record CAS, never a turn sequence.
type ContextObservation struct {
	ContractID          string              `json:"contract_id"`
	Run                 ObservationRun      `json:"run"`
	Task                ObservationTask     `json:"task"`
	NodeID              string              `json:"node_id"`
	NodeAttempt         int64               `json:"node_attempt"`
	ReadAtNS            int64               `json:"read_at_ns"`
	Sources             []SourceObservation `json:"sources"`
	ContextSHA256       *string             `json:"context_sha256"`
	ContextHashState    string              `json:"context_hash_state"`
	RecordSHA256        string              `json:"record_sha256"`
	ContextStage        string              `json:"context_stage"`
	ParentContextSHA256 *string             `json:"parent_context_sha256"`
	SummaryRef          *ObservationRef     `json:"summary_ref"`
	FreshnessAction     string              `json:"freshness_action"`
}

func init() {
	observationRules[reflect.TypeOf(SourceObservation{})] = []observationRule{
		{name: "scope", kind: "object"},
		{name: "collection", kind: "string", minLength: 1, maxLength: 64},
		{name: "id", kind: "string", minLength: 1, maxLength: 256},
		{name: "revision", kind: "int64", nullable: true, minimum: 1, maximum: 9223372036854775807},
		{name: "content_sha256", kind: "string", nullable: true, minLength: 64, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "read_at_ns", kind: "int64", nullable: true, maximum: 9223372036854775807},
		{name: "available_at_ns", kind: "int64", nullable: true, maximum: 9223372036854775807},
		{name: "availability_seq", kind: "int64", nullable: true, maximum: 9223372036854775807},
		{name: "provenance_status", kind: "string", minLength: 1, maxLength: 80, enums: []string{"pinned", "revision_not_recorded", "not_observed", "unavailable"}},
		{name: "read_authorization", kind: "string", minLength: 1, maxLength: 80, enums: []string{"confirmed", "not_recorded", "revoked"}},
		{name: "sharing", kind: "string", minLength: 1, maxLength: 80, enums: []string{"team", "shared", "not_recorded"}},
		{name: "observation_reason", kind: "string", maxLength: 128},
		{name: "observed_at_ns", kind: "int64", nullable: true, maximum: 9223372036854775807},
		{name: "authority", kind: "string", minLength: 1, maxLength: 80, enums: []string{"data_only_untrusted"}},
	}
	observationRules[reflect.TypeOf(ContextObservation{})] = []observationRule{
		{name: "contract_id", kind: "string", minLength: 1, maxLength: 80, enums: []string{CrewObservationContractID}},
		{name: "run", kind: "object"}, {name: "task", kind: "object"},
		{name: "node_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "node_attempt", kind: "int64", minimum: 1, maximum: 10000},
		{name: "read_at_ns", kind: "int64", maximum: 9223372036854775807},
		// Elements and size are checked explicitly; no caller-defined schema extension.
		{name: "sources", kind: "context_sources"},
		{name: "context_sha256", kind: "string", nullable: true, minLength: 64, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "context_hash_state", kind: "string", minLength: 1, maxLength: 80, enums: []string{"recorded", "not_recorded"}},
		{name: "record_sha256", kind: "string", minLength: 64, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "context_stage", kind: "string", minLength: 1, maxLength: 80, enums: []string{"source_read", "briefing", "summary_handoff", "turn_input"}},
		{name: "parent_context_sha256", kind: "string", nullable: true, minLength: 64, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "summary_ref", kind: "object", nullable: true},
		{name: "freshness_action", kind: "string", minLength: 1, maxLength: 80, enums: []string{"continue", "refresh", "restart", "block"}},
	}
}

func ValidateSourceObservation(value SourceObservation) error {
	if err := validateObservation(value); err != nil {
		return err
	}
	if value.ProvenanceStatus == "pinned" && (value.Revision == nil || value.ContentSHA256 == nil || value.ReadAtNS == nil || value.ReadAuthorization != "confirmed") {
		return observationError("invalid_value", "$.provenance_status")
	}
	if value.AvailableAtNS != nil && value.ReadAtNS != nil && *value.AvailableAtNS > *value.ReadAtNS {
		return observationError("invalid_value", "$.available_at_ns")
	}
	return nil
}
func contextFields(value ContextObservation) error {
	if err := validateObservation(value); err != nil {
		return err
	}
	if err := validateObservationRun(value.Run); err != nil {
		return err
	}
	if value.Sources == nil || len(value.Sources) > 100 {
		return observationError("invalid_value", "$.sources")
	}
	for _, s := range value.Sources {
		if err := ValidateSourceObservation(s); err != nil {
			return err
		}
	}
	if (value.ContextSHA256 == nil) != (value.ContextHashState == "not_recorded") {
		return observationError("invalid_value", "$.context_sha256")
	}
	return nil
}
func contextHash(value ContextObservation) (string, error) {
	var raw bytes.Buffer
	if err := encodeObservation(&raw, reflect.ValueOf(value), true); err != nil {
		return "", err
	}
	return observationBytesSHA(raw.Bytes()), nil
}
func SealContextObservation(value ContextObservation) (ContextObservation, error) {
	value = cloneObservationValue(reflect.ValueOf(value)).Interface().(ContextObservation)
	value.RecordSHA256 = strings.Repeat("0", 64)
	if err := contextFields(value); err != nil {
		return ContextObservation{}, err
	}
	hash, err := contextHash(value)
	value.RecordSHA256 = hash
	return value, err
}
func ValidateContextObservation(value ContextObservation) error {
	if err := contextFields(value); err != nil {
		return err
	}
	hash, err := contextHash(value)
	if err != nil {
		return err
	}
	if hash != value.RecordSHA256 {
		return observationError("hash_mismatch", "$.record_sha256")
	}
	return nil
}
func DecodeSourceObservation(raw []byte) (SourceObservation, error) {
	var value SourceObservation
	if err := decodeObservationJSON(raw, &value); err != nil {
		return value, err
	}
	return value, ValidateSourceObservation(value)
}
func DecodeContextObservation(raw []byte) (ContextObservation, error) {
	var value ContextObservation
	if err := decodeObservationJSON(raw, &value); err != nil {
		return value, err
	}
	return value, ValidateContextObservation(value)
}

// CanonicalContextObservation includes the sealed record hash. The record hash
// itself omits that one field, using the same accepted typed canonical encoder.
func CanonicalContextObservation(value ContextObservation) ([]byte, error) {
	if err := ValidateContextObservation(value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err := encodeObservation(&out, reflect.ValueOf(value), false)
	return out.Bytes(), err
}
func CanonicalSourceObservation(value SourceObservation) ([]byte, error) {
	if err := ValidateSourceObservation(value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err := encodeObservation(&out, reflect.ValueOf(value), false)
	return out.Bytes(), err
}

// ContextTextSHA256 binds exact selected UTF8 bytes before clipping/fencing.
// Invalid Unicode is rejected instead of being normalized into another body.
func ContextTextSHA256(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", observationError("invalid_unicode", "$")
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:]), nil
}

// ContextReceiptReader is optional. Existing MeetingStore implementations need
// not implement it. Permission/currentness checks are separate from observation.
type ContextReceiptReader interface {
	ContextReceipt(context.Context, TeamContext) (ContextReceiptRead, error)
}

// ContextSourceKey is the exact requested entity, not a text-derived identity.
type ContextSourceKey struct {
	Scope          ObservationScope
	Collection, ID string
}

// ContextUseFacts binds current-use evidence to the exact read snapshot. These
// are caller-supplied reader facts, not cryptographic authorization. Unknown
// metadata cannot prove active/readable/shared. No raw provider body is stored.
type ContextUseFacts struct {
	Source         ContextSourceKey
	Ref            ObservationRef
	ContentSHA256  string
	GuardSHA256    string
	ReadState      string // confirmed, unavailable, denied
	Lifecycle      string // active, inactive, deleted, unknown
	Sharing        string // team, shared, unknown
	CheckedAtNS    *int64
	PolicyRef      *ObservationRef
	PolicyReadAtNS *int64
	PolicyScope    ObservationScope
	PolicyState    string // confirmed, unknown, denied
}
type ContextReceiptRead struct {
	Observation SourceObservation
	Facts       ContextUseFacts
	Text        string `json:"-"`
}
type ContextSourceState struct {
	Observation SourceObservation
	GuardSHA256 string
}
type ContextReceiptMode string

const (
	ContextReceiptOff               ContextReceiptMode = "off"
	ContextReceiptObserve           ContextReceiptMode = "observe"
	ContextReceiptEnforceCurrentUse ContextReceiptMode = "enforce_current_use"
)

type ContextPremiseDisposition string

const (
	ContextPremiseUnchanged   ContextPremiseDisposition = "unchanged"
	ContextPremiseDisplayOnly ContextPremiseDisposition = "display_only"
	ContextPremiseRefresh     ContextPremiseDisposition = "refresh_supported"
	ContextPremiseRestart     ContextPremiseDisposition = "frozen_restart"
	ContextPremiseUnverified  ContextPremiseDisposition = "unverified"
)

type ContextUseInput struct {
	Mode             ContextReceiptMode
	Source           ContextSourceKey
	TeamRef          ObservationRef // actual definition identity; current policy may be a later CAS
	Previous         *ContextSourceState
	Current          ContextReceiptRead
	Premise          ContextPremiseDisposition
	Resume           bool
	ProtectedLineage *ContextObservation
	ExecutionRef     ObservationRef
	CurrentTask      ObservationTask
	Discord          bool
	DecisionAtNS     int64
}
type ContextUseDecision struct {
	Action           string
	ProposedAction   string
	Reason           string
	EvidenceComplete bool
}

func validContextMode(mode ContextReceiptMode) bool {
	return mode == ContextReceiptOff || mode == ContextReceiptObserve || mode == ContextReceiptEnforceCurrentUse
}
func contextDecision(mode ContextReceiptMode, action, reason string, complete bool) ContextUseDecision {
	result := ContextUseDecision{Action: action, ProposedAction: action, Reason: reason, EvidenceComplete: complete}
	if mode != ContextReceiptEnforceCurrentUse {
		result.Action = "continue"
	}
	return result
}
func sourceKey(value SourceObservation) ContextSourceKey {
	return ContextSourceKey{value.Scope, value.Collection, value.ID}
}
func validContextSHA(value string) bool {
	return len(value) == 64 && observationHash.MatchString(value)
}

// EvaluateContextUse evaluates bound evidence. It does not perform a read or
// grant permission. Enforce mode must be called again immediately before every
// primary/fallback dispatch; read/use atomicity is not claimed.
func EvaluateContextUse(input ContextUseInput) ContextUseDecision {
	if input.Mode == ContextReceiptOff {
		return contextDecision(input.Mode, "continue", "off", false)
	}
	if !validContextMode(input.Mode) {
		return contextDecision(ContextReceiptEnforceCurrentUse, "block", "invalid_mode", false)
	}
	if input.Resume && !contextProtectedLineage(input) {
		return contextDecision(input.Mode, "block", "history_without_lineage", false)
	}
	if reason := contextCurrentReason(input); reason != "" {
		return contextDecision(input.Mode, "block", reason, false)
	}
	if input.Previous == nil {
		if input.Premise != ContextPremiseUnchanged && input.Premise != ContextPremiseDisplayOnly {
			return contextDecision(input.Mode, "block", "unverified_premise", false)
		}
		return contextDecision(input.Mode, "continue", "initial_current_read", true)
	}
	return contextChangedDecision(input)
}
func contextCurrentReason(input ContextUseInput) string {
	read, facts := input.Current.Observation, input.Current.Facts
	if ValidateSourceObservation(read) != nil {
		return "invalid_source_receipt"
	}
	if validateObservation(input.Source.Scope) != nil || input.Source.Collection == "" || input.Source.ID == "" {
		return "invalid_source_key"
	}
	if sourceKey(read) != input.Source || facts.Source != input.Source || facts.Ref.Collection != read.Collection || facts.Ref.ID != read.ID {
		return "foreign_source"
	}
	if read.Revision == nil || facts.Ref.Revision != *read.Revision || read.ContentSHA256 == nil || facts.ContentSHA256 != *read.ContentSHA256 {
		return "snapshot_mismatch"
	}
	if reason := contextAuthorityReason(input); reason != "" {
		return reason
	}
	if reason := contextPolicyReason(input); reason != "" {
		return reason
	}
	if !validContextSHA(facts.GuardSHA256) {
		return "unknown_guard"
	}
	if read.ReadAtNS == nil || facts.CheckedAtNS == nil || *facts.CheckedAtNS < *read.ReadAtNS || input.DecisionAtNS < *facts.CheckedAtNS {
		return "unknown_current_check"
	}
	return ""
}
func contextPolicyReason(input ContextUseInput) string {
	facts := input.Current.Facts
	if input.TeamRef.Collection != "crew" || validateObservation(input.TeamRef) != nil {
		return "unknown_team_identity"
	}
	if facts.PolicyScope != input.Source.Scope {
		return "foreign_policy_scope"
	}
	if facts.PolicyRef == nil || facts.PolicyRef.Collection != "crew" || facts.PolicyRef.ID != input.TeamRef.ID || facts.PolicyRef.Revision < input.TeamRef.Revision {
		return "unknown_current_policy"
	}
	if facts.PolicyState != "confirmed" || facts.PolicyReadAtNS == nil || *facts.PolicyReadAtNS < 0 || *facts.PolicyReadAtNS > input.DecisionAtNS {
		return "unknown_current_policy"
	}
	return ""
}
func contextAuthorityReason(input ContextUseInput) string {
	read, facts := input.Current.Observation, input.Current.Facts
	if read.ReadAuthorization == "revoked" || facts.ReadState == "denied" {
		return "read_denied"
	}
	if read.ProvenanceStatus != "pinned" || read.ReadAuthorization != "confirmed" || facts.ReadState != "confirmed" {
		return "read_unavailable"
	}
	if facts.Lifecycle != "active" {
		return "lifecycle_not_current"
	}
	if facts.Sharing != read.Sharing || (facts.Sharing != "team" && facts.Sharing != "shared") {
		return "sharing_unknown"
	}
	if input.Discord && (read.Collection == "memories" || facts.Sharing != "shared") {
		return "sharing_denied"
	}
	return ""
}
func contextChangedDecision(input ContextUseInput) ContextUseDecision {
	old, current := input.Previous, input.Current
	if !contextKnownPrior(*old) || sourceKey(old.Observation) != input.Source {
		return contextDecision(input.Mode, "block", "unknown_prior_lineage", false)
	}
	if *current.Observation.Revision < *old.Observation.Revision {
		return contextDecision(input.Mode, "block", "revision_regression", false)
	}
	unchanged := *old.Observation.ContentSHA256 == *current.Observation.ContentSHA256 && old.GuardSHA256 == current.Facts.GuardSHA256
	if *old.Observation.Revision == *current.Observation.Revision && !unchanged {
		return contextDecision(input.Mode, "block", "snapshot_conflict", false)
	}
	if unchanged && (input.Premise == ContextPremiseUnchanged || input.Premise == ContextPremiseDisplayOnly) {
		return contextDecision(input.Mode, "continue", "unchanged_current_content", true)
	}
	switch input.Premise {
	case ContextPremiseRefresh:
		return contextDecision(input.Mode, "refresh", "typed_premise_refresh", true)
	case ContextPremiseRestart:
		return contextDecision(input.Mode, "restart", "frozen_premise_changed", true)
	default:
		return contextDecision(input.Mode, "block", "unverified_premise", false)
	}
}

// ContextReceiptSession is optional evidence collection, not an authorizer.
// Nil/zero-value sessions are off. Sinks must be short bounded local callbacks;
// synchronous callback latency is caller-owned. A nil sink is memory-only.
type ContextReceiptSession struct {
	mu      sync.Mutex
	mode    ContextReceiptMode
	enabled bool
	sink    func(ContextObservation) error
	pending int
	records map[string]ContextObservation
	issues  map[string]bool
}
type contextReceiptKey struct{}
type ContextReceiptSnapshot struct {
	Records  []ContextObservation
	Complete bool
	Issues   []string
}

func NewContextReceiptSession(mode ContextReceiptMode, sink func(ContextObservation) error) (*ContextReceiptSession, error) {
	if !validContextMode(mode) {
		return nil, observationError("invalid_value", "$.mode")
	}
	return &ContextReceiptSession{mode: mode, enabled: mode != ContextReceiptOff, sink: sink, records: map[string]ContextObservation{}, issues: map[string]bool{}}, nil
}
func WithContextReceiptSession(ctx context.Context, session *ContextReceiptSession) context.Context {
	return context.WithValue(ctx, contextReceiptKey{}, session)
}
func ContextReceiptSessionFrom(ctx context.Context) *ContextReceiptSession {
	if ctx == nil {
		return nil
	}
	session, _ := ctx.Value(contextReceiptKey{}).(*ContextReceiptSession)
	return session
}
func (s *ContextReceiptSession) Mode() ContextReceiptMode {
	if s == nil || !s.enabled {
		return ContextReceiptOff
	}
	return s.mode
}
func (s *ContextReceiptSession) note(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issues[reason] = true
}

// Capture binds supplied stage data to the actual currently enabled observer
// context. Installation may precede that context. It never creates an observer.
// This API cannot attest that an arbitrary caller supplied the real rendered text.
func (s *ContextReceiptSession) Capture(ctx context.Context, value ContextObservation) (ContextObservation, error) {
	if s == nil || !s.enabled {
		return ContextObservation{}, nil
	}
	if !AttemptObserverEnabled(ctx) {
		s.note("attempt_observer_disabled")
		return ContextObservation{}, observationError("observer_disabled", "$")
	}
	actual, ok := AttemptContextFrom(ctx)
	if !ok {
		s.note("attempt_context_missing")
		return ContextObservation{}, observationError("missing_context", "$")
	}
	value = cloneContextReceipt(value)
	for i := range value.Sources {
		value.Sources[i].ObservationReason = normalizeContextReason(value.Sources[i].ObservationReason)
	}
	value.ContractID = CrewObservationContractID
	value.Run = actual.Run
	value.Task = actual.Task
	value.NodeID = actual.NodeID
	value.NodeAttempt = actual.NodeAttempt
	if err := contextCaptureBinding(value); err != nil {
		s.note("context_binding_mismatch")
		return ContextObservation{}, err
	}
	sealed, err := SealContextObservation(value)
	if err != nil {
		s.note("invalid_context_receipt")
		return ContextObservation{}, err
	}
	return s.publish(sealed)
}
func (s *ContextReceiptSession) publish(value ContextObservation) (ContextObservation, error) {
	s.mu.Lock()
	if _, exists := s.records[value.RecordSHA256]; exists {
		s.mu.Unlock()
		return cloneContextReceipt(value), nil
	}
	s.records[value.RecordSHA256] = cloneContextReceipt(value)
	s.pending++
	s.mu.Unlock()
	reason := deliverContextReceipt(s.sink, value)
	s.mu.Lock()
	s.pending--
	if reason != "" {
		s.issues[reason] = true
	}
	s.mu.Unlock()
	return cloneContextReceipt(value), nil
}
func cloneContextReceipt(value ContextObservation) ContextObservation {
	return cloneObservationValue(reflect.ValueOf(value)).Interface().(ContextObservation)
}
func deliverContextReceipt(sink func(ContextObservation) error, value ContextObservation) (reason string) {
	if sink == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			reason = "sink_panic"
		}
	}()
	if sink(cloneContextReceipt(value)) != nil {
		return "sink_error"
	}
	return ""
}
func (s *ContextReceiptSession) Snapshot() ContextReceiptSnapshot {
	result := ContextReceiptSnapshot{Records: []ContextObservation{}, Issues: []string{}}
	if s == nil || !s.enabled {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.records {
		result.Records = append(result.Records, cloneContextReceipt(value))
	}
	for issue := range s.issues {
		result.Issues = append(result.Issues, issue)
	}
	if s.pending > 0 {
		result.Issues = append(result.Issues, "sink_pending")
	}
	sortContextSnapshot(&result)
	result.Complete = len(result.Records) > 0 && len(result.Issues) == 0
	return result
}

func contextCaptureBinding(value ContextObservation) error {
	for _, source := range value.Sources {
		if source.Scope != value.Run.Scope {
			return observationError("scope_mismatch", "$.sources[*].scope")
		}
		if source.ReadAtNS != nil && *source.ReadAtNS > value.ReadAtNS {
			return observationError("invalid_value", "$.sources[*].read_at_ns")
		}
	}
	if value.SummaryRef != nil && (value.SummaryRef.Collection != "crew_meetings" || value.Run.RunRef.Collection != "crew_meetings" || value.SummaryRef.ID != value.Run.RunRef.ID || value.SummaryRef.Revision > value.Run.RunRef.Revision) {
		return observationError("summary_binding", "$.summary_ref")
	}
	return nil
}
func sortContextSnapshot(result *ContextReceiptSnapshot) {
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].RecordSHA256 < result.Records[j].RecordSHA256 })
	sort.Strings(result.Issues)
}

func contextProtectedLineage(input ContextUseInput) bool {
	old := input.ProtectedLineage
	if old == nil || input.Previous == nil || ValidateContextObservation(*old) != nil || old.ContextSHA256 == nil || old.ContextStage == "source_read" {
		return false
	}
	if validateObservation(input.ExecutionRef) != nil || old.Run.RunRef.Collection != input.ExecutionRef.Collection || old.Run.RunRef.ID != input.ExecutionRef.ID {
		return false
	}
	if old.Run.Scope != input.Source.Scope || old.Run.Definition.Ref != input.TeamRef || old.Task != input.CurrentTask {
		return false
	}
	for _, source := range old.Sources {
		if reflect.DeepEqual(source, input.Previous.Observation) {
			return true
		}
	}
	return false
}

// MarkContextReceiptIncomplete records only fixed diagnostic codes. It never
// changes the authority decision or the successful business result.
func MarkContextReceiptIncomplete(ctx context.Context, reason string) {
	session := ContextReceiptSessionFrom(ctx)
	if session == nil || !session.enabled {
		return
	}
	session.note(normalizeContextReason(reason))
}
func normalizeContextReason(reason string) string {
	switch reason {
	case "", "read_unavailable", "read_denied", "read_error", "unsupported_collection", "missing_reader", "history_without_lineage", "invalid_source_receipt", "current_policy_unavailable", "current_policy_denied", "sink_error", "sink_panic", "context_observation_incomplete":
		return reason
	default:
		return "context_observation_incomplete"
	}
}

func contextKnownPrior(value ContextSourceState) bool {
	source := value.Observation
	return ValidateSourceObservation(source) == nil && source.ProvenanceStatus == "pinned" && source.ReadAuthorization == "confirmed" && source.ReadAtNS != nil && source.Revision != nil && source.ContentSHA256 != nil && validContextSHA(value.GuardSHA256)
}
