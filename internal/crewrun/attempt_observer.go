// Package crewrun observation records are compiled against the frozen research
// contract. They never load private contract files or change product JSON.
package crewrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const CrewObservationContractID = "arkism-crew-research-v2"
const CrewObservationContractSHA256 = "7780b97e9ec705bc32a0eff5c083d2a26966e9781f794da9c15b6d19db1f9b15"

// ObservationRef is the exact additive CanonicalRef projection.
type ObservationRef struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
}

// ObservationScope is the exact additive ScopeIdentity projection.
type ObservationScope struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	UserID      string `json:"user_id"`
	AgentID     string `json:"agent_id"`
}

// ObservationDefinition is the exact additive DefinitionIdentity projection.
type ObservationDefinition struct {
	Ref              ObservationRef `json:"ref"`
	SourceType       string         `json:"source_type"`
	DefinitionSHA256 *string        `json:"definition_sha256"`
	HashState        string         `json:"hash_state"`
}

// ObservationRun is the exact additive RunIdentity projection.
type ObservationRun struct {
	Scope               ObservationScope      `json:"scope"`
	RunRef              ObservationRef        `json:"run_ref"`
	RootRunID           *string               `json:"root_run_id"`
	ParentRunID         *string               `json:"parent_run_id"`
	ParentNodeID        *string               `json:"parent_node_id"`
	Definition          ObservationDefinition `json:"definition"`
	ExecutionPath       string                `json:"execution_path"`
	InvocationMode      string                `json:"invocation_mode"`
	ResultRevision      *int64                `json:"result_revision"`
	RootRunCollection   *string               `json:"root_run_collection"`
	ParentRunCollection *string               `json:"parent_run_collection"`
}

// ObservationTask is the exact additive TaskIdentity projection.
type ObservationTask struct {
	Namespace   string `json:"namespace"`
	TaskID      string `json:"task_id"`
	TaskVersion string `json:"task_version"`
	InputSHA256 string `json:"input_sha256"`
	SpecSHA256  string `json:"spec_sha256"`
}

// AttemptIdentity is the exact additive AttemptIdentity projection.
type AttemptIdentity struct {
	Run                   ObservationRun  `json:"run"`
	NodeID                string          `json:"node_id"`
	NodeAttempt           int64           `json:"node_attempt"`
	CallKey               string          `json:"call_key"`
	RetryIndex            int64           `json:"retry_index"`
	FallbackParentCallKey *string         `json:"fallback_parent_call_key"`
	AttemptRef            *ObservationRef `json:"attempt_ref"`
	IdentityBasis         string          `json:"identity_basis"`
	AttemptID             string          `json:"attempt_id"`
	ParentAttemptID       *string         `json:"parent_attempt_id"`
	Phase                 string          `json:"phase"`
	CanonicalAttemptID    *string         `json:"canonical_attempt_id"`
}

// UsageObservation is the exact additive UsageObservation projection.
type UsageObservation struct {
	InputTokens          *int64   `json:"input_tokens"`
	OutputTokens         *int64   `json:"output_tokens"`
	UsageBasis           string   `json:"usage_basis"`
	CostUSD              *float64 `json:"cost_usd"`
	CostBasis            string   `json:"cost_basis"`
	PricingRevision      *string  `json:"pricing_revision"`
	StartedAtNS          *int64   `json:"started_at_ns"`
	FinishedAtNS         *int64   `json:"finished_at_ns"`
	DurationNS           *int64   `json:"duration_ns"`
	DurationBasis        string   `json:"duration_basis"`
	TokenCeilingEnforced bool     `json:"token_ceiling_enforced"`
	InputBasis           string   `json:"input_basis"`
	OutputBasis          string   `json:"output_basis"`
	CacheReadTokens      *int64   `json:"cache_read_tokens"`
	CacheReadBasis       string   `json:"cache_read_basis"`
	CacheWriteTokens     *int64   `json:"cache_write_tokens"`
	CacheWriteBasis      string   `json:"cache_write_basis"`
	ReasoningTokens      *int64   `json:"reasoning_tokens"`
	ReasoningBasis       string   `json:"reasoning_basis"`
	ProviderUsageSchema  *string  `json:"provider_usage_schema"`
	TokenAccounting      string   `json:"token_accounting"`
}

// AttemptObservation is the exact additive AttemptObservation projection.
type AttemptObservation struct {
	ContractID          string           `json:"contract_id"`
	Identity            AttemptIdentity  `json:"identity"`
	Task                ObservationTask  `json:"task"`
	Status              string           `json:"status"`
	DispatchState       string           `json:"dispatch_state"`
	Usage               UsageObservation `json:"usage"`
	ErrorCode           string           `json:"error_code"`
	RecordSHA256        string           `json:"record_sha256"`
	ObservationRevision int64            `json:"observation_revision"`
	UsageRole           string           `json:"usage_role"`
	LeafCoverage        string           `json:"leaf_coverage"`
}

// RunTimingObservation is the exact additive RunTimingObservation projection.
type RunTimingObservation struct {
	Run                  ObservationRun `json:"run"`
	WallDurationNS       *int64         `json:"wall_duration_ns"`
	WallDurationBasis    string         `json:"wall_duration_basis"`
	SumCallDurationNS    *int64         `json:"sum_call_duration_ns"`
	SumCallDurationBasis string         `json:"sum_call_duration_basis"`
}

type observationRule struct {
	name                 string
	kind                 string
	minLength, maxLength int
	minimum, maximum     int64
	enums                []string
	pattern              string
	nullable             bool
}

// This is a closed compiled v2 projection table, not a caller-defined schema API.
var observationRules = map[reflect.Type][]observationRule{
	reflect.TypeOf(ObservationRef{}): {
		{name: "collection", kind: "string", minLength: 1, maxLength: 64},
		{name: "id", kind: "string", minLength: 1, maxLength: 256},
		{name: "revision", kind: "int64", minimum: 1, maximum: 9223372036854775807},
	},
	reflect.TypeOf(ObservationScope{}): {
		{name: "workspace_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "project_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "user_id", kind: "string", minLength: 0, maxLength: 256},
		{name: "agent_id", kind: "string", minLength: 0, maxLength: 256},
	},
	reflect.TypeOf(ObservationDefinition{}): {
		{name: "ref", kind: "object"},
		{name: "source_type", kind: "string", minLength: 1, maxLength: 80, enums: []string{"agent", "workflow", "team"}},
		{name: "definition_sha256", kind: "string", nullable: true, minLength: 64, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "hash_state", kind: "string", minLength: 1, maxLength: 80, enums: []string{"recorded", "not_recorded"}},
	},
	reflect.TypeOf(ObservationRun{}): {
		{name: "scope", kind: "object"},
		{name: "run_ref", kind: "object"},
		{name: "root_run_id", kind: "string", nullable: true, minLength: 1, maxLength: 256},
		{name: "parent_run_id", kind: "string", nullable: true, minLength: 1, maxLength: 256},
		{name: "parent_node_id", kind: "string", nullable: true, minLength: 1, maxLength: 256},
		{name: "definition", kind: "object"},
		{name: "execution_path", kind: "string", minLength: 1, maxLength: 80, enums: []string{"single", "workflow", "crew"}},
		{name: "invocation_mode", kind: "string", minLength: 1, maxLength: 80, enums: []string{"legacy", "flow"}},
		{name: "result_revision", kind: "int64", nullable: true, minimum: 1, maximum: 9223372036854775807},
		{name: "root_run_collection", kind: "string", nullable: true, minLength: 1, maxLength: 80, enums: []string{"crew_runs", "crew_meetings", "crew_assists"}},
		{name: "parent_run_collection", kind: "string", nullable: true, minLength: 1, maxLength: 80, enums: []string{"crew_runs", "crew_meetings", "crew_assists"}},
	},
	reflect.TypeOf(ObservationTask{}): {
		{name: "namespace", kind: "string", minLength: 1, maxLength: 128},
		{name: "task_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "task_version", kind: "string", minLength: 1, maxLength: 128},
		{name: "input_sha256", kind: "string", minLength: 64, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "spec_sha256", kind: "string", minLength: 64, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
	},
	reflect.TypeOf(AttemptIdentity{}): {
		{name: "run", kind: "object"},
		{name: "node_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "node_attempt", kind: "int64", minimum: 1, maximum: 10000},
		{name: "call_key", kind: "string", minLength: 1, maxLength: 256},
		{name: "retry_index", kind: "int64", minimum: 0, maximum: 10000},
		{name: "fallback_parent_call_key", kind: "string", nullable: true, minLength: 1, maxLength: 256},
		{name: "attempt_ref", kind: "object", nullable: true},
		{name: "identity_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"canonical_flow_receipt", "observed_legacy_call", "observed_host_leaf"}},
		{name: "attempt_id", kind: "string", minLength: 1, maxLength: 256, pattern: "^crw2-attempt-[0-9a-f]{64}$"},
		{name: "parent_attempt_id", kind: "string", nullable: true, minLength: 1, maxLength: 256, pattern: "^crw2-attempt-[0-9a-f]{64}$"},
		{name: "phase", kind: "string", minLength: 1, maxLength: 80, enums: []string{"main", "plan", "worker", "synthesis", "moderator", "scribe", "meeting_turn", "tool", "unknown"}},
		{name: "canonical_attempt_id", kind: "string", nullable: true, minLength: 1, maxLength: 256},
	},
	reflect.TypeOf(UsageObservation{}): {
		{name: "input_tokens", kind: "int64", nullable: true, minimum: 0, maximum: 1000000000},
		{name: "output_tokens", kind: "int64", nullable: true, minimum: 0, maximum: 1000000000},
		{name: "usage_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_reported", "estimated", "unknown"}},
		{name: "cost_usd", kind: "float64", nullable: true},
		{name: "cost_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_reported", "estimated", "unknown"}},
		{name: "pricing_revision", kind: "string", nullable: true, minLength: 1, maxLength: 128},
		{name: "started_at_ns", kind: "int64", nullable: true, minimum: 0, maximum: 9223372036854775807},
		{name: "finished_at_ns", kind: "int64", nullable: true, minimum: 0, maximum: 9223372036854775807},
		{name: "duration_ns", kind: "int64", nullable: true, minimum: 0, maximum: 9223372036854775807},
		{name: "duration_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"observed_monotonic", "estimated", "unknown"}},
		{name: "token_ceiling_enforced", kind: "bool"},
		{name: "input_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_reported", "estimated", "unknown"}},
		{name: "output_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_reported", "estimated", "unknown"}},
		{name: "cache_read_tokens", kind: "int64", nullable: true, minimum: 0, maximum: 1000000000},
		{name: "cache_read_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_reported", "estimated", "unknown"}},
		{name: "cache_write_tokens", kind: "int64", nullable: true, minimum: 0, maximum: 1000000000},
		{name: "cache_write_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_reported", "estimated", "unknown"}},
		{name: "reasoning_tokens", kind: "int64", nullable: true, minimum: 0, maximum: 1000000000},
		{name: "reasoning_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_reported", "estimated", "unknown"}},
		{name: "provider_usage_schema", kind: "string", nullable: true, minLength: 1, maxLength: 128},
		{name: "token_accounting", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_components_not_implicitly_additive"}},
	},
	reflect.TypeOf(AttemptObservation{}): {
		{name: "contract_id", kind: "string", minLength: 1, maxLength: 80, enums: []string{"arkism-crew-research-v2"}},
		{name: "identity", kind: "object"},
		{name: "task", kind: "object"},
		{name: "status", kind: "string", minLength: 1, maxLength: 80, enums: []string{"reserved", "succeeded", "failed", "timeout", "cancelled", "unknown", "cancelled_before_dispatch"}},
		{name: "dispatch_state", kind: "string", minLength: 1, maxLength: 80, enums: []string{"not_dispatched", "dispatched", "unknown"}},
		{name: "usage", kind: "object"},
		{name: "error_code", kind: "string", minLength: 0, maxLength: 128},
		{name: "record_sha256", kind: "string", minLength: 64, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "observation_revision", kind: "int64", minimum: 1, maximum: 9223372036854775807},
		{name: "usage_role", kind: "string", minLength: 1, maxLength: 80, enums: []string{"provider_leaf", "transport_aggregate", "opaque_dispatch"}},
		{name: "leaf_coverage", kind: "string", minLength: 1, maxLength: 80, enums: []string{"not_applicable", "complete", "partial", "unknown"}},
	},
	reflect.TypeOf(RunTimingObservation{}): {
		{name: "run", kind: "object"},
		{name: "wall_duration_ns", kind: "int64", nullable: true, minimum: 0, maximum: 9223372036854775807},
		{name: "wall_duration_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"observed_monotonic", "estimated", "unknown"}},
		{name: "sum_call_duration_ns", kind: "int64", nullable: true, minimum: 0, maximum: 9223372036854775807},
		{name: "sum_call_duration_basis", kind: "string", minLength: 1, maxLength: 80, enums: []string{"complete", "partial", "unknown"}},
	},
}

// ObservationError contains a fixed code and schema path, never input contents.
type ObservationError struct{ Code, Path string }

func (e *ObservationError) Error() string      { return e.Code + ": " + e.Path }
func observationError(code, path string) error { return &ObservationError{code, path} }

var observationHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var observationAttemptHash = regexp.MustCompile(`^crw2-attempt-[0-9a-f]{64}$`)
var observationErrorCodes = map[string]bool{
	"": true, "failed": true, "timeout": true, "cancelled": true, "cancelled_before_dispatch": true, "unknown": true,
	"unknown_error": true, "execution_error": true, "transport_error": true, "provider_error": true, "rate_limit": true,
	"quota_exhausted": true, "budget_refused": true, "capacity_refused": true, "usage_unknown": true, "settlement_failed": true,
	"ai_failed": true, "ai_output": true, "bad_config": true, "attempt_store_required": true, "attempt_replay_blocked": true,
	"flow_budget_limit": true, "flow_budget_unsupported": true, "flow_concurrency_limit": true, "flow_time_limit": true,
	"flow_legacy_run": true, "invalid_request": true, "observer_failed": true, "receipt_missing": true,
}

// NormalizeAttemptErrorCode never forwards arbitrary provider or error bodies.
func NormalizeAttemptErrorCode(code string) string {
	if observationErrorCodes[code] {
		return code
	}
	return "unknown_error"
}

func validateObservation(value any) error {
	return validateObservationValue(reflect.ValueOf(value), "$")
}
func validateObservationValue(value reflect.Value, path string) error {
	rules, ok := observationRules[value.Type()]
	if !ok {
		return observationError("invalid_type", path)
	}
	for i, rule := range rules {
		field := value.Field(i)
		at := path + "." + rule.name
		if rule.nullable {
			if field.IsNil() {
				continue
			}
			field = field.Elem()
		}
		if err := validateObservationField(field, rule, at); err != nil {
			return err
		}
	}
	return nil
}
func validateObservationField(value reflect.Value, rule observationRule, path string) error {
	switch rule.kind {
	case "strings":
		if value.IsNil() || value.Len() > 10000 {
			return observationError("invalid_value", path)
		}
		for i := 0; i < value.Len(); i++ {
			if !observationAttemptHash.MatchString(value.Index(i).String()) {
				return observationError("invalid_value", path+"[*]")
			}
		}
	case "object":
		return validateObservationValue(value, path)
	case "string":
		return validateObservationString(value.String(), rule, path)
	case "int64":
		if value.Int() < rule.minimum || value.Int() > rule.maximum {
			return observationError("invalid_value", path)
		}
	case "float64":
		if math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) || value.Float() < 0 {
			return observationError("invalid_value", path)
		}
	}
	return nil
}
func observationContains(values []string, value string) bool {
	for _, allowed := range values {
		if value == allowed {
			return true
		}
	}
	return false
}

// UnknownUsageObservation retains absence independently for every component.
func UnknownUsageObservation() UsageObservation {
	return UsageObservation{UsageBasis: "unknown", InputBasis: "unknown", OutputBasis: "unknown", CostBasis: "unknown",
		CacheReadBasis: "unknown", CacheWriteBasis: "unknown", ReasoningBasis: "unknown", DurationBasis: "unknown",
		TokenAccounting: "provider_components_not_implicitly_additive"}
}
func usageOverall(input, output string) string {
	if input == "unknown" || output == "unknown" {
		return "unknown"
	}
	if input == "provider_reported" && output == "provider_reported" {
		return "provider_reported"
	}
	return "estimated"
}

// ValidateUsageObservation checks component presence and basis without summing
// provider-specific cache or reasoning components into input/output totals.
func ValidateUsageObservation(usage UsageObservation) error {
	if err := validateObservation(usage); err != nil {
		return err
	}
	for _, part := range []struct {
		value       *int64
		basis, path string
	}{
		{usage.InputTokens, usage.InputBasis, "input_tokens"}, {usage.OutputTokens, usage.OutputBasis, "output_tokens"},
		{usage.CacheReadTokens, usage.CacheReadBasis, "cache_read_tokens"}, {usage.CacheWriteTokens, usage.CacheWriteBasis, "cache_write_tokens"},
		{usage.ReasoningTokens, usage.ReasoningBasis, "reasoning_tokens"},
	} {
		if (part.value == nil) != (part.basis == "unknown") {
			return observationError("invalid_value", "$."+part.path)
		}
	}
	if usage.UsageBasis != usageOverall(usage.InputBasis, usage.OutputBasis) {
		return observationError("invalid_value", "$.usage_basis")
	}
	if (usage.CostUSD == nil) != (usage.CostBasis == "unknown") {
		return observationError("invalid_value", "$.cost_usd")
	}
	if (usage.DurationNS == nil) != (usage.DurationBasis == "unknown") {
		return observationError("invalid_value", "$.duration_ns")
	}
	if usage.StartedAtNS != nil && usage.FinishedAtNS != nil && *usage.FinishedAtNS < *usage.StartedAtNS {
		return observationError("invalid_value", "$.finished_at_ns")
	}
	return nil
}
func validateObservationRun(run ObservationRun) error {
	if err := validateObservation(run); err != nil {
		return err
	}
	if !observationContains([]string{"crew_runs", "crew_meetings", "crew_assists"}, run.RunRef.Collection) || run.Definition.Ref.Collection != "crew" {
		return observationError("invalid_value", "$.run_ref.collection")
	}
	if (run.Definition.DefinitionSHA256 == nil) != (run.Definition.HashState == "not_recorded") {
		return observationError("invalid_value", "$.definition.definition_sha256")
	}
	if err := validateObservationExecutionKeys(run); err != nil {
		return err
	}
	return validateObservationRoute(run)
}
func validateObservationExecutionKeys(run ObservationRun) error {
	if (run.RootRunID == nil) != (run.RootRunCollection == nil) || (run.ParentRunID == nil) != (run.ParentRunCollection == nil) {
		return observationError("unpaired_execution_key", "$.root_run_collection")
	}
	if run.ParentRunID != nil && *run.ParentRunID == run.RunRef.ID && *run.ParentRunCollection == run.RunRef.Collection {
		return observationError("parent_cycle", "$.parent_run_id")
	}
	if run.RootRunID != nil {
		self := *run.RootRunID == run.RunRef.ID && *run.RootRunCollection == run.RunRef.Collection
		if self && run.ParentRunID != nil {
			return observationError("parent_cycle", "$.parent_run_id")
		}
		if !self && run.ParentRunID == nil {
			return observationError("missing_parent", "$.parent_run_id")
		}
	}
	return nil
}
func validateObservationRoute(run ObservationRun) error {
	source, path := run.Definition.SourceType, run.ExecutionPath
	valid := false
	switch run.RunRef.Collection {
	case "crew_runs":
		valid = map[string]string{"agent": "single", "workflow": "workflow", "team": "crew"}[source] == path && (source == "workflow" || run.InvocationMode == "flow")
	case "crew_meetings":
		valid = source == "team" && path == "crew" && (run.InvocationMode != "flow" || (run.ParentRunCollection != nil && *run.ParentRunCollection == "crew_runs"))
	case "crew_assists":
		valid = source == "agent" && path == "single" && run.InvocationMode == "legacy"
	}
	if !valid {
		return observationError("execution_route_mismatch", "$.execution_path")
	}
	if run.RunRef.Collection != "crew_runs" && run.ResultRevision != nil {
		return observationError("result_revision_domain", "$.result_revision")
	}
	return nil
}

func validateAttemptIdentity(identity AttemptIdentity) error {
	if identity.AttemptRef != nil && identity.AttemptRef.Collection != "jobs" {
		return observationError("invalid_value", "$.attempt_ref.collection")
	}
	if err := validateObservation(identity); err != nil {
		return err
	}
	if err := validateObservationRun(identity.Run); err != nil {
		return err
	}
	if identity.Run.RootRunID == nil {
		return observationError("identity_unavailable", "$.run.root_run_id")
	}
	if identity.IdentityBasis == "canonical_flow_receipt" && identity.Run.InvocationMode != "flow" {
		return observationError("identity_basis_mismatch", "$.identity_basis")
	}
	if identity.IdentityBasis == "observed_host_leaf" && (identity.ParentAttemptID == nil || identity.CanonicalAttemptID != nil || identity.AttemptRef != nil) {
		return observationError("invalid_value", "$.canonical_attempt_id")
	}
	if identity.IdentityBasis == "canonical_flow_receipt" && identity.CanonicalAttemptID == nil {
		return observationError("invalid_value", "$.canonical_attempt_id")
	}
	if identity.AttemptRef != nil && identity.CanonicalAttemptID != nil && identity.AttemptRef.ID != *identity.CanonicalAttemptID {
		return observationError("invalid_value", "$.attempt_ref")
	}
	return nil
}

// StableAttemptID uses exactly the frozen 16-field typed canonical array. Product
// CAS revisions, usage and observer revisions intentionally do not alter it.
func StableAttemptID(identity AttemptIdentity) (string, error) {
	checked := identity
	checked.AttemptID = "crw2-attempt-" + strings.Repeat("0", 64)
	if err := validateAttemptIdentity(checked); err != nil {
		return "", err
	}
	values := []any{CrewObservationContractID, identity.Run.Scope, identity.Run.RootRunCollection, identity.Run.RootRunID, identity.Run.RunRef.Collection, identity.Run.RunRef.ID,
		identity.Run.Definition.Ref.Collection, identity.Run.Definition.Ref.ID, identity.Run.Definition.Ref.Revision, identity.NodeID, identity.NodeAttempt,
		identity.Phase, identity.CallKey, identity.RetryIndex, identity.ParentAttemptID, identity.FallbackParentCallKey}
	var encoded bytes.Buffer
	encoded.WriteString("a16[")
	for _, value := range values {
		if err := encodeObservation(&encoded, reflect.ValueOf(value), false); err != nil {
			return "", err
		}
	}
	encoded.WriteByte(']')
	sum := sha256.Sum256(encoded.Bytes())
	return "crw2-attempt-" + hex.EncodeToString(sum[:]), nil
}
func encodeObservation(out *bytes.Buffer, value reflect.Value, omitHash bool) error {
	if !value.IsValid() {
		out.WriteByte('n')
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			out.WriteByte('n')
			return nil
		}
		return encodeObservation(out, value.Elem(), omitHash)
	}
	switch value.Kind() {
	case reflect.String:
		fmt.Fprintf(out, "s%d:%s", len(value.String()), value.String())
	case reflect.Int64:
		fmt.Fprintf(out, "i%d;", value.Int())
	case reflect.Float64:
		number := value.Float()
		if number == 0 {
			number = 0
		}
		fmt.Fprintf(out, "d%016x;", math.Float64bits(number))
	case reflect.Bool:
		if value.Bool() {
			out.WriteByte('t')
		} else {
			out.WriteByte('f')
		}
	case reflect.Slice:
		if value.IsNil() {
			return observationError("invalid_type", "$")
		}
		fmt.Fprintf(out, "a%d[", value.Len())
		for i := 0; i < value.Len(); i++ {
			if err := encodeObservation(out, value.Index(i), false); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case reflect.Struct:
		return encodeObservationObject(out, value, omitHash)
	default:
		return observationError("invalid_type", "$")
	}
	return nil
}
func encodeObservationObject(out *bytes.Buffer, value reflect.Value, omitHash bool) error {
	rules, ok := observationRules[value.Type()]
	if !ok {
		return observationError("invalid_type", "$")
	}
	fields := make([]int, 0, len(rules))
	for i, rule := range rules {
		if !omitHash || rule.name != "record_sha256" {
			fields = append(fields, i)
		}
	}
	sort.Slice(fields, func(i, j int) bool { return rules[fields[i]].name < rules[fields[j]].name })
	fmt.Fprintf(out, "o%d{", len(fields))
	for _, index := range fields {
		if err := encodeObservation(out, reflect.ValueOf(rules[index].name), false); err != nil {
			return err
		}
		if err := encodeObservation(out, value.Field(index), false); err != nil {
			return err
		}
	}
	out.WriteByte('}')
	return nil
}
func observationRecordHash(record AttemptObservation) (string, error) {
	var raw bytes.Buffer
	if err := encodeObservation(&raw, reflect.ValueOf(record), true); err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw.Bytes())
	return hex.EncodeToString(hash[:]), nil
}

// SealAttemptObservation constructs observer identity and hash on a value copy.
// It does not certify a caller-supplied record hash; use Validate for that.
func SealAttemptObservation(record AttemptObservation) (AttemptObservation, error) {
	id, err := StableAttemptID(record.Identity)
	if err != nil {
		return AttemptObservation{}, err
	}
	if record.Identity.AttemptID != "" && record.Identity.AttemptID != id {
		return AttemptObservation{}, observationError("invalid_value", "$.identity.attempt_id")
	}
	record.Identity.AttemptID = id
	record.RecordSHA256 = strings.Repeat("0", 64)
	if err := validateAttemptObservationFields(record); err != nil {
		return AttemptObservation{}, err
	}
	record.RecordSHA256, err = observationRecordHash(record)
	return cloneObservation(record), err
}
func validateAttemptObservationFields(record AttemptObservation) error {
	if err := validateObservation(record); err != nil {
		return err
	}
	if err := validateAttemptIdentity(record.Identity); err != nil {
		return err
	}
	if err := ValidateUsageObservation(record.Usage); err != nil {
		return err
	}
	if err := validateObservationRole(record); err != nil {
		return err
	}
	if !observationErrorCodes[record.ErrorCode] {
		return observationError("invalid_value", "$.error_code")
	}
	if record.DispatchState == "not_dispatched" && hasReportedCharge(record.Usage) {
		return observationError("invalid_value", "$.dispatch_state")
	}
	if record.Status == "succeeded" && record.DispatchState != "dispatched" {
		return observationError("invalid_value", "$.dispatch_state")
	}
	if record.Status == "cancelled_before_dispatch" && record.DispatchState != "not_dispatched" {
		return observationError("invalid_value", "$.dispatch_state")
	}
	return nil
}

// A provider-reported positive amount proves activity and cannot be discarded
// as a free pre-dispatch event. Estimated reservation quantities and reported
// zeros remain valid; observed duration can include time spent waiting.
func hasReportedCharge(usage UsageObservation) bool {
	for _, component := range []struct {
		value *int64
		basis string
	}{
		{usage.InputTokens, usage.InputBasis}, {usage.OutputTokens, usage.OutputBasis},
		{usage.CacheReadTokens, usage.CacheReadBasis}, {usage.CacheWriteTokens, usage.CacheWriteBasis},
		{usage.ReasoningTokens, usage.ReasoningBasis},
	} {
		if component.basis == "provider_reported" && component.value != nil && *component.value > 0 {
			return true
		}
	}
	return usage.CostBasis == "provider_reported" && usage.CostUSD != nil && *usage.CostUSD > 0
}

// ValidateAttemptObservation verifies the exact compiled fields and record hash.
func ValidateAttemptObservation(record AttemptObservation) error {
	if err := validateAttemptObservationFields(record); err != nil {
		return err
	}
	id, err := StableAttemptID(record.Identity)
	if err != nil {
		return err
	}
	if record.Identity.AttemptID != id {
		return observationError("invalid_value", "$.identity.attempt_id")
	}
	hash, err := observationRecordHash(record)
	if err != nil {
		return err
	}
	if hash != record.RecordSHA256 {
		return observationError("hash_mismatch", "$.record_sha256")
	}
	return nil
}
func cloneObservationValue(value reflect.Value) reflect.Value {
	result := reflect.New(value.Type()).Elem()
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			result.Set(reflect.New(value.Type().Elem()))
			result.Elem().Set(cloneObservationValue(value.Elem()))
		}
	case reflect.Slice:
		if !value.IsNil() {
			result = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			for i := 0; i < value.Len(); i++ {
				result.Index(i).Set(cloneObservationValue(value.Index(i)))
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			result.Field(i).Set(cloneObservationValue(value.Field(i)))
		}
	default:
		result.Set(value)
	}
	return result
}
func cloneObservation(record AttemptObservation) AttemptObservation {
	return cloneObservationValue(reflect.ValueOf(record)).Interface().(AttemptObservation)
}

// AttemptContext supplies observation identity independently from FLOW scope.
// It can therefore describe legacy calls without claiming a durable receipt.
type AttemptContext struct {
	Run                   ObservationRun
	Task                  ObservationTask
	NodeID                string
	NodeAttempt           int64
	ParentAttemptID       *string
	FallbackParentCallKey *string
	IdentityBasis         string
}
type attemptObserverKey struct{}
type attemptContextKey struct{}

func WithAttemptObserver(ctx context.Context, observer *AttemptObserver) context.Context {
	return context.WithValue(ctx, attemptObserverKey{}, observer)
}
func AttemptObserverEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	observer, _ := ctx.Value(attemptObserverKey{}).(*AttemptObserver)
	return observer != nil && observer.enabled
}
func WithAttemptContext(ctx context.Context, scope AttemptContext) context.Context {
	return context.WithValue(ctx, attemptContextKey{}, cloneObservationValue(reflect.ValueOf(scope)).Interface().(AttemptContext))
}
func AttemptContextFrom(ctx context.Context) (AttemptContext, bool) {
	if ctx == nil {
		return AttemptContext{}, false
	}
	scope, ok := ctx.Value(attemptContextKey{}).(AttemptContext)
	if !ok {
		return scope, false
	}
	return cloneObservationValue(reflect.ValueOf(scope)).Interface().(AttemptContext), true
}
func AttemptIdentityFromContext(ctx context.Context, phase, callKey string, retryIndex int64, canonicalAttemptID *string) (AttemptIdentity, error) {
	scope, ok := AttemptContextFrom(ctx)
	if !ok {
		return AttemptIdentity{}, observationError("identity_unavailable", "$")
	}
	if err := validateObservation(scope.Task); err != nil {
		return AttemptIdentity{}, err
	}
	identity := AttemptIdentity{Run: scope.Run, NodeID: scope.NodeID, NodeAttempt: scope.NodeAttempt, CallKey: callKey,
		RetryIndex: retryIndex, Phase: phase, ParentAttemptID: scope.ParentAttemptID, FallbackParentCallKey: scope.FallbackParentCallKey,
		CanonicalAttemptID: canonicalAttemptID, IdentityBasis: "observed_legacy_call"}
	if scope.IdentityBasis != "" {
		identity.IdentityBasis = scope.IdentityBasis
	}
	if canonicalAttemptID != nil {
		identity.IdentityBasis = "canonical_flow_receipt"
	}
	id, err := StableAttemptID(identity)
	if err != nil {
		return AttemptIdentity{}, err
	}
	identity.AttemptID = id
	return cloneObservationValue(reflect.ValueOf(identity)).Interface().(AttemptIdentity), nil
}

var observationReasons = map[string]bool{
	"trace_missing": true, "trace_store_failed": true, "trace_receipt_invalid": true, "sink_failed": true,
	"observer_sink_failed": true, "observer_sink_panic": true, "usage_invalid": true, "identity_unavailable": true,
	"host_attempts_incomplete": true, "invalid_observation": true, "observation_conflict": true,
	"observation_transition": true, "invalid_incomplete_reason": true,
}

// MarkAttemptObservationIncomplete records only allowlisted reason codes. It
// never changes the work result, performs I/O, or grants a retry/effect permit.
func MarkAttemptObservationIncomplete(ctx context.Context, reason string) {
	if !AttemptObserverEnabled(ctx) {
		return
	}
	observer, _ := ctx.Value(attemptObserverKey{}).(*AttemptObserver)
	if !observationReasons[reason] {
		reason = "invalid_incomplete_reason"
	}
	observer.note(reason)
}

// RecordAttempt seals one value and reports failures through the observer's
// completeness state. Callers must preserve the original business result.
func RecordAttempt(ctx context.Context, record AttemptObservation) {
	if !AttemptObserverEnabled(ctx) {
		return
	}
	observer, _ := ctx.Value(attemptObserverKey{}).(*AttemptObserver)
	sealed, err := SealAttemptObservation(record)
	if err != nil {
		observer.note("invalid_observation")
		return
	}
	observer.Observe(sealed)
}

// AttemptObserver is opt-in and off as a nil pointer or zero value. A collector
// is not a model runner, durable FLOW ledger, or final-node trace projection.
type AttemptObserver struct {
	mu        sync.Mutex
	enabled   bool
	sink      func(AttemptObservation) error
	records   map[string]map[int64]AttemptObservation
	issues    map[string]bool
	pending   int64
	canonical map[string]string
}

// NewAttemptObserver explicitly enables collection. A nil sink means in-memory
// collection only; it makes no external delivery or durability guarantee.
// A nonnil sink runs synchronously and must be a short, bounded local callback.
// Blocking/arbitrary I/O sinks can block the caller; latency independence and
// external delivery are not promised. Caller-owned flushing is a separate gate.
func NewAttemptObserver(sink func(AttemptObservation) error) *AttemptObserver {
	return &AttemptObserver{enabled: true, sink: sink, records: map[string]map[int64]AttemptObservation{}, issues: map[string]bool{}, canonical: map[string]string{}}
}
func (o *AttemptObserver) note(reason string) {
	if o == nil || !o.enabled {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.issues[reason] = true
}
func observationTerminal(status string) bool { return status != "reserved" }
func observationLineage(record AttemptObservation) []byte {
	identity := record.Identity
	identity.Run.RunRef.Revision = 1
	identity.Run.ResultRevision = nil
	identity.AttemptRef = nil
	identity.CanonicalAttemptID = nil
	var out bytes.Buffer
	_ = encodeObservation(&out, reflect.ValueOf(identity), false)
	_ = encodeObservation(&out, reflect.ValueOf(record.Task), false)
	_ = encodeObservation(&out, reflect.ValueOf(record.UsageRole), false)
	return out.Bytes()
}
func observationBasisRank(basis string) int {
	switch basis {
	case "provider_reported", "observed_monotonic":
		return 2
	case "estimated":
		return 1
	default:
		return 0
	}
}
func monotonicObservationComponent(before, after *int64, beforeBasis, afterBasis string) bool {
	prior, next := observationBasisRank(beforeBasis), observationBasisRank(afterBasis)
	if next < prior {
		return false
	}
	return !(prior == 2 && next == 2 && before != nil && after != nil && *after != *before)
}
func monotonicObservationUsage(before, after UsageObservation) bool {
	for _, component := range []struct {
		before, after           *int64
		beforeBasis, afterBasis string
	}{
		{before.InputTokens, after.InputTokens, before.InputBasis, after.InputBasis},
		{before.OutputTokens, after.OutputTokens, before.OutputBasis, after.OutputBasis},
		{before.CacheReadTokens, after.CacheReadTokens, before.CacheReadBasis, after.CacheReadBasis},
		{before.CacheWriteTokens, after.CacheWriteTokens, before.CacheWriteBasis, after.CacheWriteBasis},
		{before.ReasoningTokens, after.ReasoningTokens, before.ReasoningBasis, after.ReasoningBasis},
		{before.DurationNS, after.DurationNS, before.DurationBasis, after.DurationBasis},
	} {
		if !monotonicObservationComponent(component.before, component.after, component.beforeBasis, component.afterBasis) {
			return false
		}
	}
	prior, next := observationBasisRank(before.CostBasis), observationBasisRank(after.CostBasis)
	if next < prior {
		return false
	}
	return !(prior == 2 && next == 2 && before.CostUSD != nil && after.CostUSD != nil && *after.CostUSD != *before.CostUSD)
}
func monotonicObservation(before, after AttemptObservation) bool {
	if after.Identity.Run.RunRef.Revision < before.Identity.Run.RunRef.Revision {
		return false
	}
	if before.Identity.AttemptRef != nil && (after.Identity.AttemptRef == nil || !matchingEvidenceRef(*before.Identity.AttemptRef, *after.Identity.AttemptRef)) {
		return false
	}
	if observationTerminal(before.Status) && before.DispatchState != "unknown" && before.DispatchState != after.DispatchState {
		return false
	}
	if observationTerminal(before.Status) && after.Status != before.Status {
		return false
	}
	if before.DispatchState == "dispatched" && after.DispatchState != "dispatched" {
		return false
	}
	if before.DispatchState == "unknown" && after.DispatchState == "not_dispatched" {
		return false
	}
	if before.Identity.CanonicalAttemptID != nil && (after.Identity.CanonicalAttemptID == nil || *before.Identity.CanonicalAttemptID != *after.Identity.CanonicalAttemptID) {
		return false
	}
	return monotonicObservationUsage(before.Usage, after.Usage)
}
func validObservationTransition(records map[int64]AttemptObservation, incoming AttemptObservation) bool {
	revisions := make([]int64, 0, len(records)+1)
	for revision, record := range records {
		if !bytes.Equal(observationLineage(record), observationLineage(incoming)) {
			return false
		}
		revisions = append(revisions, revision)
	}
	revisions = append(revisions, incoming.ObservationRevision)
	sort.Slice(revisions, func(i, j int) bool { return revisions[i] < revisions[j] })
	var previous AttemptObservation
	seen := false
	for _, revision := range revisions {
		record := incoming
		if revision != incoming.ObservationRevision {
			record = records[revision]
		}
		if seen && !monotonicObservation(previous, record) {
			return false
		}
		previous = record
		seen = true
	}
	return true
}

// Observe accepts sealed records. Exact ID+observer revision+hash replay is a
// no-op, including delivery; changed timing is not silently removed from hashes.
// Fresh collectors may compare schedule/usage separately from observed timing.
func (o *AttemptObserver) Observe(record AttemptObservation) {
	if o == nil || !o.enabled {
		return
	}
	if err := ValidateAttemptObservation(record); err != nil {
		o.note("invalid_observation")
		return
	}
	record = cloneObservation(record)
	o.mu.Lock()
	id := record.Identity.AttemptID
	versions := o.records[id]
	canonicalKey := ""
	if record.Identity.CanonicalAttemptID != nil {
		var key bytes.Buffer
		_ = encodeObservation(&key, reflect.ValueOf(record.Identity.Run.Scope), false)
		_ = encodeObservation(&key, reflect.ValueOf(*record.Identity.CanonicalAttemptID), false)
		canonicalKey = key.String()
		if prior, ok := o.canonical[canonicalKey]; ok && prior != id {
			o.issues["canonical_attempt_conflict"] = true
			o.mu.Unlock()
			return
		}
	}
	if prior, ok := versions[record.ObservationRevision]; ok {
		if prior.RecordSHA256 != record.RecordSHA256 {
			o.issues["observation_conflict"] = true
		}
		o.mu.Unlock()
		return
	}
	if !validObservationTransition(versions, record) {
		o.issues["observation_transition"] = true
		o.mu.Unlock()
		return
	}
	if versions == nil {
		versions = map[int64]AttemptObservation{}
		o.records[id] = versions
	}
	versions[record.ObservationRevision] = record
	if canonicalKey != "" {
		o.canonical[canonicalKey] = id
	}
	sink := o.sink
	if sink != nil {
		o.pending++
	}
	o.mu.Unlock()
	if sink != nil {
		reason := deliverObservation(sink, cloneObservation(record))
		o.mu.Lock()
		o.pending--
		if reason != "" {
			o.issues[reason] = true
		}
		o.mu.Unlock()
	}
}
func deliverObservation(sink func(AttemptObservation) error, record AttemptObservation) (reason string) {
	defer func() {
		if recover() != nil {
			reason = "observer_sink_panic"
		}
	}()
	if sink(record) != nil {
		return "observer_sink_failed"
	}
	return ""
}

// ComponentTotal never adds overlapping token components together. Value is
// absent when any selected call lacks this component. KnownSum can include
// estimates and is not a measured paid lower bound; ProviderReportedSum keeps
// the reported subset separate. Components are not implicitly additive.
type ComponentTotal struct {
	KnownSum            int64
	ProviderReportedSum int64
	EstimatedSum        int64
	UnknownCalls        int64
	Overflow            bool
	Value               *int64
}
type CostTotal struct {
	KnownSum            float64
	ProviderReportedSum float64
	EstimatedSum        float64
	UnknownCalls        int64
	Overflow            bool
	Value               *float64
}
type ObserverTotals struct {
	LogicalAttempts                                               int64
	ReservedOnly                                                  int64
	DispatchedAttempts                                            int64
	UnknownDispatchAttempts                                       int64
	CancelledBeforeDispatch                                       int64
	StatusCounts                                                  map[string]int64
	Input, Output, CacheRead, CacheWrite, Reasoning, CallDuration ComponentTotal
	Cost                                                          CostTotal
}
type AttemptLedgerSnapshot struct {
	Enabled              bool
	Complete             bool
	EvidenceKind         string
	Records              []AttemptObservation
	Latest               []AttemptObservation
	Issues               []string
	Totals               ObserverTotals // compatibility view: observed provider leaves only
	ProviderLeafTotals   ObserverTotals // explicit name of the Totals compatibility scope
	AggregateTotals      ObserverTotals
	ReservationEstimates CostTotal
	RuntimeAuthenticated bool
	RunCoverage          string
}

func componentAdd(total *ComponentTotal, value *int64, basis string) {
	if value == nil {
		total.UnknownCalls++
		return
	}
	if *value > math.MaxInt64-total.KnownSum {
		total.Overflow = true
		return
	}
	total.KnownSum += *value
	if basis == "provider_reported" {
		total.ProviderReportedSum += *value
	} else if basis == "estimated" {
		total.EstimatedSum += *value
	}
}
func componentFinish(total *ComponentTotal) {
	if total.UnknownCalls == 0 && !total.Overflow {
		value := total.KnownSum
		total.Value = &value
	}
}
func observerAddUsage(totals *ObserverTotals, usage UsageObservation) {
	componentAdd(&totals.Input, usage.InputTokens, usage.InputBasis)
	componentAdd(&totals.Output, usage.OutputTokens, usage.OutputBasis)
	componentAdd(&totals.CacheRead, usage.CacheReadTokens, usage.CacheReadBasis)
	componentAdd(&totals.CacheWrite, usage.CacheWriteTokens, usage.CacheWriteBasis)
	componentAdd(&totals.Reasoning, usage.ReasoningTokens, usage.ReasoningBasis)
	if usage.CostUSD == nil {
		totals.Cost.UnknownCalls++
	} else if math.IsInf(totals.Cost.KnownSum+*usage.CostUSD, 0) {
		totals.Cost.Overflow = true
	} else {
		totals.Cost.KnownSum += *usage.CostUSD
		if usage.CostBasis == "provider_reported" {
			totals.Cost.ProviderReportedSum += *usage.CostUSD
		} else {
			totals.Cost.EstimatedSum += *usage.CostUSD
		}
	}
	componentAdd(&totals.CallDuration, usage.DurationNS, usage.DurationBasis)
}
func observerFinishTotals(totals *ObserverTotals, issues map[string]bool) {
	for _, total := range []*ComponentTotal{&totals.Input, &totals.Output, &totals.CacheRead, &totals.CacheWrite, &totals.Reasoning, &totals.CallDuration} {
		componentFinish(total)
		if total.Overflow {
			issues["accounting_overflow"] = true
		}
	}
	if totals.Cost.UnknownCalls == 0 && !totals.Cost.Overflow {
		value := totals.Cost.KnownSum
		totals.Cost.Value = &value
	}
	if totals.Cost.Overflow {
		issues["accounting_overflow"] = true
	}
}
func observationRoot(run ObservationRun) string {
	if run.RootRunID == nil || run.RootRunCollection == nil {
		return ""
	}
	return *run.RootRunCollection + "/" + *run.RootRunID
}

func observerParentIssues(latest map[string]AttemptObservation, issues map[string]bool) {
	for id, record := range latest {
		parent := record.Identity.ParentAttemptID
		if parent != nil {
			issues["parent_evidence_unverified"] = true
		}
		if parent == nil {
			continue
		}
		seen := map[string]bool{id: true}
		cursor := parent
		for cursor != nil {
			if seen[*cursor] {
				issues["parent_cycle"] = true
				break
			}
			seen[*cursor] = true
			found, ok := latest[*cursor]
			if !ok {
				issues["missing_parent"] = true
				break
			}
			if found.Task != record.Task || observationRoot(found.Identity.Run) != observationRoot(record.Identity.Run) || found.Identity.Run.Scope != record.Identity.Run.Scope {
				issues["parent_lineage_mismatch"] = true
				break
			}
			cursor = found.Identity.ParentAttemptID
		}
	}
}

// Snapshot retains every accepted observer revision, sorted deterministically.
// Totals select one latest observation per logical call, not reservation+settle.
func (o *AttemptObserver) Snapshot() AttemptLedgerSnapshot {
	result := AttemptLedgerSnapshot{EvidenceKind: "per_attempt_ledger", RunCoverage: "unknown", Records: []AttemptObservation{}, Latest: []AttemptObservation{}, Issues: []string{}, Totals: ObserverTotals{StatusCounts: map[string]int64{}}}
	if o == nil || !o.enabled {
		return result
	}
	result.Enabled = true
	o.mu.Lock()
	issues := map[string]bool{}
	if o.pending > 0 {
		issues["sink_pending"] = true
	}
	for issue := range o.issues {
		issues[issue] = true
	}
	latest := map[string]AttemptObservation{}
	ids := make([]string, 0, len(o.records))
	for id := range o.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		versions := o.records[id]
		revisions := make([]int64, 0, len(versions))
		for revision := range versions {
			revisions = append(revisions, revision)
		}
		sort.Slice(revisions, func(i, j int) bool { return revisions[i] < revisions[j] })
		for i, revision := range revisions {
			if revision != int64(i+1) {
				issues["observation_gap"] = true
			}
			result.Records = append(result.Records, cloneObservation(versions[revision]))
		}
		record := cloneObservation(versions[revisions[len(revisions)-1]])
		latest[id] = record
		result.Latest = append(result.Latest, record)
	}
	o.mu.Unlock()
	observerParentIssues(latest, issues)
	result.AggregateTotals.StatusCounts = map[string]int64{}
	for _, record := range result.Latest {
		if record.DispatchState == "not_dispatched" && record.Usage.CostBasis == "estimated" && record.Usage.CostUSD != nil {
			reservationUsage := UnknownUsageObservation()
			reservationUsage.CostUSD = record.Usage.CostUSD
			reservationUsage.CostBasis = "estimated"
			temp := ObserverTotals{}
			temp.Cost = result.ReservationEstimates
			observerAddUsage(&temp, reservationUsage)
			result.ReservationEstimates = temp.Cost
		}
		if record.UsageRole == "provider_leaf" {
			observerCountLatest(&result.Totals, record, issues)
		} else {
			observerCountLatest(&result.AggregateTotals, record, issues)
			issues["leaf_coverage_unverified"] = true
		}
	}
	observerFinishTotals(&result.AggregateTotals, issues)
	if result.AggregateTotals.DispatchedAttempts+result.AggregateTotals.UnknownDispatchAttempts == 0 {
		clearObservationValues(&result.AggregateTotals)
	}

	observerFinishTotals(&result.Totals, issues)
	if result.Totals.DispatchedAttempts+result.Totals.UnknownDispatchAttempts == 0 {
		clearObservationValues(&result.Totals)
	}
	if len(result.Latest) == 0 {
		issues["no_observations"] = true
		for _, total := range []*ComponentTotal{&result.Totals.Input, &result.Totals.Output, &result.Totals.CacheRead, &result.Totals.CacheWrite, &result.Totals.Reasoning, &result.Totals.CallDuration} {
			total.Value = nil
		}
		result.Totals.Cost.Value = nil
	}
	for issue := range issues {
		result.Issues = append(result.Issues, issue)
	}
	sort.Strings(result.Issues)
	result.ProviderLeafTotals = result.Totals
	result.Complete = len(issues) == 0
	return result
}
func observerCountLatest(totals *ObserverTotals, record AttemptObservation, issues map[string]bool) {
	totals.LogicalAttempts++
	totals.StatusCounts[record.Status]++
	if record.Status == "reserved" {
		totals.ReservedOnly++
		issues["pending_attempt"] = true
		return
	}
	if record.Status == "cancelled_before_dispatch" {
		totals.CancelledBeforeDispatch++
		return
	}
	if record.DispatchState == "unknown" {
		totals.UnknownDispatchAttempts++
		issues["unknown_dispatch"] = true
		observerAddUsage(totals, record.Usage)
		return
	}
	if record.DispatchState == "not_dispatched" {
		return
	}
	totals.DispatchedAttempts++
	observerAddUsage(totals, record.Usage)
	if record.Usage.UsageBasis == "unknown" || record.Usage.CacheReadBasis == "unknown" || record.Usage.CacheWriteBasis == "unknown" || record.Usage.ReasoningBasis == "unknown" {
		issues["unknown_usage"] = true
	}
	if record.Usage.CostBasis == "unknown" {
		issues["unknown_cost"] = true
	}
	if record.Usage.DurationBasis == "unknown" {
		issues["unknown_duration"] = true
	}
}

// Timing keeps one logical run's wall duration separate from that run's call
// sum. Other runs, including child runs, are not silently folded into it. A
// caller needing a root-wide measure must aggregate that declared scope itself.
func (o *AttemptObserver) Timing(run ObservationRun, wall *int64, basis string) (RunTimingObservation, error) {
	out := RunTimingObservation{Run: run, WallDurationNS: wall, WallDurationBasis: basis, SumCallDurationBasis: "unknown"}
	if err := validateObservation(out); err != nil {
		return RunTimingObservation{}, err
	}
	if err := validateObservationRun(run); err != nil {
		return RunTimingObservation{}, err
	}
	if (wall == nil) != (basis == "unknown") {
		return RunTimingObservation{}, observationError("invalid_value", "$.wall_duration_ns")
	}
	totals := ObserverTotals{StatusCounts: map[string]int64{}}
	issues := map[string]bool{}
	selected := 0
	sumBasis := "complete"
	for _, record := range o.Snapshot().Latest {
		if record.UsageRole != "provider_leaf" || !sameObservationRun(record.Identity.Run, run) {
			continue
		}
		selected++
		observerCountLatest(&totals, record, issues)
		if record.DispatchState == "dispatched" && record.Usage.DurationBasis == "estimated" {
			sumBasis = "partial"
		}
	}
	observerFinishTotals(&totals, issues)
	if selected > 0 && totals.UnknownDispatchAttempts == 0 && totals.ReservedOnly == 0 && totals.CallDuration.Value != nil {
		value := *totals.CallDuration.Value
		out.SumCallDurationNS = &value
		out.SumCallDurationBasis = sumBasis
	}
	return cloneObservationValue(reflect.ValueOf(out)).Interface().(RunTimingObservation), nil
}
func sameObservationRun(a, b ObservationRun) bool {
	a.RunRef.Revision = 1
	b.RunRef.Revision = 1
	a.ResultRevision = nil
	b.ResultRevision = nil
	var left, right bytes.Buffer
	_ = encodeObservation(&left, reflect.ValueOf(a), false)
	_ = encodeObservation(&right, reflect.ValueOf(b), false)
	return bytes.Equal(left.Bytes(), right.Bytes())
}

// Closed additive evidence DTOs. Pins verify local bytes, never sender authentication.
type ObservationDispatchClosure struct {
	ContractID          string           `json:"contract_id"`
	Scope               ObservationScope `json:"scope"`
	Task                ObservationTask  `json:"task"`
	RootRunCollection   string           `json:"root_run_collection"`
	RootRunID           string           `json:"root_run_id"`
	ParentAttemptID     string           `json:"parent_attempt_id"`
	SenderBoundaryID    string           `json:"sender_boundary_id"`
	MemberAttemptIDs    []string         `json:"member_attempt_ids"`
	MembershipSHA256    string           `json:"membership_sha256"`
	SenderJournalSHA256 string           `json:"sender_journal_sha256"`
	RecordSHA256        string           `json:"record_sha256"`
}
type ObservationSenderJournal struct {
	ContractID         string           `json:"contract_id"`
	Scope              ObservationScope `json:"scope"`
	Task               ObservationTask  `json:"task"`
	RootRunCollection  string           `json:"root_run_collection"`
	RootRunID          string           `json:"root_run_id"`
	ParentAttemptID    string           `json:"parent_attempt_id"`
	SenderBoundaryID   string           `json:"sender_boundary_id"`
	State              string           `json:"state"`
	MemberAttemptIDs   []string         `json:"member_attempt_ids"`
	OpeningEventSHA256 string           `json:"opening_event_sha256"`
	ClosingEventSHA256 *string          `json:"closing_event_sha256"`
	RecordSHA256       string           `json:"record_sha256"`
}
type ObservationParentLinkEvidence struct {
	ContractID        string           `json:"contract_id"`
	Scope             ObservationScope `json:"scope"`
	Task              ObservationTask  `json:"task"`
	RootRunCollection string           `json:"root_run_collection"`
	RootRunID         string           `json:"root_run_id"`
	ParentAttemptID   string           `json:"parent_attempt_id"`
	ChildAttemptID    string           `json:"child_attempt_id"`
	ParentRunRef      ObservationRef   `json:"parent_run_ref"`
	ChildRunRef       ObservationRef   `json:"child_run_ref"`
	Relationship      string           `json:"relationship"`
	SourceSHA256      string           `json:"source_sha256"`
	RecordSHA256      string           `json:"record_sha256"`
}

func init() {
	observationRules[reflect.TypeOf(ObservationDispatchClosure{})] = []observationRule{
		{name: "contract_id", kind: "string", minLength: 1, maxLength: 80, enums: []string{"arkism-crew-research-v2"}},
		{name: "scope", kind: "object"},
		{name: "task", kind: "object"},
		{name: "root_run_collection", kind: "string", minLength: 1, maxLength: 80, enums: []string{"crew_runs", "crew_meetings", "crew_assists"}},
		{name: "root_run_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "parent_attempt_id", kind: "string", minLength: 1, maxLength: 256, pattern: "^crw2-attempt-[0-9a-f]{64}$"},
		{name: "sender_boundary_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "member_attempt_ids", kind: "strings"},
		{name: "membership_sha256", kind: "string", minLength: 1, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "sender_journal_sha256", kind: "string", minLength: 1, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "record_sha256", kind: "string", minLength: 1, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
	}

	observationRules[reflect.TypeOf(ObservationSenderJournal{})] = []observationRule{
		{name: "contract_id", kind: "string", minLength: 1, maxLength: 80, enums: []string{"arkism-crew-research-v2"}},
		{name: "scope", kind: "object"},
		{name: "task", kind: "object"},
		{name: "root_run_collection", kind: "string", minLength: 1, maxLength: 80, enums: []string{"crew_runs", "crew_meetings", "crew_assists"}},
		{name: "root_run_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "parent_attempt_id", kind: "string", minLength: 1, maxLength: 256, pattern: "^crw2-attempt-[0-9a-f]{64}$"},
		{name: "sender_boundary_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "state", kind: "string", minLength: 1, maxLength: 80, enums: []string{"open", "closed"}},
		{name: "member_attempt_ids", kind: "strings"},
		{name: "opening_event_sha256", kind: "string", minLength: 1, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "closing_event_sha256", kind: "string", nullable: true, minLength: 1, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "record_sha256", kind: "string", minLength: 1, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
	}

	observationRules[reflect.TypeOf(ObservationParentLinkEvidence{})] = []observationRule{
		{name: "contract_id", kind: "string", minLength: 1, maxLength: 80, enums: []string{"arkism-crew-research-v2"}},
		{name: "scope", kind: "object"},
		{name: "task", kind: "object"},
		{name: "root_run_collection", kind: "string", minLength: 1, maxLength: 80, enums: []string{"crew_runs", "crew_meetings", "crew_assists"}},
		{name: "root_run_id", kind: "string", minLength: 1, maxLength: 256},
		{name: "parent_attempt_id", kind: "string", minLength: 1, maxLength: 256, pattern: "^crw2-attempt-[0-9a-f]{64}$"},
		{name: "child_attempt_id", kind: "string", minLength: 1, maxLength: 256, pattern: "^crw2-attempt-[0-9a-f]{64}$"},
		{name: "parent_run_ref", kind: "object"},
		{name: "child_run_ref", kind: "object"},
		{name: "relationship", kind: "string", minLength: 1, maxLength: 80, enums: []string{"same_execution_dispatch", "linked_meeting", "host_leaf", "subflow"}},
		{name: "source_sha256", kind: "string", minLength: 1, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
		{name: "record_sha256", kind: "string", minLength: 1, maxLength: 64, pattern: "^[0-9a-f]{64}$"},
	}

}

func clearObservationValues(totals *ObserverTotals) {
	for _, part := range []*ComponentTotal{&totals.Input, &totals.Output, &totals.CacheRead, &totals.CacheWrite, &totals.Reasoning, &totals.CallDuration} {
		part.Value = nil
	}
	totals.Cost.Value = nil
}

// ObservationEvidence contains caller-pinned local bytes only. No source
// authentication, scheduled coverage or effect permission is supplied by pins.
type ObservationEvidence struct {
	ParentLinks    map[string][]byte
	ParentSHA256   map[string]string
	Closures       []ObservationDispatchClosure
	SenderJournals map[string][]byte
	SenderSHA256   map[string]string
}
type ObservationBoundaryGroup struct {
	ParentAttemptID       string
	MemberAttemptIDs      []string
	LeafCost              CostTotal
	AggregateCost         UsageObservation
	LocalPinIntegrityOnly bool
}
type ObservationQualification struct {
	AccountingComplete   bool
	Issues               []string
	IntegrityComplete    bool
	RuntimeAuthenticated bool
	RunCoverage          string
	Groups               []ObservationBoundaryGroup
}

func evidenceHash(value any) (string, error) {
	if err := validateObservation(value); err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := encodeObservation(&out, reflect.ValueOf(value), true); err != nil {
		return "", err
	}
	sum := sha256.Sum256(out.Bytes())
	return hex.EncodeToString(sum[:]), nil
}
func sealEvidence(value any) (any, error) {
	cloned := cloneObservationValue(reflect.ValueOf(value))
	field := cloned.FieldByName("RecordSHA256")
	field.SetString(strings.Repeat("0", 64))
	hash, err := evidenceHash(cloned.Interface())
	if err != nil {
		return nil, err
	}
	field.SetString(hash)
	return cloned.Interface(), nil
}
func SealObservationParentLink(value ObservationParentLinkEvidence) (ObservationParentLinkEvidence, error) {
	sealed, err := sealEvidence(value)
	if err != nil {
		return ObservationParentLinkEvidence{}, err
	}
	result := sealed.(ObservationParentLinkEvidence)
	return result, validateEvidence(result)
}
func SealObservationDispatchClosure(value ObservationDispatchClosure) (ObservationDispatchClosure, error) {
	sealed, err := sealEvidence(value)
	if err != nil {
		return ObservationDispatchClosure{}, err
	}
	result := sealed.(ObservationDispatchClosure)
	return result, validateEvidence(result)
}
func SealObservationSenderJournal(value ObservationSenderJournal) (ObservationSenderJournal, error) {
	sealed, err := sealEvidence(value)
	if err != nil {
		return ObservationSenderJournal{}, err
	}
	result := sealed.(ObservationSenderJournal)
	return result, validateEvidence(result)
}

// ObservationMembershipHash hashes the sorted set. Duplicate IDs are rejected.
func ObservationMembershipHash(members []string) (string, error) {
	values := append([]string{}, members...)
	sort.Strings(values)
	for i, id := range values {
		if !observationAttemptHash.MatchString(id) || (i > 0 && id == values[i-1]) {
			return "", observationError("invalid_value", "$.member_attempt_ids")
		}
	}
	var out bytes.Buffer
	if err := encodeObservation(&out, reflect.ValueOf(values), false); err != nil {
		return "", err
	}
	sum := sha256.Sum256(out.Bytes())
	return hex.EncodeToString(sum[:]), nil
}
func validateEvidence(value any) error {
	if err := validateObservation(value); err != nil {
		return err
	}
	hash, err := evidenceHash(value)
	if err != nil {
		return err
	}
	actual := reflect.ValueOf(value).FieldByName("RecordSHA256").String()
	if hash != actual {
		return observationError("hash_mismatch", "$.record_sha256")
	}
	switch typed := value.(type) {
	case ObservationDispatchClosure:
		expected, e := ObservationMembershipHash(typed.MemberAttemptIDs)
		if e != nil {
			return e
		}
		if expected != typed.MembershipSHA256 {
			return observationError("hash_mismatch", "$.membership_sha256")
		}
	case ObservationSenderJournal:
		if _, e := ObservationMembershipHash(typed.MemberAttemptIDs); e != nil {
			return e
		}
		if (typed.State == "closed") != (typed.ClosingEventSHA256 != nil) {
			return observationError("invalid_value", "$.closing_event_sha256")
		}
	case ObservationParentLinkEvidence:
		for _, ref := range []ObservationRef{typed.ParentRunRef, typed.ChildRunRef} {
			if !observationContains([]string{"crew_runs", "crew_meetings", "crew_assists"}, ref.Collection) {
				return observationError("invalid_value", "$.parent_run_ref.collection")
			}
		}
	default:
		return observationError("invalid_type", "$")
	}
	return nil
}

func checkObservationJSONUnicode(raw []byte) error {
	if !utf8.Valid(raw) {
		return observationError("invalid_unicode", "$")
	}
	quoted := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			break
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return observationError("invalid_json", "$")
		}
		code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return observationError("invalid_json", "$")
		}
		i += 4
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return observationError("invalid_unicode", "$")
			}
			low, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return observationError("invalid_unicode", "$")
			}
			i += 6
		} else if code >= 0xdc00 && code <= 0xdfff {
			return observationError("invalid_unicode", "$")
		}
	}
	return nil
}
func walkObservationJSON(decoder *json.Decoder, depth int) error {
	if depth > 128 {
		return observationError("json_depth_limit", "$")
	}
	token, err := decoder.Token()
	if err != nil {
		return observationError("invalid_json", "$")
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, e := decoder.Token()
			if e != nil {
				return observationError("invalid_json", "$")
			}
			name, ok := key.(string)
			if !ok {
				return observationError("invalid_json", "$")
			}
			if seen[name] {
				return observationError("duplicate_key", "$")
			}
			seen[name] = true
			if e = walkObservationJSON(decoder, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for decoder.More() {
			if e := walkObservationJSON(decoder, depth+1); e != nil {
				return e
			}
		}
	default:
		return observationError("invalid_json", "$")
	}
	if _, err = decoder.Token(); err != nil {
		return observationError("invalid_json", "$")
	}
	return nil
}
func observationJSONShape(raw []byte, typ reflect.Type, path string) error {
	if typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return observationJSONShape(raw, typ.Elem(), path)
	}
	switch typ.Kind() {
	case reflect.Struct:
		return observationJSONObject(raw, typ, path)
	case reflect.Slice:
		return observationJSONArray(raw, typ, path)
	case reflect.Int64:
		text := strings.TrimSpace(string(raw))
		if !observationIntLexical.MatchString(text) {
			return observationError("invalid_type", path)
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return observationError("invalid_value", path)
		}
	case reflect.Float64:
		return observationJSONFloat(raw, path)
	}
	return nil
}
func observationJSONObject(raw []byte, typ reflect.Type, path string) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return observationError("invalid_type", path)
	}
	rules, ok := observationRules[typ]
	if !ok {
		return observationError("invalid_type", path)
	}
	known := map[string]bool{}
	for _, rule := range rules {
		known[rule.name] = true
	}
	for key := range object {
		if !known[key] {
			return observationError("unknown_field", path)
		}
	}
	for i, rule := range rules {
		data, present := object[rule.name]
		at := path + "." + rule.name
		if !present {
			return observationError("missing_field", at)
		}
		if !rule.nullable && bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return observationError("invalid_type", at)
		}
		if err := observationJSONShape(data, typ.Field(i).Type, at); err != nil {
			return err
		}
	}
	return nil
}
func observationJSONArray(raw []byte, typ reflect.Type, path string) error {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil {
		return observationError("invalid_type", path)
	}
	for _, item := range items {
		if err := observationJSONShape(item, typ.Elem(), path+"[*]"); err != nil {
			return err
		}
	}
	return nil
}
func observationJSONFloat(raw []byte, path string) error {
	text := strings.TrimSpace(string(raw))
	if text == "true" || text == "false" || strings.HasPrefix(text, "\"") {
		return observationError("invalid_type", path)
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
		return observationError("invalid_value", path)
	}
	return nil
}

var observationIntLexical = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func decodeObservationJSON(raw []byte, out any) error {
	if err := checkObservationJSONUnicode(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkObservationJSON(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return observationError("invalid_json", "$")
	}
	typ := reflect.TypeOf(out)
	if typ.Kind() != reflect.Pointer {
		return observationError("invalid_type", "$")
	}
	if err := observationJSONShape(raw, typ.Elem(), "$"); err != nil {
		return err
	}
	if json.Unmarshal(raw, out) != nil {
		return observationError("invalid_type", "$")
	}
	return nil
}

// DecodeAttemptObservation is a closed strict transport decoder for v2 only.
func DecodeAttemptObservation(raw []byte) (AttemptObservation, error) {
	var value AttemptObservation
	if err := decodeObservationJSON(raw, &value); err != nil {
		return value, err
	}
	return value, ValidateAttemptObservation(value)
}
func DecodeObservationParentLink(raw []byte) (ObservationParentLinkEvidence, error) {
	var value ObservationParentLinkEvidence
	if err := decodeObservationJSON(raw, &value); err != nil {
		return value, err
	}
	return value, validateEvidence(value)
}
func DecodeObservationDispatchClosure(raw []byte) (ObservationDispatchClosure, error) {
	var value ObservationDispatchClosure
	if err := decodeObservationJSON(raw, &value); err != nil {
		return value, err
	}
	return value, validateEvidence(value)
}
func DecodeObservationSenderJournal(raw []byte) (ObservationSenderJournal, error) {
	var value ObservationSenderJournal
	if err := decodeObservationJSON(raw, &value); err != nil {
		return value, err
	}
	return value, validateEvidence(value)
}

func observationBytesSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func sameEvidenceLineage(scope ObservationScope, task ObservationTask, rootCollection, rootID string, record AttemptObservation) bool {
	return scope == record.Identity.Run.Scope && task == record.Task && record.Identity.Run.RootRunID != nil && record.Identity.Run.RootRunCollection != nil && rootCollection == *record.Identity.Run.RootRunCollection && rootID == *record.Identity.Run.RootRunID
}
func matchingEvidenceRef(proof, actual ObservationRef) bool {
	return proof.Collection == actual.Collection && proof.ID == actual.ID && proof.Revision <= actual.Revision
}
func qualifyObservationParent(record, parent AttemptObservation, raw []byte, pin string) error {
	if pin == "" || observationBytesSHA(raw) != pin {
		return observationError("unverified_evidence_pin", "$")
	}
	proof, err := DecodeObservationParentLink(raw)
	if err != nil {
		return err
	}
	if proof.ChildAttemptID != record.Identity.AttemptID || proof.ParentAttemptID != parent.Identity.AttemptID || !sameEvidenceLineage(proof.Scope, proof.Task, proof.RootRunCollection, proof.RootRunID, record) || !sameEvidenceLineage(proof.Scope, proof.Task, proof.RootRunCollection, proof.RootRunID, parent) {
		return observationError("parent_lineage_mismatch", "$")
	}
	if !matchingEvidenceRef(proof.ParentRunRef, parent.Identity.Run.RunRef) || !matchingEvidenceRef(proof.ChildRunRef, record.Identity.Run.RunRef) {
		return observationError("parent_reference_mismatch", "$")
	}
	return qualifyObservationRelationship(record, parent, proof)
}
func observationDescendants(parent string, latest map[string]AttemptObservation) ([]string, error) {
	var members []string
	for id, record := range latest {
		if record.UsageRole != "provider_leaf" || record.DispatchState == "not_dispatched" {
			continue
		}
		cursor := record.Identity.ParentAttemptID
		seen := map[string]bool{id: true}
		for cursor != nil {
			if seen[*cursor] {
				return nil, observationError("parent_cycle", "$")
			}
			seen[*cursor] = true
			if *cursor == parent {
				members = append(members, id)
				break
			}
			next, ok := latest[*cursor]
			if !ok {
				return nil, observationError("missing_parent", "$")
			}
			cursor = next.Identity.ParentAttemptID
		}
	}
	sort.Strings(members)
	return members, nil
}
func qualifyObservationClosure(parent AttemptObservation, closure ObservationDispatchClosure, latest map[string]AttemptObservation, evidence ObservationEvidence) (ObservationBoundaryGroup, error) {
	group := ObservationBoundaryGroup{ParentAttemptID: parent.Identity.AttemptID, AggregateCost: parent.Usage, LocalPinIntegrityOnly: true}
	if err := validateEvidence(closure); err != nil {
		return group, err
	}
	if !sameEvidenceLineage(closure.Scope, closure.Task, closure.RootRunCollection, closure.RootRunID, parent) {
		return group, observationError("parent_lineage_mismatch", "$")
	}
	raw := evidence.SenderJournals[closure.SenderBoundaryID]
	pin := evidence.SenderSHA256[closure.SenderBoundaryID]
	if pin == "" || observationBytesSHA(raw) != pin || pin != closure.SenderJournalSHA256 {
		return group, observationError("unverified_evidence_pin", "$")
	}
	journal, err := DecodeObservationSenderJournal(raw)
	if err != nil {
		return group, err
	}
	if journal.State != "closed" || journal.ParentAttemptID != parent.Identity.AttemptID || journal.SenderBoundaryID != closure.SenderBoundaryID || !sameEvidenceLineage(journal.Scope, journal.Task, journal.RootRunCollection, journal.RootRunID, parent) {
		return group, observationError("unclosed_sender_boundary", "$")
	}
	members, err := observationDescendants(parent.Identity.AttemptID, latest)
	if err != nil {
		return group, err
	}
	expected, _ := ObservationMembershipHash(members)
	journalMembers, _ := ObservationMembershipHash(journal.MemberAttemptIDs)
	if expected != closure.MembershipSHA256 || journalMembers != expected {
		return group, observationError("membership_mismatch", "$")
	}
	totals := ObserverTotals{}
	for _, id := range members {
		observerAddUsage(&totals, latest[id].Usage)
	}
	observerFinishTotals(&totals, map[string]bool{})
	group.LeafCost = totals.Cost
	group.MemberAttemptIDs = members
	return group, nil
}

// QualifyObservationLedger verifies caller-pinned local evidence only. Matching
// unsigned fixtures never establish runtime authentication or whole-run coverage.
func QualifyObservationLedger(snapshot AttemptLedgerSnapshot, evidence ObservationEvidence) (ObservationQualification, error) {
	result := ObservationQualification{RunCoverage: "unknown", Groups: []ObservationBoundaryGroup{}}
	latest := map[string]AttemptObservation{}
	collector := NewAttemptObserver(nil)
	for _, record := range snapshot.Records {
		if err := ValidateAttemptObservation(record); err != nil {
			return result, err
		}
		collector.Observe(record)
	}
	rebuilt := collector.Snapshot()
	issues, issueErr := qualificationObservationIssues(snapshot.Issues, rebuilt.Issues)
	if issueErr != nil {
		return result, issueErr
	}
	result.AccountingComplete = true
	for _, issue := range issues {
		switch issue {
		case "parent_evidence_unverified", "leaf_coverage_unverified":
		case "unknown_usage", "unknown_cost", "unknown_duration", "unknown_dispatch", "pending_attempt", "accounting_overflow", "observation_gap":
			result.AccountingComplete = false
			result.Issues = append(result.Issues, issue)
		default:
			result.AccountingComplete = false
			result.Issues = issues
			return result, observationError(issue, "$")
		}
	}
	for _, record := range rebuilt.Latest {
		latest[record.Identity.AttemptID] = record
	}
	if err := qualifyObservationParents(latest, evidence); err != nil {
		result.AccountingComplete = false
		return result, err
	}
	groups, err := qualifyObservationGroups(latest, evidence)
	if err != nil {
		result.AccountingComplete = false
		return result, err
	}
	result.Groups = groups
	result.IntegrityComplete = true
	return result, nil
}

func validateObservationString(text string, rule observationRule, path string) error {
	if !utf8.ValidString(text) {
		return observationError("invalid_unicode", path)
	}
	if len(text) < rule.minLength || len(text) > rule.maxLength {
		return observationError("invalid_value", path)
	}
	if len(rule.enums) > 0 && !observationContains(rule.enums, text) {
		return observationError("invalid_value", path)
	}
	if rule.pattern != "" {
		pattern := observationHash
		if strings.HasPrefix(rule.pattern, "^crw2-attempt-") {
			pattern = observationAttemptHash
		}
		if !pattern.MatchString(text) {
			return observationError("invalid_value", path)
		}
	}
	return nil
}

func validateObservationRole(record AttemptObservation) error {
	if record.UsageRole == "provider_leaf" && record.LeafCoverage != "not_applicable" || record.UsageRole == "transport_aggregate" && record.LeafCoverage == "not_applicable" || record.UsageRole == "opaque_dispatch" && record.LeafCoverage == "complete" {
		return observationError("coverage_role_mismatch", "$.leaf_coverage")
	}
	if record.Identity.IdentityBasis == "observed_host_leaf" && record.UsageRole != "provider_leaf" {
		return observationError("coverage_role_mismatch", "$.usage_role")
	}
	if record.Status == "reserved" && record.DispatchState == "dispatched" {
		return observationError("invalid_value", "$.dispatch_state")
	}
	return nil
}

func qualifyObservationRelationship(record, parent AttemptObservation, proof ObservationParentLinkEvidence) error {
	own, p := record.Identity.Run.RunRef, parent.Identity.Run.RunRef
	same := own.Collection == p.Collection && own.ID == p.ID
	if same {
		if !observationContains([]string{"same_execution_dispatch", "host_leaf"}, proof.Relationship) {
			return observationError("parent_relationship_mismatch", "$")
		}
	} else {
		run := record.Identity.Run
		if run.ParentRunID == nil || run.ParentRunCollection == nil || *run.ParentRunID != p.ID || *run.ParentRunCollection != p.Collection {
			return observationError("parent_reference_mismatch", "$")
		}
		if own.Collection == "crew_meetings" {
			if proof.Relationship != "linked_meeting" || p.Collection != "crew_runs" {
				return observationError("parent_relationship_mismatch", "$")
			}
		} else if own.Collection != "crew_runs" || proof.Relationship != "subflow" {
			return observationError("parent_relationship_mismatch", "$")
		}
	}
	return nil
}

func qualifyObservationParents(latest map[string]AttemptObservation, evidence ObservationEvidence) error {
	for id, record := range latest {
		parent := record.Identity.ParentAttemptID
		if parent == nil {
			run := record.Identity.Run
			if *run.RootRunCollection != run.RunRef.Collection || *run.RootRunID != run.RunRef.ID {
				return observationError("parent_evidence_unverified", "$")
			}
			continue
		}
		p, ok := latest[*parent]
		if !ok {
			return observationError("missing_parent", "$")
		}
		if err := qualifyObservationParent(record, p, evidence.ParentLinks[id], evidence.ParentSHA256[id]); err != nil {
			return err
		}
	}
	for id := range evidence.ParentLinks {
		record, ok := latest[id]
		if !ok || record.Identity.ParentAttemptID == nil {
			return observationError("orphan_parent_proof", "$")
		}
	}
	return nil
}

func qualifyObservationGroups(latest map[string]AttemptObservation, evidence ObservationEvidence) ([]ObservationBoundaryGroup, error) {
	groups := []ObservationBoundaryGroup{}
	closures := map[string]ObservationDispatchClosure{}
	for _, closure := range evidence.Closures {
		if _, ok := closures[closure.ParentAttemptID]; ok {
			return nil, observationError("duplicate_closure", "$")
		}
		parent, ok := latest[closure.ParentAttemptID]
		if !ok || parent.UsageRole != "transport_aggregate" || parent.LeafCoverage != "complete" {
			return nil, observationError("orphan_closure", "$")
		}
		closures[closure.ParentAttemptID] = closure
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record := latest[id]
		if record.UsageRole == "provider_leaf" {
			continue
		}
		if record.UsageRole != "transport_aggregate" || record.LeafCoverage != "complete" {
			return nil, observationError("incomplete_leaf_coverage", "$")
		}
		closure, ok := closures[id]
		if !ok {
			return nil, observationError("unproven_leaf_coverage", "$")
		}
		group, err := qualifyObservationClosure(record, closure, latest, evidence)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func qualificationObservationIssues(original, rebuilt []string) ([]string, error) {
	known := []string{"parent_evidence_unverified", "leaf_coverage_unverified", "sink_pending", "no_observations", "observation_gap", "pending_attempt", "unknown_dispatch", "unknown_usage", "unknown_cost", "unknown_duration", "accounting_overflow", "canonical_attempt_conflict", "parent_cycle", "missing_parent", "parent_lineage_mismatch"}
	seen := map[string]bool{}
	for _, source := range [][]string{original, rebuilt} {
		for _, issue := range source {
			if !observationReasons[issue] && !observationContains(known, issue) {
				return nil, observationError("invalid_snapshot_issue", "$")
			}
			seen[issue] = true
		}
	}
	issues := make([]string, 0, len(seen))
	for issue := range seen {
		issues = append(issues, issue)
	}
	sort.Strings(issues)
	return issues, nil
}
