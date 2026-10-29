// Package dynamicsv2 defines the isolated, fixed dynamics-contract-v2 data contract.
// It does not execute state transitions, ranking, or product operations.
package dynamicsv2

import (
	"github.com/FelixBaek/arkism/internal/memoryharness"
	"reflect"
)

const ContractSHA256 = "952d663b76254e15a3a65ab09dcd580504bc4c2814ce4903a8883ece1ca8f0da"
const P1ContractSHA256 = "b6e965bbfecb4a0f20365d11709ff168043293fe1c5b848b06bc886e95f5f0d8"

// ScopeV2 preserves the immutable baseline type without widening its contract.
type ScopeV2 = memoryharness.Scope

// RefV2 preserves the immutable baseline type without widening its contract.
type RefV2 = memoryharness.Ref

// LegacyContextBudget preserves the immutable baseline type without widening its contract.
type LegacyContextBudget = memoryharness.ContextBudget

type AttemptDisposition string

type Attribution string

type Dimension string

type ErrorCode string

type EventKind string

type FallbackMode string

type InputRole string

type Outcome string

type OutputRole string

type OutputStatus string

type PolicyID string

type Reason string

type ReceiptDisposition string

type V2ErrorCode string

// ContractErrorV2 is a closed contract record. All JSON fields are required.
type ContractErrorV2 struct {
	Code V2ErrorCode `json:"code"`
	Path string      `json:"path"`
}

// DynamicInputManifestV2 is a closed contract record. All JSON fields are required.
type DynamicInputManifestV2 struct {
	Protocol             string     `json:"protocol"`
	DatasetID            string     `json:"dataset_id"`
	StreamID             string     `json:"stream_id"`
	ClockDomain          string     `json:"clock_domain"`
	BaselineBundleSHA256 string     `json:"baseline_bundle_sha256"`
	PolicyContractSHA256 string     `json:"policy_contract_sha256"`
	Files                []RoleFile `json:"files"`
	ManifestSHA256       string     `json:"manifest_sha256"`
}

// DynamicsConfigV2 is a closed contract record. All JSON fields are required.
type DynamicsConfigV2 struct {
	Protocol           string  `json:"protocol"`
	Version            string  `json:"version"`
	Mode               string  `json:"mode"`
	Clock              string  `json:"clock"`
	ClockDomain        string  `json:"clock_domain"`
	Initial            float64 `json:"initial"`
	TauNS              int64   `json:"tau_ns"`
	EtaPositive        float64 `json:"eta_positive"`
	EtaNegative        float64 `json:"eta_negative"`
	UnitStrength       float64 `json:"unit_strength"`
	NegativePolicy     string  `json:"negative_policy"`
	DecayEnabled       bool    `json:"decay_enabled"`
	OutcomeJumpEnabled bool    `json:"outcome_jump_enabled"`
	Atol               float64 `json:"atol"`
	Rtol               float64 `json:"rtol"`
	EvidenceRuleSHA256 string  `json:"evidence_rule_sha256"`
	ConfigSHA256       string  `json:"config_sha256"`
}

// DynamicsKeyV2 is a closed contract record. All JSON fields are required.
type DynamicsKeyV2 struct {
	Scope          ScopeV2   `json:"scope"`
	Target         RefV2     `json:"target"`
	Dimension      Dimension `json:"dimension"`
	ContractSHA256 string    `json:"contract_sha256"`
	ConfigSHA256   string    `json:"config_sha256"`
}

// DynamicsRunConfigV2 is a closed contract record. All JSON fields are required.
type DynamicsRunConfigV2 struct {
	Protocol            string           `json:"protocol"`
	RunID               string           `json:"run_id"`
	InputManifestSHA256 string           `json:"input_manifest_sha256"`
	RegistrySHA256      string           `json:"registry_sha256"`
	PolicyIDs           []PolicyID       `json:"policy_ids"`
	Configs             []PolicyConfig   `json:"configs"`
	ResourceLimits      ResourceLimits   `json:"resource_limits"`
	ConfigSHA256        string           `json:"config_sha256"`
	Registry            PolicyRegistryV2 `json:"registry"`
}

// EventFileV2 is a closed contract record. All JSON fields are required.
type EventFileV2 struct {
	Protocol     string    `json:"protocol"`
	StreamID     string    `json:"stream_id"`
	ClockDomain  string    `json:"clock_domain"`
	ConfigSHA256 string    `json:"config_sha256"`
	Events       []EventV2 `json:"events"`
}

// EventV2 is a closed contract record. All JSON fields are required.
type EventV2 struct {
	Protocol           string           `json:"protocol"`
	StreamID           string           `json:"stream_id"`
	EventID            string           `json:"event_id"`
	TransportAttemptID string           `json:"transport_attempt_id"`
	Key                DynamicsKeyV2    `json:"key"`
	Kind               EventKind        `json:"kind"`
	ClockDomain        string           `json:"clock_domain"`
	ObservedAtNS       *int64           `json:"observed_at_ns"`
	ArrivedAtNS        *int64           `json:"arrived_at_ns"`
	AcceptedAtNS       int64            `json:"accepted_at_ns"`
	AcceptedSeq        int64            `json:"accepted_seq"`
	Task               *TaskBinding     `json:"task"`
	Evidence           *EvidenceBinding `json:"evidence"`
	Outcome            Outcome          `json:"outcome"`
	Attribution        Attribution      `json:"attribution"`
	Strength           *float64         `json:"strength"`
	Reason             Reason           `json:"reason"`
	PayloadSHA256      string           `json:"payload_sha256"`
}

// ImportanceFeatureV1 is a closed contract record. All JSON fields are required.
type ImportanceFeatureV1 struct {
	FeatureID             string        `json:"feature_id"`
	Scope                 ScopeV2       `json:"scope"`
	Target                RefV2         `json:"target"`
	Source                EvidenceRef   `json:"source"`
	ClockDomain           string        `json:"clock_domain"`
	ObservedAtNS          *int64        `json:"observed_at_ns"`
	AvailableAtNS         int64         `json:"available_at_ns"`
	AvailableAcceptedSeq  int64         `json:"available_accepted_seq"`
	FeatureName           string        `json:"feature_name"`
	FeatureVersion        string        `json:"feature_version"`
	RuleSHA256            string        `json:"rule_sha256"`
	GeneratorConfigSHA256 string        `json:"generator_config_sha256"`
	ProducerVersion       string        `json:"producer_version"`
	InputRefs             []EvidenceRef `json:"input_refs"`
	Status                string        `json:"status"`
	Value                 *float64      `json:"value"`
	Unit                  string        `json:"unit"`
	MissingReason         *Reason       `json:"missing_reason"`
	FeatureSHA256         string        `json:"feature_sha256"`
}

// P1FeatureFileV1 is a closed contract record. All JSON fields are required.
type P1FeatureFileV1 struct {
	Protocol              string                `json:"protocol"`
	FeatureContractSHA256 string                `json:"feature_contract_sha256"`
	Features              []ImportanceFeatureV1 `json:"features"`
}

// P1InputManifestV1 is a closed contract record. All JSON fields are required.
type P1InputManifestV1 struct {
	Protocol                  string   `json:"protocol"`
	BaseDynamicManifestSHA256 string   `json:"base_dynamic_manifest_sha256"`
	FeatureContractSHA256     string   `json:"feature_contract_sha256"`
	FeatureFile               RoleFile `json:"feature_file"`
	ManifestSHA256            string   `json:"manifest_sha256"`
}

// PolicyDecisionV2 is a closed contract record. All JSON fields are required.
type PolicyDecisionV2 struct {
	QueryID                 string              `json:"query_id"`
	PolicyID                PolicyID            `json:"policy_id"`
	PolicyVersion           string              `json:"policy_version"`
	CandidateSnapshotSHA256 string              `json:"candidate_snapshot_sha256"`
	PrefixSHA256            string              `json:"prefix_sha256"`
	RankerConfigSHA256      string              `json:"ranker_config_sha256"`
	Ranks                   []RankRow           `json:"ranks"`
	Selected                []RefV2             `json:"selected"`
	ContextCostUsed         int64               `json:"context_cost_used"`
	ContextBudget           LegacyContextBudget `json:"context_budget"`
	Status                  string              `json:"status"`
	Reason                  Reason              `json:"reason"`
	DecisionSHA256          string              `json:"decision_sha256"`
}

// PolicyRegistryV2 is a closed contract record. All JSON fields are required.
type PolicyRegistryV2 struct {
	Protocol       string         `json:"protocol"`
	Entries        []PolicyEntry  `json:"entries"`
	ResourceLimits ResourceLimits `json:"resource_limits"`
	RegistrySHA256 string         `json:"registry_sha256"`
}

// PredictionRunV2 is a closed contract record. All JSON fields are required.
type PredictionRunV2 struct {
	Protocol            string         `json:"protocol"`
	RunID               string         `json:"run_id"`
	PolicyID            PolicyID       `json:"policy_id"`
	PolicyVersion       string         `json:"policy_version"`
	InputManifestSHA256 string         `json:"input_manifest_sha256"`
	RunConfigSHA256     string         `json:"run_config_sha256"`
	RegistrySHA256      string         `json:"registry_sha256"`
	Predictions         []PredictionV2 `json:"predictions"`
	Cost                OfflineCost    `json:"cost"`
	RunSHA256           string         `json:"run_sha256"`
}

// PredictionV2 is a closed contract record. All JSON fields are required.
type PredictionV2 struct {
	QueryID  string            `json:"query_id"`
	Status   string            `json:"status"`
	Decision *PolicyDecisionV2 `json:"decision"`
	Error    *ContractErrorV2  `json:"error"`
	Reason   Reason            `json:"reason"`
}

// ProbeFileV2 is a closed contract record. All JSON fields are required.
type ProbeFileV2 struct {
	Protocol    string    `json:"protocol"`
	StreamID    string    `json:"stream_id"`
	ClockDomain string    `json:"clock_domain"`
	Probes      []ProbeV2 `json:"probes"`
}

// ProbeV2 is a closed contract record. All JSON fields are required.
type ProbeV2 struct {
	Protocol                string              `json:"protocol"`
	QueryID                 string              `json:"query_id"`
	StreamID                string              `json:"stream_id"`
	Scope                   ScopeV2             `json:"scope"`
	Dimension               Dimension           `json:"dimension"`
	AtNS                    int64               `json:"at_ns"`
	ClockDomain             string              `json:"clock_domain"`
	CutoffAcceptedSeq       int64               `json:"cutoff_accepted_seq"`
	CandidateSnapshotID     string              `json:"candidate_snapshot_id"`
	CandidateSnapshotSHA256 string              `json:"candidate_snapshot_sha256"`
	PrefixSHA256            string              `json:"prefix_sha256"`
	FinalK                  int64               `json:"final_k"`
	ContextBudget           LegacyContextBudget `json:"context_budget"`
	PolicyID                PolicyID            `json:"policy_id"`
	PolicyVersion           string              `json:"policy_version"`
}

// RankerConfigV2 is a closed contract record. All JSON fields are required.
type RankerConfigV2 struct {
	PolicyID                PolicyID   `json:"policy_id"`
	PolicyVersion           string     `json:"policy_version"`
	RegistrySHA256          string     `json:"registry_sha256"`
	DynamicsConfigSHA256    *string    `json:"dynamics_config_sha256"`
	FeatureContractSHA256   *string    `json:"feature_contract_sha256"`
	OuterOffset             int64      `json:"outer_offset"`
	Lanes                   []RankLane `json:"lanes"`
	MissingState            string     `json:"missing_state"`
	MissingFeature          string     `json:"missing_feature"`
	RecencyAnchor           string     `json:"recency_anchor"`
	RecencyTauNS            int64      `json:"recency_tau_ns"`
	BaseEqualRank           string     `json:"base_equal_rank"`
	FeatureEqualRank        string     `json:"feature_equal_rank"`
	FinalTie                string     `json:"final_tie"`
	Fallback                string     `json:"fallback"`
	Atol                    float64    `json:"atol"`
	Rtol                    float64    `json:"rtol"`
	InheritedB2ConfigSHA256 *string    `json:"inherited_b2_config_sha256"`
	Ablation                string     `json:"ablation"`
	ConfigSHA256            string     `json:"config_sha256"`
}

// ReceiptIndexV2 is a closed contract record. All JSON fields are required.
type ReceiptIndexV2 struct {
	StreamID             string                `json:"stream_id"`
	ClockDomain          string                `json:"clock_domain"`
	Cursor               StreamCursorV2        `json:"cursor"`
	Receipts             []ReceiptV2           `json:"receipts"`
	EvidenceConsumptions []EvidenceConsumption `json:"evidence_consumptions"`
	LifecycleFence       LifecycleFence        `json:"lifecycle_fence"`
	IndexSHA256          string                `json:"index_sha256"`
}

// ReceiptV2 is a closed contract record. All JSON fields are required.
type ReceiptV2 struct {
	StreamID                  string             `json:"stream_id"`
	EventID                   string             `json:"event_id"`
	PayloadSHA256             string             `json:"payload_sha256"`
	EvidenceConsumptionSHA256 *string            `json:"evidence_consumption_sha256"`
	Key                       DynamicsKeyV2      `json:"key"`
	AcceptedAtNS              int64              `json:"accepted_at_ns"`
	AcceptedSeq               int64              `json:"accepted_seq"`
	Disposition               ReceiptDisposition `json:"disposition"`
	BeforeStateRevision       int64              `json:"before_state_revision"`
	AfterStateRevision        int64              `json:"after_state_revision"`
	Reason                    Reason             `json:"reason"`
	ReceiptSHA256             string             `json:"receipt_sha256"`
	DuplicateOfReceiptSHA256  *string            `json:"duplicate_of_receipt_sha256"`
}

// ReplayStatsV2 is a closed contract record. All JSON fields are required.
type ReplayStatsV2 struct {
	InputCount             int64            `json:"input_count"`
	AppliedCount           int64            `json:"applied_count"`
	DuplicateEventCount    int64            `json:"duplicate_event_count"`
	DuplicateEvidenceCount int64            `json:"duplicate_evidence_count"`
	NonLearningCount       int64            `json:"non_learning_count"`
	RejectedCount          int64            `json:"rejected_count"`
	Error                  *ContractErrorV2 `json:"error"`
}

// SnapshotFileV2 is a closed contract record. All JSON fields are required.
type SnapshotFileV2 struct {
	Protocol string     `json:"protocol"`
	Snapshot SnapshotV2 `json:"snapshot"`
}

// SnapshotV2 is a closed contract record. All JSON fields are required.
type SnapshotV2 struct {
	Protocol       string         `json:"protocol"`
	Scope          ScopeV2        `json:"scope"`
	Dimension      Dimension      `json:"dimension"`
	ConfigSHA256   string         `json:"config_sha256"`
	Cursor         StreamCursorV2 `json:"cursor"`
	States         []StateV2      `json:"states"`
	Receipts       ReceiptIndexV2 `json:"receipts"`
	SnapshotSHA256 string         `json:"snapshot_sha256"`
	StreamID       string         `json:"stream_id"`
}

// StateV2 is a closed contract record. All JSON fields are required.
type StateV2 struct {
	Key                     DynamicsKeyV2 `json:"key"`
	Mode                    string        `json:"mode"`
	Value                   float64       `json:"value"`
	StateRevision           int64         `json:"state_revision"`
	ActivatedAtNS           *int64        `json:"activated_at_ns"`
	ActivationAcceptedSeq   *int64        `json:"activation_accepted_seq"`
	ActivationEventID       *string       `json:"activation_event_id"`
	ActivationPayloadSHA256 *string       `json:"activation_payload_sha256"`
	LastAppliedAtNS         *int64        `json:"last_applied_at_ns"`
	LastAppliedSeq          *int64        `json:"last_applied_seq"`
	Status                  string        `json:"status"`
	Reason                  Reason        `json:"reason"`
	StateSHA256             string        `json:"state_sha256"`
}

// StateViewV2 is a closed contract record. All JSON fields are required.
type StateViewV2 struct {
	Key                   DynamicsKeyV2  `json:"key"`
	AtNS                  int64          `json:"at_ns"`
	ClockDomain           string         `json:"clock_domain"`
	CutoffAcceptedSeq     int64          `json:"cutoff_accepted_seq"`
	SnapshotSHA256        string         `json:"snapshot_sha256"`
	SnapshotCursor        StreamCursorV2 `json:"snapshot_cursor"`
	SourceStateRevision   int64          `json:"source_state_revision"`
	ActivatedAtNS         int64          `json:"activated_at_ns"`
	ActivationAcceptedSeq int64          `json:"activation_accepted_seq"`
	Value                 float64        `json:"value"`
	Status                string         `json:"status"`
	Reason                Reason         `json:"reason"`
	StreamID              string         `json:"stream_id"`
	Scope                 ScopeV2        `json:"scope"`
	Dimension             Dimension      `json:"dimension"`
	ConfigSHA256          string         `json:"config_sha256"`
}

// StreamCursorV2 is a closed contract record. All JSON fields are required.
type StreamCursorV2 struct {
	StreamID     string `json:"stream_id"`
	ClockDomain  string `json:"clock_domain"`
	AcceptedAtNS int64  `json:"accepted_at_ns"`
	AcceptedSeq  int64  `json:"accepted_seq"`
	PrefixSHA256 string `json:"prefix_sha256"`
}

// TransitionV2 is a closed contract record. All JSON fields are required.
type TransitionV2 struct {
	BeforeStateSHA256  string             `json:"before_state_sha256"`
	State              StateV2            `json:"state"`
	Receipts           ReceiptIndexV2     `json:"receipts"`
	Receipt            ReceiptV2          `json:"receipt"`
	Cursor             StreamCursorV2     `json:"cursor"`
	TransitionSHA256   string             `json:"transition_sha256"`
	AttemptDisposition AttemptDisposition `json:"attempt_disposition"`
}

// AuxRank is a closed contract record. All JSON fields are required.
type AuxRank struct {
	Kind      string  `json:"kind"`
	Value     float64 `json:"value"`
	DenseRank int64   `json:"dense_rank"`
}

// CLIStatus is a closed contract record. All JSON fields are required.
type CLIStatus struct {
	Status  string           `json:"status"`
	RunID   *string          `json:"run_id"`
	Outputs []OutputRoleFile `json:"outputs"`
	Error   *ContractErrorV2 `json:"error"`
}

// CompareResult is a closed contract record. All JSON fields are required.
type CompareResult struct {
	Protocol        string            `json:"protocol"`
	RunIDs          []string          `json:"run_ids"`
	RegistrySHA256  string            `json:"registry_sha256"`
	EqualConditions bool              `json:"equal_conditions"`
	Checks          []ComparisonCheck `json:"checks"`
	Statuses        []ComparedStatus  `json:"statuses"`
}

// ComparedStatus is a closed contract record. All JSON fields are required.
type ComparedStatus struct {
	RunID    string   `json:"run_id"`
	PolicyID PolicyID `json:"policy_id"`
	Planned  int64    `json:"planned"`
	Ok       int64    `json:"ok"`
	Failed   int64    `json:"failed"`
	Fallback int64    `json:"fallback"`
}

// ComparisonCheck is a closed contract record. All JSON fields are required.
type ComparisonCheck struct {
	Name   string `json:"name"`
	Equal  bool   `json:"equal"`
	Reason Reason `json:"reason"`
}

// EvidenceBinding is a closed contract record. All JSON fields are required.
type EvidenceBinding struct {
	Refs                 []EvidenceRef `json:"refs"`
	Target               RefV2         `json:"target"`
	TaskSHA256           string        `json:"task_sha256"`
	Dimension            Dimension     `json:"dimension"`
	RuleSHA256           string        `json:"rule_sha256"`
	Independence         string        `json:"independence"`
	AvailableAcceptedSeq int64         `json:"available_accepted_seq"`
	SourceStatus         string        `json:"source_status"`
	BindingSHA256        string        `json:"binding_sha256"`
}

// EvidenceConsumption is a closed contract record. All JSON fields are required.
type EvidenceConsumption struct {
	IdentitySHA256     string `json:"identity_sha256"`
	ClaimSHA256        string `json:"claim_sha256"`
	FirstEventID       string `json:"first_event_id"`
	FirstReceiptSHA256 string `json:"first_receipt_sha256"`
}

// EvidenceRef is a closed contract record. All JSON fields are required.
type EvidenceRef struct {
	Namespace     string `json:"namespace"`
	Collection    string `json:"collection"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	ContentSHA256 string `json:"content_sha256"`
}

// FenceEntry is a closed contract record. All JSON fields are required.
type FenceEntry struct {
	Scope      ScopeV2       `json:"scope"`
	Target     RefV2         `json:"target"`
	Status     string        `json:"status"`
	SourceRefs []EvidenceRef `json:"source_refs"`
	Reason     Reason        `json:"reason"`
}

// LifecycleFence is a closed contract record. All JSON fields are required.
type LifecycleFence struct {
	Namespace          string       `json:"namespace"`
	Epoch              int64        `json:"epoch"`
	MinimumAcceptedSeq int64        `json:"minimum_accepted_seq"`
	Entries            []FenceEntry `json:"entries"`
	FenceSHA256        string       `json:"fence_sha256"`
}

// OfflineCost is a closed contract record. All JSON fields are required.
type OfflineCost struct {
	EventInputs         int64    `json:"event_inputs"`
	UniqueEvents        int64    `json:"unique_events"`
	StateUpdates        int64    `json:"state_updates"`
	DuplicateEvents     int64    `json:"duplicate_events"`
	DuplicateEvidence   int64    `json:"duplicate_evidence"`
	FeatureReads        int64    `json:"feature_reads"`
	FailedPredictions   int64    `json:"failed_predictions"`
	FallbackPredictions int64    `json:"fallback_predictions"`
	ElapsedWallNS       *int64   `json:"elapsed_wall_ns"`
	ProviderUSD         *float64 `json:"provider_usd"`
}

// OutputManifest is a closed contract record. All JSON fields are required.
type OutputManifest struct {
	Protocol            string           `json:"protocol"`
	RunID               string           `json:"run_id"`
	InputManifestSHA256 string           `json:"input_manifest_sha256"`
	RunConfigSHA256     string           `json:"run_config_sha256"`
	RunConfigRawSHA256  string           `json:"run_config_raw_sha256"`
	RegistrySHA256      string           `json:"registry_sha256"`
	Status              OutputStatus     `json:"status"`
	PlannedRows         int64            `json:"planned_rows"`
	FailedRows          int64            `json:"failed_rows"`
	Files               []OutputRoleFile `json:"files"`
}

// OutputRoleFile is a closed contract record. All JSON fields are required.
type OutputRoleFile struct {
	Role         OutputRole `json:"role"`
	RelativePath string     `json:"relative_path"`
	RawSHA256    string     `json:"raw_sha256"`
	SizeBytes    int64      `json:"size_bytes"`
}

// PolicyConfig is a closed contract record. All JSON fields are required.
type PolicyConfig struct {
	PolicyID PolicyID          `json:"policy_id"`
	Dynamics *DynamicsConfigV2 `json:"dynamics"`
	Ranker   RankerConfigV2    `json:"ranker"`
}

// PolicyEntry is a closed contract record. All JSON fields are required.
type PolicyEntry struct {
	PolicyID            PolicyID          `json:"policy_id"`
	Version             string            `json:"version"`
	ImplementedMode     string            `json:"implemented_mode"`
	UsesOutcomes        bool              `json:"uses_outcomes"`
	UsesElapsedDecay    bool              `json:"uses_elapsed_decay"`
	AllowedRoles        []InputRole       `json:"allowed_roles"`
	Dynamics            *DynamicsConfigV2 `json:"dynamics"`
	Ranker              RankerConfigV2    `json:"ranker"`
	RuleSHA256          string            `json:"rule_sha256"`
	ParameterProvenance string            `json:"parameter_provenance"`
}

// RankLane is a closed contract record. All JSON fields are required.
type RankLane struct {
	Kind   string  `json:"kind"`
	Weight float64 `json:"weight"`
}

// RankRow is a closed contract record. All JSON fields are required.
type RankRow struct {
	Ref           RefV2     `json:"ref"`
	BaseRRFScore  float64   `json:"base_rrf_score"`
	BaseDenseRank int64     `json:"base_dense_rank"`
	Auxiliary     []AuxRank `json:"auxiliary"`
	FusionScore   float64   `json:"fusion_score"`
	FinalOrdinal  int64     `json:"final_ordinal"`
}

// ResourceLimits is a closed contract record. All JSON fields are required.
type ResourceLimits struct {
	MaxFileBytes   int64 `json:"max_file_bytes"`
	MaxEvents      int64 `json:"max_events"`
	MaxStates      int64 `json:"max_states"`
	MaxReceipts    int64 `json:"max_receipts"`
	MaxProbes      int64 `json:"max_probes"`
	MaxFeatures    int64 `json:"max_features"`
	MaxArrayItems  int64 `json:"max_array_items"`
	MaxStringBytes int64 `json:"max_string_bytes"`
	MaxJSONDepth   int64 `json:"max_json_depth"`
}

// RoleFile is a closed contract record. All JSON fields are required.
type RoleFile struct {
	Role         InputRole `json:"role"`
	RelativePath string    `json:"relative_path"`
	RawSHA256    string    `json:"raw_sha256"`
	SizeBytes    int64     `json:"size_bytes"`
}

// TaskBinding is a closed contract record. All JSON fields are required.
type TaskBinding struct {
	TaskRef       EvidenceRef `json:"task_ref"`
	RunRef        EvidenceRef `json:"run_ref"`
	TrialRef      EvidenceRef `json:"trial_ref"`
	CheckerSHA256 string      `json:"checker_sha256"`
	RuleSHA256    string      `json:"rule_sha256"`
	Qualification string      `json:"qualification"`
	TaskSHA256    string      `json:"task_sha256"`
}

// DynamicInputManifestV2HashProjection is a closed contract record. All JSON fields are required.
type DynamicInputManifestV2HashProjection struct {
	Protocol             string     `json:"protocol"`
	DatasetID            string     `json:"dataset_id"`
	StreamID             string     `json:"stream_id"`
	ClockDomain          string     `json:"clock_domain"`
	BaselineBundleSHA256 string     `json:"baseline_bundle_sha256"`
	PolicyContractSHA256 string     `json:"policy_contract_sha256"`
	Files                []RoleFile `json:"files"`
}

// DynamicsConfigV2HashProjection is a closed contract record. All JSON fields are required.
type DynamicsConfigV2HashProjection struct {
	Protocol           string  `json:"protocol"`
	Version            string  `json:"version"`
	Mode               string  `json:"mode"`
	Clock              string  `json:"clock"`
	ClockDomain        string  `json:"clock_domain"`
	Initial            float64 `json:"initial"`
	TauNS              int64   `json:"tau_ns"`
	EtaPositive        float64 `json:"eta_positive"`
	EtaNegative        float64 `json:"eta_negative"`
	UnitStrength       float64 `json:"unit_strength"`
	NegativePolicy     string  `json:"negative_policy"`
	DecayEnabled       bool    `json:"decay_enabled"`
	OutcomeJumpEnabled bool    `json:"outcome_jump_enabled"`
	Atol               float64 `json:"atol"`
	Rtol               float64 `json:"rtol"`
	EvidenceRuleSHA256 string  `json:"evidence_rule_sha256"`
}

// DynamicsRunConfigV2HashProjection is a closed contract record. All JSON fields are required.
type DynamicsRunConfigV2HashProjection struct {
	Protocol            string           `json:"protocol"`
	RunID               string           `json:"run_id"`
	InputManifestSHA256 string           `json:"input_manifest_sha256"`
	RegistrySHA256      string           `json:"registry_sha256"`
	PolicyIDs           []PolicyID       `json:"policy_ids"`
	Configs             []PolicyConfig   `json:"configs"`
	ResourceLimits      ResourceLimits   `json:"resource_limits"`
	Registry            PolicyRegistryV2 `json:"registry"`
}

// EventLogicalProjectionV2 is a closed contract record. All JSON fields are required.
type EventLogicalProjectionV2 struct {
	Protocol     string           `json:"protocol"`
	StreamID     string           `json:"stream_id"`
	EventID      string           `json:"event_id"`
	Key          DynamicsKeyV2    `json:"key"`
	Kind         EventKind        `json:"kind"`
	ClockDomain  string           `json:"clock_domain"`
	ObservedAtNS *int64           `json:"observed_at_ns"`
	Task         *TaskBinding     `json:"task"`
	Evidence     *EvidenceBinding `json:"evidence"`
	Outcome      Outcome          `json:"outcome"`
	Attribution  Attribution      `json:"attribution"`
	Strength     *float64         `json:"strength"`
	Reason       Reason           `json:"reason"`
}

// EvidenceBindingHashProjection is a closed contract record. All JSON fields are required.
type EvidenceBindingHashProjection struct {
	Refs                 []EvidenceRef `json:"refs"`
	Target               RefV2         `json:"target"`
	TaskSHA256           string        `json:"task_sha256"`
	Dimension            Dimension     `json:"dimension"`
	RuleSHA256           string        `json:"rule_sha256"`
	Independence         string        `json:"independence"`
	AvailableAcceptedSeq int64         `json:"available_accepted_seq"`
	SourceStatus         string        `json:"source_status"`
}

// EvidenceClaimProjectionV2 is a closed contract record. All JSON fields are required.
type EvidenceClaimProjectionV2 struct {
	Outcome      Outcome         `json:"outcome"`
	Attribution  Attribution     `json:"attribution"`
	Strength     *float64        `json:"strength"`
	ObservedAtNS *int64          `json:"observed_at_ns"`
	Task         TaskBinding     `json:"task"`
	Evidence     EvidenceBinding `json:"evidence"`
}

// EvidenceIdentityProjectionV2 is a closed contract record. All JSON fields are required.
type EvidenceIdentityProjectionV2 struct {
	Scope         ScopeV2       `json:"scope"`
	Target        RefV2         `json:"target"`
	Dimension     Dimension     `json:"dimension"`
	TaskRef       EvidenceRef   `json:"task_ref"`
	RunRef        EvidenceRef   `json:"run_ref"`
	TrialRef      EvidenceRef   `json:"trial_ref"`
	CheckerSHA256 string        `json:"checker_sha256"`
	EvidenceRefs  []EvidenceRef `json:"evidence_refs"`
}

// ImportanceFeatureV1HashProjection is a closed contract record. All JSON fields are required.
type ImportanceFeatureV1HashProjection struct {
	FeatureID             string        `json:"feature_id"`
	Scope                 ScopeV2       `json:"scope"`
	Target                RefV2         `json:"target"`
	Source                EvidenceRef   `json:"source"`
	ClockDomain           string        `json:"clock_domain"`
	ObservedAtNS          *int64        `json:"observed_at_ns"`
	AvailableAtNS         int64         `json:"available_at_ns"`
	AvailableAcceptedSeq  int64         `json:"available_accepted_seq"`
	FeatureName           string        `json:"feature_name"`
	FeatureVersion        string        `json:"feature_version"`
	RuleSHA256            string        `json:"rule_sha256"`
	GeneratorConfigSHA256 string        `json:"generator_config_sha256"`
	ProducerVersion       string        `json:"producer_version"`
	InputRefs             []EvidenceRef `json:"input_refs"`
	Status                string        `json:"status"`
	Value                 *float64      `json:"value"`
	Unit                  string        `json:"unit"`
	MissingReason         *Reason       `json:"missing_reason"`
}

// LifecycleFenceHashProjection is a closed contract record. All JSON fields are required.
type LifecycleFenceHashProjection struct {
	Namespace          string       `json:"namespace"`
	Epoch              int64        `json:"epoch"`
	MinimumAcceptedSeq int64        `json:"minimum_accepted_seq"`
	Entries            []FenceEntry `json:"entries"`
}

// P1InputManifestV1HashProjection is a closed contract record. All JSON fields are required.
type P1InputManifestV1HashProjection struct {
	Protocol                  string   `json:"protocol"`
	BaseDynamicManifestSHA256 string   `json:"base_dynamic_manifest_sha256"`
	FeatureContractSHA256     string   `json:"feature_contract_sha256"`
	FeatureFile               RoleFile `json:"feature_file"`
}

// PolicyDecisionV2HashProjection is a closed contract record. All JSON fields are required.
type PolicyDecisionV2HashProjection struct {
	QueryID                 string              `json:"query_id"`
	PolicyID                PolicyID            `json:"policy_id"`
	PolicyVersion           string              `json:"policy_version"`
	CandidateSnapshotSHA256 string              `json:"candidate_snapshot_sha256"`
	PrefixSHA256            string              `json:"prefix_sha256"`
	RankerConfigSHA256      string              `json:"ranker_config_sha256"`
	Ranks                   []RankRow           `json:"ranks"`
	Selected                []RefV2             `json:"selected"`
	ContextCostUsed         int64               `json:"context_cost_used"`
	ContextBudget           LegacyContextBudget `json:"context_budget"`
	Status                  string              `json:"status"`
	Reason                  Reason              `json:"reason"`
}

// PolicyRegistryGraphProjectionV2 is a closed contract record. All JSON fields are required.
type PolicyRegistryGraphProjectionV2 struct {
	Protocol       string                            `json:"protocol"`
	Entries        []RegistryPolicyEntryProjectionV2 `json:"entries"`
	ResourceLimits ResourceLimits                    `json:"resource_limits"`
}

// PredictionRunV2HashProjection is a closed contract record. All JSON fields are required.
type PredictionRunV2HashProjection struct {
	Protocol            string         `json:"protocol"`
	RunID               string         `json:"run_id"`
	PolicyID            PolicyID       `json:"policy_id"`
	PolicyVersion       string         `json:"policy_version"`
	InputManifestSHA256 string         `json:"input_manifest_sha256"`
	RunConfigSHA256     string         `json:"run_config_sha256"`
	RegistrySHA256      string         `json:"registry_sha256"`
	Predictions         []PredictionV2 `json:"predictions"`
	Cost                OfflineCost    `json:"cost"`
}

// RankerConfigParametersProjectionV2 is a closed contract record. All JSON fields are required.
type RankerConfigParametersProjectionV2 struct {
	PolicyID                PolicyID   `json:"policy_id"`
	PolicyVersion           string     `json:"policy_version"`
	DynamicsConfigSHA256    *string    `json:"dynamics_config_sha256"`
	FeatureContractSHA256   *string    `json:"feature_contract_sha256"`
	OuterOffset             int64      `json:"outer_offset"`
	Lanes                   []RankLane `json:"lanes"`
	MissingState            string     `json:"missing_state"`
	MissingFeature          string     `json:"missing_feature"`
	RecencyAnchor           string     `json:"recency_anchor"`
	RecencyTauNS            int64      `json:"recency_tau_ns"`
	BaseEqualRank           string     `json:"base_equal_rank"`
	FeatureEqualRank        string     `json:"feature_equal_rank"`
	FinalTie                string     `json:"final_tie"`
	Fallback                string     `json:"fallback"`
	Atol                    float64    `json:"atol"`
	Rtol                    float64    `json:"rtol"`
	InheritedB2ConfigSHA256 *string    `json:"inherited_b2_config_sha256"`
	Ablation                string     `json:"ablation"`
}

// ReceiptIndexV2HashProjection is a closed contract record. All JSON fields are required.
type ReceiptIndexV2HashProjection struct {
	StreamID             string                `json:"stream_id"`
	ClockDomain          string                `json:"clock_domain"`
	Cursor               StreamCursorV2        `json:"cursor"`
	Receipts             []ReceiptV2           `json:"receipts"`
	EvidenceConsumptions []EvidenceConsumption `json:"evidence_consumptions"`
	LifecycleFence       LifecycleFence        `json:"lifecycle_fence"`
}

// ReceiptV2HashProjection is a closed contract record. All JSON fields are required.
type ReceiptV2HashProjection struct {
	StreamID                  string             `json:"stream_id"`
	EventID                   string             `json:"event_id"`
	PayloadSHA256             string             `json:"payload_sha256"`
	EvidenceConsumptionSHA256 *string            `json:"evidence_consumption_sha256"`
	Key                       DynamicsKeyV2      `json:"key"`
	AcceptedAtNS              int64              `json:"accepted_at_ns"`
	AcceptedSeq               int64              `json:"accepted_seq"`
	Disposition               ReceiptDisposition `json:"disposition"`
	BeforeStateRevision       int64              `json:"before_state_revision"`
	AfterStateRevision        int64              `json:"after_state_revision"`
	Reason                    Reason             `json:"reason"`
	DuplicateOfReceiptSHA256  *string            `json:"duplicate_of_receipt_sha256"`
}

// RegistryBoundRankerProjectionV2 is a closed contract record. All JSON fields are required.
type RegistryBoundRankerProjectionV2 struct {
	PolicyID                PolicyID   `json:"policy_id"`
	PolicyVersion           string     `json:"policy_version"`
	DynamicsConfigSHA256    *string    `json:"dynamics_config_sha256"`
	FeatureContractSHA256   *string    `json:"feature_contract_sha256"`
	OuterOffset             int64      `json:"outer_offset"`
	Lanes                   []RankLane `json:"lanes"`
	MissingState            string     `json:"missing_state"`
	MissingFeature          string     `json:"missing_feature"`
	RecencyAnchor           string     `json:"recency_anchor"`
	RecencyTauNS            int64      `json:"recency_tau_ns"`
	BaseEqualRank           string     `json:"base_equal_rank"`
	FeatureEqualRank        string     `json:"feature_equal_rank"`
	FinalTie                string     `json:"final_tie"`
	Fallback                string     `json:"fallback"`
	Atol                    float64    `json:"atol"`
	Rtol                    float64    `json:"rtol"`
	InheritedB2ConfigSHA256 *string    `json:"inherited_b2_config_sha256"`
	Ablation                string     `json:"ablation"`
	ConfigSHA256            string     `json:"config_sha256"`
}

// RegistryPolicyEntryProjectionV2 is a closed contract record. All JSON fields are required.
type RegistryPolicyEntryProjectionV2 struct {
	PolicyID            PolicyID                        `json:"policy_id"`
	Version             string                          `json:"version"`
	ImplementedMode     string                          `json:"implemented_mode"`
	UsesOutcomes        bool                            `json:"uses_outcomes"`
	UsesElapsedDecay    bool                            `json:"uses_elapsed_decay"`
	AllowedRoles        []InputRole                     `json:"allowed_roles"`
	Dynamics            *DynamicsConfigV2               `json:"dynamics"`
	Ranker              RegistryBoundRankerProjectionV2 `json:"ranker"`
	RuleSHA256          string                          `json:"rule_sha256"`
	ParameterProvenance string                          `json:"parameter_provenance"`
}

// SnapshotV2HashProjection is a closed contract record. All JSON fields are required.
type SnapshotV2HashProjection struct {
	Protocol     string         `json:"protocol"`
	Scope        ScopeV2        `json:"scope"`
	Dimension    Dimension      `json:"dimension"`
	ConfigSHA256 string         `json:"config_sha256"`
	Cursor       StreamCursorV2 `json:"cursor"`
	States       []StateV2      `json:"states"`
	Receipts     ReceiptIndexV2 `json:"receipts"`
	StreamID     string         `json:"stream_id"`
}

// StateV2HashProjection is a closed contract record. All JSON fields are required.
type StateV2HashProjection struct {
	Key                     DynamicsKeyV2 `json:"key"`
	Mode                    string        `json:"mode"`
	Value                   float64       `json:"value"`
	StateRevision           int64         `json:"state_revision"`
	ActivatedAtNS           *int64        `json:"activated_at_ns"`
	ActivationAcceptedSeq   *int64        `json:"activation_accepted_seq"`
	ActivationEventID       *string       `json:"activation_event_id"`
	ActivationPayloadSHA256 *string       `json:"activation_payload_sha256"`
	LastAppliedAtNS         *int64        `json:"last_applied_at_ns"`
	LastAppliedSeq          *int64        `json:"last_applied_seq"`
	Status                  string        `json:"status"`
	Reason                  Reason        `json:"reason"`
}

// StreamAcceptedStepProjectionV2 is a closed contract record. All JSON fields are required.
type StreamAcceptedStepProjectionV2 struct {
	PreviousPrefixSHA256 string `json:"previous_prefix_sha256"`
	StreamID             string `json:"stream_id"`
	ClockDomain          string `json:"clock_domain"`
	AcceptedSeq          int64  `json:"accepted_seq"`
	AcceptedAtNS         int64  `json:"accepted_at_ns"`
	PayloadSHA256        string `json:"payload_sha256"`
}

// StreamInitialPrefixProjectionV2 is a closed contract record. All JSON fields are required.
type StreamInitialPrefixProjectionV2 struct {
	StreamID     string    `json:"stream_id"`
	ClockDomain  string    `json:"clock_domain"`
	Scope        ScopeV2   `json:"scope"`
	Dimension    Dimension `json:"dimension"`
	ConfigSHA256 string    `json:"config_sha256"`
}

// TransitionV2HashProjection is a closed contract record. All JSON fields are required.
type TransitionV2HashProjection struct {
	BeforeStateSHA256  string             `json:"before_state_sha256"`
	State              StateV2            `json:"state"`
	Receipts           ReceiptIndexV2     `json:"receipts"`
	Receipt            ReceiptV2          `json:"receipt"`
	Cursor             StreamCursorV2     `json:"cursor"`
	AttemptDisposition AttemptDisposition `json:"attempt_disposition"`
}

func (e *ContractErrorV2) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code) + " at " + e.Path
}

type fieldRule struct {
	name, kind, ref, constant string
	nullable, hasConstant     bool
	integer                   int64
	enums                     []string
	item                      *fieldRule
	maxItems                  int
}

type objectRule struct {
	fields []fieldRule
	typ    reflect.Type
}
type numberRule struct {
	kind             string
	minimum, maximum float64
}

// fixedObjects is compiled data, never loaded from caller-provided schema.
var fixedObjects = map[string]objectRule{
	"ContractErrorV2": {typ: reflect.TypeOf(ContractErrorV2{}), fields: []fieldRule{
		{name: "code", kind: "ref", nullable: false, ref: "V2ErrorCode"},
		{name: "path", kind: "ref", nullable: false, ref: "SchemaPath"},
	}},
	"DynamicInputManifestV2": {typ: reflect.TypeOf(DynamicInputManifestV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-input-manifest-v2"},
		{name: "dataset_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "baseline_bundle_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "policy_contract_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "files", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RoleFile"}},
		{name: "manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"DynamicsConfigV2": {typ: reflect.TypeOf(DynamicsConfigV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-config-v2"},
		{name: "version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "mode", kind: "string", nullable: false, hasConstant: true, constant: "scalar"},
		{name: "clock", kind: "string", nullable: false, hasConstant: true, constant: "accepted_time"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "initial", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "tau_ns", kind: "ref", nullable: false, ref: "PositiveNS"},
		{name: "eta_positive", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "eta_negative", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "unit_strength", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "negative_policy", kind: "string", nullable: false, hasConstant: true, constant: "only_memory_attributable_independent"},
		{name: "decay_enabled", kind: "ref", nullable: false, ref: "Bool"},
		{name: "outcome_jump_enabled", kind: "ref", nullable: false, ref: "Bool"},
		{name: "atol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "rtol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "evidence_rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"DynamicsKeyV2": {typ: reflect.TypeOf(DynamicsKeyV2{}), fields: []fieldRule{
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "target", kind: "ref", nullable: false, ref: "RefV2"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "contract_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"DynamicsRunConfigV2": {typ: reflect.TypeOf(DynamicsRunConfigV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-run-config-v2"},
		{name: "run_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "input_manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "registry_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "policy_ids", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "PolicyID"}},
		{name: "configs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "PolicyConfig"}},
		{name: "resource_limits", kind: "ref", nullable: false, ref: "ResourceLimits"},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "registry", kind: "ref", nullable: false, ref: "PolicyRegistryV2"},
	}},
	"EventFileV2": {typ: reflect.TypeOf(EventFileV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-event-file-v2"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "events", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EventV2"}},
	}},
	"EventV2": {typ: reflect.TypeOf(EventV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-event-v2"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "event_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "transport_attempt_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "key", kind: "ref", nullable: false, ref: "DynamicsKeyV2"},
		{name: "kind", kind: "ref", nullable: false, ref: "EventKind"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "observed_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "arrived_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "accepted_at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "task", kind: "ref", nullable: true, ref: "TaskBinding"},
		{name: "evidence", kind: "ref", nullable: true, ref: "EvidenceBinding"},
		{name: "outcome", kind: "ref", nullable: false, ref: "Outcome"},
		{name: "attribution", kind: "ref", nullable: false, ref: "Attribution"},
		{name: "strength", kind: "ref", nullable: true, ref: "UnitFloat"},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
		{name: "payload_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"ImportanceFeatureV1": {typ: reflect.TypeOf(ImportanceFeatureV1{}), fields: []fieldRule{
		{name: "feature_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "target", kind: "ref", nullable: false, ref: "RefV2"},
		{name: "source", kind: "ref", nullable: false, ref: "EvidenceRef"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "observed_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "available_at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "available_accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "feature_name", kind: "string", nullable: false, hasConstant: true, constant: "importance"},
		{name: "feature_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "generator_config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "producer_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "input_refs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EvidenceRef"}},
		{name: "status", kind: "string", nullable: false, enums: []string{"present", "missing"}},
		{name: "value", kind: "ref", nullable: true, ref: "UnitFloat"},
		{name: "unit", kind: "string", nullable: false, hasConstant: true, constant: "dimensionless"},
		{name: "missing_reason", kind: "ref", nullable: true, ref: "Reason"},
		{name: "feature_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"P1FeatureFileV1": {typ: reflect.TypeOf(P1FeatureFileV1{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "p1-feature-file-v1"},
		{name: "feature_contract_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "features", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "ImportanceFeatureV1"}},
	}},
	"P1InputManifestV1": {typ: reflect.TypeOf(P1InputManifestV1{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "p1-input-manifest-v1"},
		{name: "base_dynamic_manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "feature_contract_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "feature_file", kind: "ref", nullable: false, ref: "RoleFile"},
		{name: "manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"PolicyDecisionV2": {typ: reflect.TypeOf(PolicyDecisionV2{}), fields: []fieldRule{
		{name: "query_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "policy_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "candidate_snapshot_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "prefix_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "ranker_config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "ranks", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RankRow"}},
		{name: "selected", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RefV2"}},
		{name: "context_cost_used", kind: "ref", nullable: false, ref: "Count"},
		{name: "context_budget", kind: "ref", nullable: false, ref: "LegacyContextBudget"},
		{name: "status", kind: "string", nullable: false, enums: []string{"ok", "fallback"}},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
		{name: "decision_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"PolicyRegistryV2": {typ: reflect.TypeOf(PolicyRegistryV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-policy-registry-v2"},
		{name: "entries", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "PolicyEntry"}},
		{name: "resource_limits", kind: "ref", nullable: false, ref: "ResourceLimits"},
		{name: "registry_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"PredictionRunV2": {typ: reflect.TypeOf(PredictionRunV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-prediction-run-v2"},
		{name: "run_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "policy_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "input_manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "run_config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "registry_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "predictions", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "PredictionV2"}},
		{name: "cost", kind: "ref", nullable: false, ref: "OfflineCost"},
		{name: "run_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"PredictionV2": {typ: reflect.TypeOf(PredictionV2{}), fields: []fieldRule{
		{name: "query_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "status", kind: "string", nullable: false, enums: []string{"ok", "fallback", "failed"}},
		{name: "decision", kind: "ref", nullable: true, ref: "PolicyDecisionV2"},
		{name: "error", kind: "ref", nullable: true, ref: "ContractErrorV2"},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
	}},
	"ProbeFileV2": {typ: reflect.TypeOf(ProbeFileV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-probe-file-v2"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "probes", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "ProbeV2"}},
	}},
	"ProbeV2": {typ: reflect.TypeOf(ProbeV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-probe-v2"},
		{name: "query_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "cutoff_accepted_seq", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "candidate_snapshot_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "candidate_snapshot_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "prefix_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "final_k", kind: "ref", nullable: false, ref: "FinalK"},
		{name: "context_budget", kind: "ref", nullable: false, ref: "LegacyContextBudget"},
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "policy_version", kind: "ref", nullable: false, ref: "LegacyID"},
	}},
	"RankerConfigV2": {typ: reflect.TypeOf(RankerConfigV2{}), fields: []fieldRule{
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "policy_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "registry_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "dynamics_config_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "feature_contract_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "outer_offset", kind: "int64", nullable: false, hasConstant: true, integer: 60},
		{name: "lanes", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RankLane"}},
		{name: "missing_state", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "missing_feature", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "recency_anchor", kind: "string", nullable: false, hasConstant: true, constant: "updated_at_ns"},
		{name: "recency_tau_ns", kind: "ref", nullable: false, ref: "PositiveNS"},
		{name: "base_equal_rank", kind: "string", nullable: false, hasConstant: true, constant: "exact_score_dense"},
		{name: "feature_equal_rank", kind: "string", nullable: false, hasConstant: true, constant: "exact_value_dense"},
		{name: "final_tie", kind: "string", nullable: false, hasConstant: true, constant: "trust_updated_key"},
		{name: "fallback", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "atol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "rtol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "inherited_b2_config_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "ablation", kind: "string", nullable: false, enums: []string{"none", "disable_decay", "disable_outcome_jumps"}},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"ReceiptIndexV2": {typ: reflect.TypeOf(ReceiptIndexV2{}), fields: []fieldRule{
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "cursor", kind: "ref", nullable: false, ref: "StreamCursorV2"},
		{name: "receipts", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "ReceiptV2"}},
		{name: "evidence_consumptions", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EvidenceConsumption"}},
		{name: "lifecycle_fence", kind: "ref", nullable: false, ref: "LifecycleFence"},
		{name: "index_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"ReceiptV2": {typ: reflect.TypeOf(ReceiptV2{}), fields: []fieldRule{
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "event_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "payload_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "evidence_consumption_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "key", kind: "ref", nullable: false, ref: "DynamicsKeyV2"},
		{name: "accepted_at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "disposition", kind: "ref", nullable: false, ref: "ReceiptDisposition"},
		{name: "before_state_revision", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "after_state_revision", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
		{name: "receipt_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "duplicate_of_receipt_sha256", kind: "ref", nullable: true, ref: "SHA"},
	}},
	"RefV2": {typ: reflect.TypeOf(RefV2{}), fields: []fieldRule{
		{name: "origin", kind: "string", nullable: false, enums: []string{"memory", "fixture_alt"}},
		{name: "collection", kind: "string", nullable: false, hasConstant: true, constant: "memories"},
		{name: "id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "revision", kind: "ref", nullable: false, ref: "Seq"},
		{name: "content_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"ReplayStatsV2": {typ: reflect.TypeOf(ReplayStatsV2{}), fields: []fieldRule{
		{name: "input_count", kind: "ref", nullable: false, ref: "Count"},
		{name: "applied_count", kind: "ref", nullable: false, ref: "Count"},
		{name: "duplicate_event_count", kind: "ref", nullable: false, ref: "Count"},
		{name: "duplicate_evidence_count", kind: "ref", nullable: false, ref: "Count"},
		{name: "non_learning_count", kind: "ref", nullable: false, ref: "Count"},
		{name: "rejected_count", kind: "ref", nullable: false, ref: "Count"},
		{name: "error", kind: "ref", nullable: true, ref: "ContractErrorV2"},
	}},
	"ScopeV2": {typ: reflect.TypeOf(ScopeV2{}), fields: []fieldRule{
		{name: "workspace", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "project", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "user", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "agent", kind: "ref", nullable: false, ref: "LegacyID"},
	}},
	"SnapshotFileV2": {typ: reflect.TypeOf(SnapshotFileV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-snapshot-file-v2"},
		{name: "snapshot", kind: "ref", nullable: false, ref: "SnapshotV2"},
	}},
	"SnapshotV2": {typ: reflect.TypeOf(SnapshotV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-snapshot-v2"},
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "cursor", kind: "ref", nullable: false, ref: "StreamCursorV2"},
		{name: "states", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "StateV2"}},
		{name: "receipts", kind: "ref", nullable: false, ref: "ReceiptIndexV2"},
		{name: "snapshot_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
	}},
	"StateV2": {typ: reflect.TypeOf(StateV2{}), fields: []fieldRule{
		{name: "key", kind: "ref", nullable: false, ref: "DynamicsKeyV2"},
		{name: "mode", kind: "string", nullable: false, hasConstant: true, constant: "scalar"},
		{name: "value", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "state_revision", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "activated_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "activation_accepted_seq", kind: "ref", nullable: true, ref: "Seq"},
		{name: "activation_event_id", kind: "ref", nullable: true, ref: "LegacyID"},
		{name: "activation_payload_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "last_applied_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "last_applied_seq", kind: "ref", nullable: true, ref: "Seq"},
		{name: "status", kind: "string", nullable: false, enums: []string{"pending_activation", "active", "invalidated"}},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
		{name: "state_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"StateViewV2": {typ: reflect.TypeOf(StateViewV2{}), fields: []fieldRule{
		{name: "key", kind: "ref", nullable: false, ref: "DynamicsKeyV2"},
		{name: "at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "cutoff_accepted_seq", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "snapshot_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "snapshot_cursor", kind: "ref", nullable: false, ref: "StreamCursorV2"},
		{name: "source_state_revision", kind: "ref", nullable: false, ref: "Seq"},
		{name: "activated_at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "activation_accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "value", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "status", kind: "string", nullable: false, enums: []string{"present", "invalidated"}},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"StreamCursorV2": {typ: reflect.TypeOf(StreamCursorV2{}), fields: []fieldRule{
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "accepted_at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "accepted_seq", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "prefix_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"TransitionV2": {typ: reflect.TypeOf(TransitionV2{}), fields: []fieldRule{
		{name: "before_state_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "state", kind: "ref", nullable: false, ref: "StateV2"},
		{name: "receipts", kind: "ref", nullable: false, ref: "ReceiptIndexV2"},
		{name: "receipt", kind: "ref", nullable: false, ref: "ReceiptV2"},
		{name: "cursor", kind: "ref", nullable: false, ref: "StreamCursorV2"},
		{name: "transition_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "attempt_disposition", kind: "ref", nullable: false, ref: "AttemptDisposition"},
	}},
	"AuxRank": {typ: reflect.TypeOf(AuxRank{}), fields: []fieldRule{
		{name: "kind", kind: "string", nullable: false, enums: []string{"recency", "state", "importance"}},
		{name: "value", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "dense_rank", kind: "ref", nullable: false, ref: "Seq"},
	}},
	"CLIStatus": {typ: reflect.TypeOf(CLIStatus{}), fields: []fieldRule{
		{name: "status", kind: "string", nullable: false, enums: []string{"completed", "completed_with_failures", "failed"}},
		{name: "run_id", kind: "ref", nullable: true, ref: "LegacyID"},
		{name: "outputs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "OutputRoleFile"}, maxItems: 8},
		{name: "error", kind: "ref", nullable: true, ref: "ContractErrorV2"},
	}},
	"CompareResult": {typ: reflect.TypeOf(CompareResult{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-compare-v2"},
		{name: "run_ids", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "LegacyID"}},
		{name: "registry_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "equal_conditions", kind: "ref", nullable: false, ref: "Bool"},
		{name: "checks", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "ComparisonCheck"}},
		{name: "statuses", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "ComparedStatus"}},
	}},
	"ComparedStatus": {typ: reflect.TypeOf(ComparedStatus{}), fields: []fieldRule{
		{name: "run_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "planned", kind: "ref", nullable: false, ref: "Count"},
		{name: "ok", kind: "ref", nullable: false, ref: "Count"},
		{name: "failed", kind: "ref", nullable: false, ref: "Count"},
		{name: "fallback", kind: "ref", nullable: false, ref: "Count"},
	}},
	"ComparisonCheck": {typ: reflect.TypeOf(ComparisonCheck{}), fields: []fieldRule{
		{name: "name", kind: "string", nullable: false, enums: []string{"candidates", "events", "scope", "clock", "cutoff", "budget", "registry", "features", "source_snapshot"}},
		{name: "equal", kind: "ref", nullable: false, ref: "Bool"},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
	}},
	"EvidenceBinding": {typ: reflect.TypeOf(EvidenceBinding{}), fields: []fieldRule{
		{name: "refs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EvidenceRef"}},
		{name: "target", kind: "ref", nullable: false, ref: "RefV2"},
		{name: "task_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "independence", kind: "string", nullable: false, enums: []string{"synthetic_independent", "unknown"}},
		{name: "available_accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "source_status", kind: "string", nullable: false, enums: []string{"available", "deleted", "revoked", "unknown"}},
		{name: "binding_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"EvidenceConsumption": {typ: reflect.TypeOf(EvidenceConsumption{}), fields: []fieldRule{
		{name: "identity_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "claim_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "first_event_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "first_receipt_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"EvidenceRef": {typ: reflect.TypeOf(EvidenceRef{}), fields: []fieldRule{
		{name: "namespace", kind: "string", nullable: false, hasConstant: true, constant: "fixture"},
		{name: "collection", kind: "string", nullable: false, enums: []string{"sources", "evaluations", "eval_tasks", "eval_runs", "eval_trials", "judge_calibrations", "traces"}},
		{name: "id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "revision", kind: "ref", nullable: false, ref: "Seq"},
		{name: "content_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"FenceEntry": {typ: reflect.TypeOf(FenceEntry{}), fields: []fieldRule{
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "target", kind: "ref", nullable: false, ref: "RefV2"},
		{name: "status", kind: "string", nullable: false, enums: []string{"current", "invalidated"}},
		{name: "source_refs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EvidenceRef"}},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
	}},
	"LifecycleFence": {typ: reflect.TypeOf(LifecycleFence{}), fields: []fieldRule{
		{name: "namespace", kind: "string", nullable: false, hasConstant: true, constant: "synthetic"},
		{name: "epoch", kind: "ref", nullable: false, ref: "Seq"},
		{name: "minimum_accepted_seq", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "entries", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "FenceEntry"}},
		{name: "fence_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"OfflineCost": {typ: reflect.TypeOf(OfflineCost{}), fields: []fieldRule{
		{name: "event_inputs", kind: "ref", nullable: false, ref: "Count"},
		{name: "unique_events", kind: "ref", nullable: false, ref: "Count"},
		{name: "state_updates", kind: "ref", nullable: false, ref: "Count"},
		{name: "duplicate_events", kind: "ref", nullable: false, ref: "Count"},
		{name: "duplicate_evidence", kind: "ref", nullable: false, ref: "Count"},
		{name: "feature_reads", kind: "ref", nullable: false, ref: "Count"},
		{name: "failed_predictions", kind: "ref", nullable: false, ref: "Count"},
		{name: "fallback_predictions", kind: "ref", nullable: false, ref: "Count"},
		{name: "elapsed_wall_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "provider_usd", kind: "null", nullable: true},
	}},
	"OutputManifest": {typ: reflect.TypeOf(OutputManifest{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-output-manifest-v2"},
		{name: "run_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "input_manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "run_config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "run_config_raw_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "registry_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "status", kind: "ref", nullable: false, ref: "OutputStatus"},
		{name: "planned_rows", kind: "ref", nullable: false, ref: "Count"},
		{name: "failed_rows", kind: "ref", nullable: false, ref: "Count"},
		{name: "files", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "OutputRoleFile"}, maxItems: 7},
	}},
	"OutputRoleFile": {typ: reflect.TypeOf(OutputRoleFile{}), fields: []fieldRule{
		{name: "role", kind: "ref", nullable: false, ref: "OutputRole"},
		{name: "relative_path", kind: "ref", nullable: false, ref: "SafePath"},
		{name: "raw_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "size_bytes", kind: "ref", nullable: false, ref: "Count"},
	}},
	"PolicyConfig": {typ: reflect.TypeOf(PolicyConfig{}), fields: []fieldRule{
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "dynamics", kind: "ref", nullable: true, ref: "DynamicsConfigV2"},
		{name: "ranker", kind: "ref", nullable: false, ref: "RankerConfigV2"},
	}},
	"PolicyEntry": {typ: reflect.TypeOf(PolicyEntry{}), fields: []fieldRule{
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "implemented_mode", kind: "string", nullable: false, enums: []string{"none", "scalar"}},
		{name: "uses_outcomes", kind: "ref", nullable: false, ref: "Bool"},
		{name: "uses_elapsed_decay", kind: "ref", nullable: false, ref: "Bool"},
		{name: "allowed_roles", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "InputRole"}},
		{name: "dynamics", kind: "ref", nullable: true, ref: "DynamicsConfigV2"},
		{name: "ranker", kind: "ref", nullable: false, ref: "RankerConfigV2"},
		{name: "rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "parameter_provenance", kind: "string", nullable: false, hasConstant: true, constant: "engineering_regression_only"},
	}},
	"RankLane": {typ: reflect.TypeOf(RankLane{}), fields: []fieldRule{
		{name: "kind", kind: "string", nullable: false, enums: []string{"relevance", "recency", "state", "importance"}},
		{name: "weight", kind: "ref", nullable: false, ref: "UnitFloat"},
	}},
	"RankRow": {typ: reflect.TypeOf(RankRow{}), fields: []fieldRule{
		{name: "ref", kind: "ref", nullable: false, ref: "RefV2"},
		{name: "base_rrf_score", kind: "ref", nullable: false, ref: "NonnegativeFloat"},
		{name: "base_dense_rank", kind: "ref", nullable: false, ref: "Seq"},
		{name: "auxiliary", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "AuxRank"}},
		{name: "fusion_score", kind: "ref", nullable: false, ref: "NonnegativeFloat"},
		{name: "final_ordinal", kind: "ref", nullable: false, ref: "Seq"},
	}},
	"ResourceLimits": {typ: reflect.TypeOf(ResourceLimits{}), fields: []fieldRule{
		{name: "max_file_bytes", kind: "ref", nullable: false, ref: "PositiveInt64"},
		{name: "max_events", kind: "ref", nullable: false, ref: "PositiveInt64"},
		{name: "max_states", kind: "ref", nullable: false, ref: "PositiveInt64"},
		{name: "max_receipts", kind: "ref", nullable: false, ref: "PositiveInt64"},
		{name: "max_probes", kind: "ref", nullable: false, ref: "PositiveInt64"},
		{name: "max_features", kind: "ref", nullable: false, ref: "PositiveInt64"},
		{name: "max_array_items", kind: "ref", nullable: false, ref: "PositiveInt64"},
		{name: "max_string_bytes", kind: "ref", nullable: false, ref: "PositiveInt64"},
		{name: "max_json_depth", kind: "ref", nullable: false, ref: "PositiveInt64"},
	}},
	"RoleFile": {typ: reflect.TypeOf(RoleFile{}), fields: []fieldRule{
		{name: "role", kind: "ref", nullable: false, ref: "InputRole"},
		{name: "relative_path", kind: "ref", nullable: false, ref: "SafePath"},
		{name: "raw_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "size_bytes", kind: "ref", nullable: false, ref: "Count"},
	}},
	"TaskBinding": {typ: reflect.TypeOf(TaskBinding{}), fields: []fieldRule{
		{name: "task_ref", kind: "ref", nullable: false, ref: "EvidenceRef"},
		{name: "run_ref", kind: "ref", nullable: false, ref: "EvidenceRef"},
		{name: "trial_ref", kind: "ref", nullable: false, ref: "EvidenceRef"},
		{name: "checker_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "qualification", kind: "string", nullable: false, enums: []string{"synthetic_independent", "unknown"}},
		{name: "task_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"DynamicInputManifestV2HashProjection": {typ: reflect.TypeOf(DynamicInputManifestV2HashProjection{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-input-manifest-v2"},
		{name: "dataset_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "baseline_bundle_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "policy_contract_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "files", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RoleFile"}},
	}},
	"DynamicsConfigV2HashProjection": {typ: reflect.TypeOf(DynamicsConfigV2HashProjection{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-config-v2"},
		{name: "version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "mode", kind: "string", nullable: false, hasConstant: true, constant: "scalar"},
		{name: "clock", kind: "string", nullable: false, hasConstant: true, constant: "accepted_time"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "initial", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "tau_ns", kind: "ref", nullable: false, ref: "PositiveNS"},
		{name: "eta_positive", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "eta_negative", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "unit_strength", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "negative_policy", kind: "string", nullable: false, hasConstant: true, constant: "only_memory_attributable_independent"},
		{name: "decay_enabled", kind: "ref", nullable: false, ref: "Bool"},
		{name: "outcome_jump_enabled", kind: "ref", nullable: false, ref: "Bool"},
		{name: "atol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "rtol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "evidence_rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"DynamicsRunConfigV2HashProjection": {typ: reflect.TypeOf(DynamicsRunConfigV2HashProjection{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-run-config-v2"},
		{name: "run_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "input_manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "registry_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "policy_ids", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "PolicyID"}},
		{name: "configs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "PolicyConfig"}},
		{name: "resource_limits", kind: "ref", nullable: false, ref: "ResourceLimits"},
		{name: "registry", kind: "ref", nullable: false, ref: "PolicyRegistryV2"},
	}},
	"EventLogicalProjectionV2": {typ: reflect.TypeOf(EventLogicalProjectionV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-event-v2"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "event_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "key", kind: "ref", nullable: false, ref: "DynamicsKeyV2"},
		{name: "kind", kind: "ref", nullable: false, ref: "EventKind"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "observed_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "task", kind: "ref", nullable: true, ref: "TaskBinding"},
		{name: "evidence", kind: "ref", nullable: true, ref: "EvidenceBinding"},
		{name: "outcome", kind: "ref", nullable: false, ref: "Outcome"},
		{name: "attribution", kind: "ref", nullable: false, ref: "Attribution"},
		{name: "strength", kind: "ref", nullable: true, ref: "UnitFloat"},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
	}},
	"EvidenceBindingHashProjection": {typ: reflect.TypeOf(EvidenceBindingHashProjection{}), fields: []fieldRule{
		{name: "refs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EvidenceRef"}},
		{name: "target", kind: "ref", nullable: false, ref: "RefV2"},
		{name: "task_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "independence", kind: "string", nullable: false, enums: []string{"synthetic_independent", "unknown"}},
		{name: "available_accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "source_status", kind: "string", nullable: false, enums: []string{"available", "deleted", "revoked", "unknown"}},
	}},
	"EvidenceClaimProjectionV2": {typ: reflect.TypeOf(EvidenceClaimProjectionV2{}), fields: []fieldRule{
		{name: "outcome", kind: "ref", nullable: false, ref: "Outcome"},
		{name: "attribution", kind: "ref", nullable: false, ref: "Attribution"},
		{name: "strength", kind: "ref", nullable: true, ref: "UnitFloat"},
		{name: "observed_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "task", kind: "ref", nullable: false, ref: "TaskBinding"},
		{name: "evidence", kind: "ref", nullable: false, ref: "EvidenceBinding"},
	}},
	"EvidenceIdentityProjectionV2": {typ: reflect.TypeOf(EvidenceIdentityProjectionV2{}), fields: []fieldRule{
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "target", kind: "ref", nullable: false, ref: "RefV2"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "task_ref", kind: "ref", nullable: false, ref: "EvidenceRef"},
		{name: "run_ref", kind: "ref", nullable: false, ref: "EvidenceRef"},
		{name: "trial_ref", kind: "ref", nullable: false, ref: "EvidenceRef"},
		{name: "checker_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "evidence_refs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EvidenceRef"}, maxItems: 200000},
	}},
	"ImportanceFeatureV1HashProjection": {typ: reflect.TypeOf(ImportanceFeatureV1HashProjection{}), fields: []fieldRule{
		{name: "feature_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "target", kind: "ref", nullable: false, ref: "RefV2"},
		{name: "source", kind: "ref", nullable: false, ref: "EvidenceRef"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "observed_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "available_at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "available_accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "feature_name", kind: "string", nullable: false, hasConstant: true, constant: "importance"},
		{name: "feature_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "generator_config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "producer_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "input_refs", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EvidenceRef"}},
		{name: "status", kind: "string", nullable: false, enums: []string{"present", "missing"}},
		{name: "value", kind: "ref", nullable: true, ref: "UnitFloat"},
		{name: "unit", kind: "string", nullable: false, hasConstant: true, constant: "dimensionless"},
		{name: "missing_reason", kind: "ref", nullable: true, ref: "Reason"},
	}},
	"LifecycleFenceHashProjection": {typ: reflect.TypeOf(LifecycleFenceHashProjection{}), fields: []fieldRule{
		{name: "namespace", kind: "string", nullable: false, hasConstant: true, constant: "synthetic"},
		{name: "epoch", kind: "ref", nullable: false, ref: "Seq"},
		{name: "minimum_accepted_seq", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "entries", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "FenceEntry"}},
	}},
	"P1InputManifestV1HashProjection": {typ: reflect.TypeOf(P1InputManifestV1HashProjection{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "p1-input-manifest-v1"},
		{name: "base_dynamic_manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "feature_contract_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "feature_file", kind: "ref", nullable: false, ref: "RoleFile"},
	}},
	"PolicyDecisionV2HashProjection": {typ: reflect.TypeOf(PolicyDecisionV2HashProjection{}), fields: []fieldRule{
		{name: "query_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "policy_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "candidate_snapshot_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "prefix_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "ranker_config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "ranks", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RankRow"}},
		{name: "selected", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RefV2"}},
		{name: "context_cost_used", kind: "ref", nullable: false, ref: "Count"},
		{name: "context_budget", kind: "ref", nullable: false, ref: "LegacyContextBudget"},
		{name: "status", kind: "string", nullable: false, enums: []string{"ok", "fallback"}},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
	}},
	"PolicyRegistryGraphProjectionV2": {typ: reflect.TypeOf(PolicyRegistryGraphProjectionV2{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-policy-registry-v2"},
		{name: "entries", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "projection_ref", nullable: false, ref: "RegistryPolicyEntryProjectionV2"}},
		{name: "resource_limits", kind: "ref", nullable: false, ref: "ResourceLimits"},
	}},
	"PredictionRunV2HashProjection": {typ: reflect.TypeOf(PredictionRunV2HashProjection{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-prediction-run-v2"},
		{name: "run_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "policy_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "input_manifest_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "run_config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "registry_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "predictions", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "PredictionV2"}},
		{name: "cost", kind: "ref", nullable: false, ref: "OfflineCost"},
	}},
	"RankerConfigParametersProjectionV2": {typ: reflect.TypeOf(RankerConfigParametersProjectionV2{}), fields: []fieldRule{
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "policy_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "dynamics_config_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "feature_contract_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "outer_offset", kind: "int64", nullable: false, hasConstant: true, integer: 60},
		{name: "lanes", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RankLane"}},
		{name: "missing_state", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "missing_feature", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "recency_anchor", kind: "string", nullable: false, hasConstant: true, constant: "updated_at_ns"},
		{name: "recency_tau_ns", kind: "ref", nullable: false, ref: "PositiveNS"},
		{name: "base_equal_rank", kind: "string", nullable: false, hasConstant: true, constant: "exact_score_dense"},
		{name: "feature_equal_rank", kind: "string", nullable: false, hasConstant: true, constant: "exact_value_dense"},
		{name: "final_tie", kind: "string", nullable: false, hasConstant: true, constant: "trust_updated_key"},
		{name: "fallback", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "atol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "rtol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "inherited_b2_config_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "ablation", kind: "string", nullable: false, enums: []string{"none", "disable_decay", "disable_outcome_jumps"}},
	}},
	"ReceiptIndexV2HashProjection": {typ: reflect.TypeOf(ReceiptIndexV2HashProjection{}), fields: []fieldRule{
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "cursor", kind: "ref", nullable: false, ref: "StreamCursorV2"},
		{name: "receipts", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "ReceiptV2"}},
		{name: "evidence_consumptions", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "EvidenceConsumption"}},
		{name: "lifecycle_fence", kind: "ref", nullable: false, ref: "LifecycleFence"},
	}},
	"ReceiptV2HashProjection": {typ: reflect.TypeOf(ReceiptV2HashProjection{}), fields: []fieldRule{
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "event_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "payload_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "evidence_consumption_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "key", kind: "ref", nullable: false, ref: "DynamicsKeyV2"},
		{name: "accepted_at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "disposition", kind: "ref", nullable: false, ref: "ReceiptDisposition"},
		{name: "before_state_revision", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "after_state_revision", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
		{name: "duplicate_of_receipt_sha256", kind: "ref", nullable: true, ref: "SHA"},
	}},
	"RegistryBoundRankerProjectionV2": {typ: reflect.TypeOf(RegistryBoundRankerProjectionV2{}), fields: []fieldRule{
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "policy_version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "dynamics_config_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "feature_contract_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "outer_offset", kind: "int64", nullable: false, hasConstant: true, integer: 60},
		{name: "lanes", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "RankLane"}},
		{name: "missing_state", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "missing_feature", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "recency_anchor", kind: "string", nullable: false, hasConstant: true, constant: "updated_at_ns"},
		{name: "recency_tau_ns", kind: "ref", nullable: false, ref: "PositiveNS"},
		{name: "base_equal_rank", kind: "string", nullable: false, hasConstant: true, constant: "exact_score_dense"},
		{name: "feature_equal_rank", kind: "string", nullable: false, hasConstant: true, constant: "exact_value_dense"},
		{name: "final_tie", kind: "string", nullable: false, hasConstant: true, constant: "trust_updated_key"},
		{name: "fallback", kind: "string", nullable: false, hasConstant: true, constant: "error"},
		{name: "atol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "rtol", kind: "ref", nullable: false, ref: "Tolerance"},
		{name: "inherited_b2_config_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "ablation", kind: "string", nullable: false, enums: []string{"none", "disable_decay", "disable_outcome_jumps"}},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"RegistryPolicyEntryProjectionV2": {typ: reflect.TypeOf(RegistryPolicyEntryProjectionV2{}), fields: []fieldRule{
		{name: "policy_id", kind: "ref", nullable: false, ref: "PolicyID"},
		{name: "version", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "implemented_mode", kind: "string", nullable: false, enums: []string{"none", "scalar"}},
		{name: "uses_outcomes", kind: "ref", nullable: false, ref: "Bool"},
		{name: "uses_elapsed_decay", kind: "ref", nullable: false, ref: "Bool"},
		{name: "allowed_roles", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "InputRole"}},
		{name: "dynamics", kind: "ref", nullable: true, ref: "DynamicsConfigV2"},
		{name: "ranker", kind: "projection_ref", nullable: false, ref: "RegistryBoundRankerProjectionV2"},
		{name: "rule_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "parameter_provenance", kind: "string", nullable: false, hasConstant: true, constant: "engineering_regression_only"},
	}},
	"SnapshotV2HashProjection": {typ: reflect.TypeOf(SnapshotV2HashProjection{}), fields: []fieldRule{
		{name: "protocol", kind: "string", nullable: false, hasConstant: true, constant: "dynamics-snapshot-v2"},
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "cursor", kind: "ref", nullable: false, ref: "StreamCursorV2"},
		{name: "states", kind: "array", nullable: false, item: &fieldRule{name: "", kind: "ref", nullable: false, ref: "StateV2"}},
		{name: "receipts", kind: "ref", nullable: false, ref: "ReceiptIndexV2"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
	}},
	"StateV2HashProjection": {typ: reflect.TypeOf(StateV2HashProjection{}), fields: []fieldRule{
		{name: "key", kind: "ref", nullable: false, ref: "DynamicsKeyV2"},
		{name: "mode", kind: "string", nullable: false, hasConstant: true, constant: "scalar"},
		{name: "value", kind: "ref", nullable: false, ref: "UnitFloat"},
		{name: "state_revision", kind: "ref", nullable: false, ref: "Seq0"},
		{name: "activated_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "activation_accepted_seq", kind: "ref", nullable: true, ref: "Seq"},
		{name: "activation_event_id", kind: "ref", nullable: true, ref: "LegacyID"},
		{name: "activation_payload_sha256", kind: "ref", nullable: true, ref: "SHA"},
		{name: "last_applied_at_ns", kind: "ref", nullable: true, ref: "NS"},
		{name: "last_applied_seq", kind: "ref", nullable: true, ref: "Seq"},
		{name: "status", kind: "string", nullable: false, enums: []string{"pending_activation", "active", "invalidated"}},
		{name: "reason", kind: "ref", nullable: false, ref: "Reason"},
	}},
	"StreamAcceptedStepProjectionV2": {typ: reflect.TypeOf(StreamAcceptedStepProjectionV2{}), fields: []fieldRule{
		{name: "previous_prefix_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "accepted_seq", kind: "ref", nullable: false, ref: "Seq"},
		{name: "accepted_at_ns", kind: "ref", nullable: false, ref: "NS"},
		{name: "payload_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"StreamInitialPrefixProjectionV2": {typ: reflect.TypeOf(StreamInitialPrefixProjectionV2{}), fields: []fieldRule{
		{name: "stream_id", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "clock_domain", kind: "ref", nullable: false, ref: "LegacyID"},
		{name: "scope", kind: "ref", nullable: false, ref: "ScopeV2"},
		{name: "dimension", kind: "ref", nullable: false, ref: "Dimension"},
		{name: "config_sha256", kind: "ref", nullable: false, ref: "SHA"},
	}},
	"TransitionV2HashProjection": {typ: reflect.TypeOf(TransitionV2HashProjection{}), fields: []fieldRule{
		{name: "before_state_sha256", kind: "ref", nullable: false, ref: "SHA"},
		{name: "state", kind: "ref", nullable: false, ref: "StateV2"},
		{name: "receipts", kind: "ref", nullable: false, ref: "ReceiptIndexV2"},
		{name: "receipt", kind: "ref", nullable: false, ref: "ReceiptV2"},
		{name: "cursor", kind: "ref", nullable: false, ref: "StreamCursorV2"},
		{name: "attempt_disposition", kind: "ref", nullable: false, ref: "AttemptDisposition"},
	}},
	"LegacyContextBudget": {typ: reflect.TypeOf(LegacyContextBudget{}), fields: []fieldRule{
		{name: "unit", kind: "string", nullable: false, hasConstant: true, constant: "fixture_units_v0"},
		{name: "limit", kind: "ref", nullable: false, ref: "Count"},
	}},
}
var fixedEnums = map[string][]string{
	"AttemptDisposition": {"applied", "non_learning", "invalidated", "duplicate_event", "duplicate_evidence"},
	"Attribution":        {"memory", "tool", "retrieval", "evaluator", "unattributable", "not_applicable"},
	"Dimension":          {"personal_compliance", "work_task"},
	"ErrorCode":          {"invalid_json", "invalid_unicode", "duplicate_key", "unknown_field", "missing_field", "invalid_type", "invalid_value", "unsupported_protocol", "unsupported_mode", "unsupported_policy", "unsupported_restore", "scope_mismatch", "stream_mismatch", "clock_domain_mismatch", "revision_mismatch", "source_unavailable", "task_attribution_unknown", "evidence_unqualified", "event_conflict", "evidence_conflict", "time_regression", "sequence_regression", "future_event", "stale_target", "invalidated_target", "prefix_mismatch", "config_mismatch", "hash_mismatch", "missing_state", "missing_feature", "duplicate_feature", "input_role_forbidden", "resource_limit", "io_error", "pending_activation", "unsupported_fallback", "revision_regression", "target_mismatch", "incomplete_prefix", "unsupported_version", "unsupported_type", "snapshot_conflict", "snapshot_binding", "schedule_mismatch", "unsafe_path", "output_exists", "invalid_state", "invalid_time", "invalid_half_life"},
	"EventKind":          {"activate", "outcome", "exposure", "invalidate"},
	"FallbackMode":       {"error"},
	"InputRole":          {"events", "probes", "candidates", "snapshot", "registry", "features"},
	"Outcome":            {"passed", "failed", "unknown", "not_applicable"},
	"OutputRole":         {"prediction_run", "completion_manifest"},
	"OutputStatus":       {"completed", "completed_with_failures"},
	"PolicyID":           {"B0", "B1", "B2", "V3-P1-FD", "V3-P1-IR", "V3-P1-NOTIME", "V3-P1-NOOUT"},
	"Reason":             {"none", "activation", "passed_independent", "failed_attributable", "failed_unattributable", "unknown_outcome", "exposure_only", "invalidated", "duplicate_event", "duplicate_evidence", "cold_start_missing", "feature_missing", "stale_revision", "source_deleted", "source_revoked", "source_unknown", "task_unknown", "fallback_b0", "unsupported"},
	"ReceiptDisposition": {"applied", "non_learning", "invalidated"},
	"V2ErrorCode":        {"invalid_json", "invalid_unicode", "duplicate_key", "unknown_field", "missing_field", "invalid_type", "invalid_value", "unsupported_protocol", "unsupported_mode", "unsupported_policy", "unsupported_restore", "scope_mismatch", "stream_mismatch", "clock_domain_mismatch", "revision_mismatch", "source_unavailable", "task_attribution_unknown", "evidence_unqualified", "event_conflict", "evidence_conflict", "time_regression", "sequence_regression", "future_event", "stale_target", "invalidated_target", "prefix_mismatch", "config_mismatch", "hash_mismatch", "missing_state", "missing_feature", "duplicate_feature", "input_role_forbidden", "resource_limit", "io_error", "pending_activation", "unsupported_fallback", "revision_regression", "target_mismatch", "incomplete_prefix", "unsupported_version", "unsupported_type", "snapshot_conflict", "snapshot_binding", "schedule_mismatch", "unsafe_path", "output_exists", "invalid_state", "invalid_time", "invalid_half_life"},
}
var fixedNumbers = map[string]numberRule{
	"Count":            {kind: "int64", minimum: 0, maximum: 9223372036854775807},
	"FinalK":           {kind: "int64", minimum: 0, maximum: 64},
	"NS":               {kind: "int64", minimum: 0, maximum: 9223372036854775807},
	"NonnegativeFloat": {kind: "float64", minimum: 0, maximum: 1.7976931348623157e+308},
	"PositiveInt64":    {kind: "int64", minimum: 1, maximum: 9223372036854775807},
	"PositiveNS":       {kind: "int64", minimum: 1, maximum: 9223372036854775807},
	"Seq":              {kind: "int64", minimum: 1, maximum: 9223372036854775807},
	"Seq0":             {kind: "int64", minimum: 0, maximum: 9223372036854775807},
	"Tolerance":        {kind: "float64", minimum: 0, maximum: 1e-06},
	"UnitFloat":        {kind: "float64", minimum: 0, maximum: 1},
}
var fixedProjections = map[string]string{
	"DynamicInputManifestV2": "DynamicInputManifestV2HashProjection",
	"DynamicsConfigV2":       "DynamicsConfigV2HashProjection",
	"DynamicsRunConfigV2":    "DynamicsRunConfigV2HashProjection",
	"EvidenceBinding":        "EvidenceBindingHashProjection",
	"ImportanceFeatureV1":    "ImportanceFeatureV1HashProjection",
	"LifecycleFence":         "LifecycleFenceHashProjection",
	"P1InputManifestV1":      "P1InputManifestV1HashProjection",
	"PolicyDecisionV2":       "PolicyDecisionV2HashProjection",
	"PredictionRunV2":        "PredictionRunV2HashProjection",
	"ReceiptIndexV2":         "ReceiptIndexV2HashProjection",
	"ReceiptV2":              "ReceiptV2HashProjection",
	"SnapshotV2":             "SnapshotV2HashProjection",
	"StateV2":                "StateV2HashProjection",
	"TransitionV2":           "TransitionV2HashProjection",
	"EventV2":                "EventLogicalProjectionV2",
	"RankerConfigV2":         "RankerConfigParametersProjectionV2",
	"PolicyRegistryV2":       "PolicyRegistryGraphProjectionV2",
}
var fixedHashFields = map[string]string{
	"DynamicInputManifestV2": "ManifestSHA256",
	"DynamicsConfigV2":       "ConfigSHA256",
	"DynamicsRunConfigV2":    "ConfigSHA256",
	"EvidenceBinding":        "BindingSHA256",
	"ImportanceFeatureV1":    "FeatureSHA256",
	"LifecycleFence":         "FenceSHA256",
	"P1InputManifestV1":      "ManifestSHA256",
	"PolicyDecisionV2":       "DecisionSHA256",
	"PredictionRunV2":        "RunSHA256",
	"ReceiptIndexV2":         "IndexSHA256",
	"ReceiptV2":              "ReceiptSHA256",
	"SnapshotV2":             "SnapshotSHA256",
	"StateV2":                "StateSHA256",
	"TransitionV2":           "TransitionSHA256",
	"EventV2":                "PayloadSHA256",
	"RankerConfigV2":         "ConfigSHA256",
	"PolicyRegistryV2":       "RegistrySHA256",
}
