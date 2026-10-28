// Package memoryharness defines the isolated, fixed-schema memory harness contract.
package memoryharness

import "reflect"

// ContractError identifies a contract violation without exposing input contents.
// Path is a schema path; Detail is reserved for fixed explanatory text.
type ContractError struct {
	Code   string
	Path   string
	Detail string
}

// Error returns only the error code and schema path.
func (e *ContractError) Error() string {
	if e == nil {
		return ""
	}
	if e.Path == "" {
		return e.Code
	}
	return e.Code + ": " + e.Path
}

func contractError(code, path string) error {
	return &ContractError{Code: code, Path: path}
}

// Scope is the fixed contract representation of Scope.
// All JSON fields are required; nullable values retain explicit nulls.
type Scope struct {
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	User      string `json:"user"`
	Agent     string `json:"agent"`
}

// Ref is the fixed contract representation of Ref.
// All JSON fields are required; nullable values retain explicit nulls.
type Ref struct {
	Origin        string `json:"origin"`
	Collection    string `json:"collection"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	ContentSHA256 string `json:"content_sha256"`
}

// Candidate is the fixed contract representation of Candidate.
// All JSON fields are required; nullable values retain explicit nulls.
type Candidate struct {
	Scope            Scope    `json:"scope"`
	Ref              Ref      `json:"ref"`
	Reasons          []string `json:"reasons"`
	Trust            float64  `json:"trust"`
	UpdatedAtNS      int64    `json:"updated_at_ns"`
	ObservedSeq      int64    `json:"observed_seq"`
	EffectiveAtNS    int64    `json:"effective_at_ns"`
	ContextCostUnits int64    `json:"context_cost_units"`
	Approved         bool     `json:"approved"`
	Active           bool     `json:"active"`
	Deleted          bool     `json:"deleted"`
	SourceVerified   bool     `json:"source_verified"`
	Authority        string   `json:"authority"`
}

// CandidateSnapshot is the fixed contract representation of CandidateSnapshot.
// All JSON fields are required; nullable values retain explicit nulls.
type CandidateSnapshot struct {
	SnapshotID string        `json:"snapshot_id"`
	StreamID   string        `json:"stream_id"`
	Scope      Scope         `json:"scope"`
	AsOfNS     int64         `json:"as_of_ns"`
	AsOfSeq    int64         `json:"as_of_seq"`
	Lanes      [][]Candidate `json:"lanes"`
}

// ContextBudget is the fixed contract representation of ContextBudget.
// All JSON fields are required; nullable values retain explicit nulls.
type ContextBudget struct {
	Unit  string `json:"unit"`
	Limit int64  `json:"limit"`
}

// Probe is the fixed contract representation of Probe.
// All JSON fields are required; nullable values retain explicit nulls.
type Probe struct {
	QueryID             string        `json:"query_id"`
	StreamID            string        `json:"stream_id"`
	Scope               Scope         `json:"scope"`
	AtNS                int64         `json:"at_ns"`
	CutoffAcceptedSeq   int64         `json:"cutoff_accepted_seq"`
	CandidateSnapshotID string        `json:"candidate_snapshot_id"`
	FinalK              int64         `json:"final_k"`
	ContextBudget       ContextBudget `json:"context_budget"`
	PolicyID            string        `json:"policy_id"`
}

// Gold is the fixed contract representation of Gold.
// All JSON fields are required; nullable values retain explicit nulls.
type Gold struct {
	QueryID           string `json:"query_id"`
	StreamID          string `json:"stream_id"`
	Scope             Scope  `json:"scope"`
	AtNS              int64  `json:"at_ns"`
	CutoffAcceptedSeq int64  `json:"cutoff_accepted_seq"`
	TemporalKind      string `json:"temporal_kind"`
	LabelStatus       string `json:"label_status"`
	RelevantRefs      []Ref  `json:"relevant_refs"`
}

// Prediction is the fixed contract representation of Prediction.
// All JSON fields are required; nullable values retain explicit nulls.
type Prediction struct {
	QueryID      string `json:"query_id"`
	StreamID     string `json:"stream_id"`
	Scope        Scope  `json:"scope"`
	Status       string `json:"status"`
	SelectedRefs []Ref  `json:"selected_refs"`
	ErrorCode    string `json:"error_code"`
}

// Policy is the fixed contract representation of Policy.
// All JSON fields are required; nullable values retain explicit nulls.
type Policy struct {
	ContractID        string  `json:"contract_id"`
	PolicyID          string  `json:"policy_id"`
	RankOffset        int64   `json:"rank_offset"`
	ExactReasonWeight float64 `json:"exact_reason_weight"`
	OtherWeight       float64 `json:"other_weight"`
	ContextUnit       string  `json:"context_unit"`
	EligibilityOrder  string  `json:"eligibility_order"`
}

// ProbeFile is the fixed contract representation of ProbeFile.
// All JSON fields are required; nullable values retain explicit nulls.
type ProbeFile struct {
	ContractID string  `json:"contract_id"`
	Probes     []Probe `json:"probes"`
}

// CandidateFile is the fixed contract representation of CandidateFile.
// All JSON fields are required; nullable values retain explicit nulls.
type CandidateFile struct {
	ContractID string              `json:"contract_id"`
	Snapshots  []CandidateSnapshot `json:"snapshots"`
}

// GoldFile is the fixed contract representation of GoldFile.
// All JSON fields are required; nullable values retain explicit nulls.
type GoldFile struct {
	ContractID string `json:"contract_id"`
	Labels     []Gold `json:"labels"`
}

// ArtifactEntry is the fixed contract representation of ArtifactEntry.
// All JSON fields are required; nullable values retain explicit nulls.
type ArtifactEntry struct {
	LogicalPath string `json:"logical_path"`
	SHA256      string `json:"sha256"`
	ByteLength  int64  `json:"byte_length"`
}

// InputManifest is the fixed contract representation of InputManifest.
// All JSON fields are required; nullable values retain explicit nulls.
type InputManifest struct {
	ContractID   string          `json:"contract_id"`
	ManifestKind string          `json:"manifest_kind"`
	DatasetRole  string          `json:"dataset_role"`
	Seed         uint64          `json:"seed"`
	SourceCommit string          `json:"source_commit"`
	Files        []ArtifactEntry `json:"files"`
}

// EvaluationManifest is the fixed contract representation of EvaluationManifest.
// All JSON fields are required; nullable values retain explicit nulls.
type EvaluationManifest struct {
	ContractID   string          `json:"contract_id"`
	ManifestKind string          `json:"manifest_kind"`
	DatasetRole  string          `json:"dataset_role"`
	Files        []ArtifactEntry `json:"files"`
}

// SplitEntry is the fixed contract representation of SplitEntry.
// All JSON fields are required; nullable values retain explicit nulls.
type SplitEntry struct {
	StreamID     string `json:"stream_id"`
	RootStreamID string `json:"root_stream_id"`
	SplitRole    string `json:"split_role"`
}

// SplitMap is the fixed contract representation of SplitMap.
// All JSON fields are required; nullable values retain explicit nulls.
type SplitMap struct {
	ContractID string       `json:"contract_id"`
	Streams    []SplitEntry `json:"streams"`
}

// RankedCandidate is the fixed contract representation of RankedCandidate.
// All JSON fields are required; nullable values retain explicit nulls.
type RankedCandidate struct {
	Candidate Candidate `json:"candidate"`
	Score     float64   `json:"score"`
	Lanes     []int64   `json:"lanes"`
}

// BaselineSelection is the fixed contract representation of BaselineSelection.
// All JSON fields are required; nullable values retain explicit nulls.
type BaselineSelection struct {
	Selected            []RankedCandidate `json:"selected"`
	UsedContextUnits    int64             `json:"used_context_units"`
	EligibleUniqueCount int64             `json:"eligible_unique_count"`
}

// PredictionRun is the fixed contract representation of PredictionRun.
// All JSON fields are required; nullable values retain explicit nulls.
type PredictionRun struct {
	ContractID          string       `json:"contract_id"`
	RunID               string       `json:"run_id"`
	PolicyID            string       `json:"policy_id"`
	InputManifestSHA256 string       `json:"input_manifest_sha256"`
	PolicySHA256        string       `json:"policy_sha256"`
	Predictions         []Prediction `json:"predictions"`
}

// Statistic is the fixed contract representation of Statistic.
// All JSON fields are required; nullable values retain explicit nulls.
type Statistic struct {
	Numerator   float64  `json:"numerator"`
	Denominator int64    `json:"denominator"`
	Value       *float64 `json:"value"`
	NullReason  string   `json:"null_reason"`
}

// QueryMetric is the fixed contract representation of QueryMetric.
// All JSON fields are required; nullable values retain explicit nulls.
type QueryMetric struct {
	QueryID             string   `json:"query_id"`
	StreamID            string   `json:"stream_id"`
	LabelStatus         string   `json:"label_status"`
	Status              string   `json:"status"`
	RawSelectedCount    int64    `json:"raw_selected_count"`
	UniqueSelectedCount int64    `json:"unique_selected_count"`
	RelevantCount       int64    `json:"relevant_count"`
	MatchedCount        int64    `json:"matched_count"`
	RecallAtK           *float64 `json:"recall_at_k"`
	HitAny              *int64   `json:"hit_any"`
}

// StreamMetric is the fixed contract representation of StreamMetric.
// All JSON fields are required; nullable values retain explicit nulls.
type StreamMetric struct {
	StreamID         string    `json:"stream_id"`
	ScheduledQueries int64     `json:"scheduled_queries"`
	RelevantQueries  int64     `json:"relevant_queries"`
	RecallAtK        Statistic `json:"recall_at_k"`
}

// MetricCounts is the fixed contract representation of MetricCounts.
// All JSON fields are required; nullable values retain explicit nulls.
type MetricCounts struct {
	ScheduledQueries       int64 `json:"scheduled_queries"`
	Streams                int64 `json:"streams"`
	RelevantQueries        int64 `json:"relevant_queries"`
	ClearNegativeQueries   int64 `json:"clear_negative_queries"`
	AmbiguousQueries       int64 `json:"ambiguous_queries"`
	SuccessfulQueries      int64 `json:"successful_queries"`
	FailedQueries          int64 `json:"failed_queries"`
	StreamsWithoutRelevant int64 `json:"streams_without_relevant"`
}

// MetricSummary is the fixed contract representation of MetricSummary.
// All JSON fields are required; nullable values retain explicit nulls.
type MetricSummary struct {
	StreamMacroRecallAtK              Statistic `json:"stream_macro_recall_at_k"`
	QueryWeightedRecallAtK            Statistic `json:"query_weighted_recall_at_k"`
	QueryHitAnyRate                   Statistic `json:"query_hit_any_rate"`
	AllQueryFailureRate               Statistic `json:"all_query_failure_rate"`
	ObservedNegativeFalsePositiveRate Statistic `json:"observed_negative_false_positive_rate"`
	NegativeServiceFailureRate        Statistic `json:"negative_service_failure_rate"`
}

// MetricRun is the fixed contract representation of MetricRun.
// All JSON fields are required; nullable values retain explicit nulls.
type MetricRun struct {
	ContractID               string         `json:"contract_id"`
	RunID                    string         `json:"run_id"`
	PolicyID                 string         `json:"policy_id"`
	InputManifestSHA256      string         `json:"input_manifest_sha256"`
	PolicySHA256             string         `json:"policy_sha256"`
	PredictionsSHA256        string         `json:"predictions_sha256"`
	EvaluationManifestSHA256 string         `json:"evaluation_manifest_sha256"`
	Counts                   MetricCounts   `json:"counts"`
	Queries                  []QueryMetric  `json:"queries"`
	Streams                  []StreamMetric `json:"streams"`
	Summary                  MetricSummary  `json:"summary"`
}

// ReportSummary is the fixed contract representation of ReportSummary.
// All JSON fields are required; nullable values retain explicit nulls.
type ReportSummary struct {
	ContractID               string        `json:"contract_id"`
	RunID                    string        `json:"run_id"`
	PolicyID                 string        `json:"policy_id"`
	InputManifestSHA256      string        `json:"input_manifest_sha256"`
	PredictionsSHA256        string        `json:"predictions_sha256"`
	EvaluationManifestSHA256 string        `json:"evaluation_manifest_sha256"`
	MetricsSHA256            string        `json:"metrics_sha256"`
	DatasetRole              string        `json:"dataset_role"`
	Status                   string        `json:"status"`
	Counts                   MetricCounts  `json:"counts"`
	Summary                  MetricSummary `json:"summary"`
}

// contractTypes is a closed registry of concrete struct values.
var contractTypes = map[string]reflect.Type{
	"Scope":              reflect.TypeOf(Scope{}),
	"Ref":                reflect.TypeOf(Ref{}),
	"Candidate":          reflect.TypeOf(Candidate{}),
	"CandidateSnapshot":  reflect.TypeOf(CandidateSnapshot{}),
	"ContextBudget":      reflect.TypeOf(ContextBudget{}),
	"Probe":              reflect.TypeOf(Probe{}),
	"Gold":               reflect.TypeOf(Gold{}),
	"Prediction":         reflect.TypeOf(Prediction{}),
	"Policy":             reflect.TypeOf(Policy{}),
	"ProbeFile":          reflect.TypeOf(ProbeFile{}),
	"CandidateFile":      reflect.TypeOf(CandidateFile{}),
	"GoldFile":           reflect.TypeOf(GoldFile{}),
	"ArtifactEntry":      reflect.TypeOf(ArtifactEntry{}),
	"InputManifest":      reflect.TypeOf(InputManifest{}),
	"EvaluationManifest": reflect.TypeOf(EvaluationManifest{}),
	"SplitEntry":         reflect.TypeOf(SplitEntry{}),
	"SplitMap":           reflect.TypeOf(SplitMap{}),
	"RankedCandidate":    reflect.TypeOf(RankedCandidate{}),
	"BaselineSelection":  reflect.TypeOf(BaselineSelection{}),
	"PredictionRun":      reflect.TypeOf(PredictionRun{}),
	"Statistic":          reflect.TypeOf(Statistic{}),
	"QueryMetric":        reflect.TypeOf(QueryMetric{}),
	"StreamMetric":       reflect.TypeOf(StreamMetric{}),
	"MetricCounts":       reflect.TypeOf(MetricCounts{}),
	"MetricSummary":      reflect.TypeOf(MetricSummary{}),
	"MetricRun":          reflect.TypeOf(MetricRun{}),
	"ReportSummary":      reflect.TypeOf(ReportSummary{}),
}
