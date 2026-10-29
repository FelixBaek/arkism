package memory

// Task: SYS-02/SYS-03. Native derived-state metadata has its own namespace.
import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"
)

const nativeDynamicsProtocol = "arkism-native-dynamics-v1"
const nativeDynamicsPrefix = "native_dynamics_"

type NativeDynamicsRuntime struct{ Enabled bool }

type NativeDynamicsConfig struct {
	Version             string   `json:"version" bson:"version"`
	InitialValue        *float64 `json:"initial_value" bson:"initial_value"`
	TauNS               *int64   `json:"tau_ns" bson:"tau_ns"`
	EtaPositive         *float64 `json:"eta_positive" bson:"eta_positive"`
	UnitStrength        *float64 `json:"unit_strength" bson:"unit_strength"`
	ObservationStrength *float64 `json:"observation_strength" bson:"observation_strength"`
	DecayEnabled        *bool    `json:"decay_enabled" bson:"decay_enabled"`
	PositiveLearning    *bool    `json:"positive_learning" bson:"positive_learning"`
	MaxHistory          *int64   `json:"max_history" bson:"max_history"`
	MaxDependencies     *int64   `json:"max_dependencies" bson:"max_dependencies"`
}

type NativeDynamicsKey struct {
	Scope     Scope  `json:"scope" bson:"scope"`
	Target    Ref    `json:"target" bson:"target"`
	Dimension string `json:"dimension" bson:"dimension"`
	Version   string `json:"version" bson:"version"`
}

type NativeOutcomeCaptureStamp struct {
	Protocol   string `json:"protocol" bson:"protocol"`
	Mode       string `json:"mode" bson:"mode"`
	Basis      string `json:"basis" bson:"basis"`
	Enrollment *Ref   `json:"enrollment" bson:"enrollment"`
	ConfigHash string `json:"config_hash" bson:"config_hash"`
	Version    string `json:"version" bson:"version"`
}

type NativeDynamicsDependency struct {
	Ref  Ref    `json:"ref" bson:"ref"`
	Hash string `json:"hash" bson:"hash"`
}

type NativeDynamicsState struct {
	Value           *float64  `json:"value" bson:"value"`
	LastModelNS     int64     `json:"last_model_ns" bson:"last_model_ns"`
	LastSeq         int64     `json:"last_seq" bson:"last_seq"`
	Epoch           int64     `json:"epoch" bson:"epoch"`
	Status          string    `json:"status" bson:"status"`
	ActivationAt    time.Time `json:"activation_at" bson:"activation_at"`
	MemoryCreatedAt time.Time `json:"memory_created_at" bson:"memory_created_at"`
	MemoryUpdatedAt time.Time `json:"memory_updated_at" bson:"memory_updated_at"`
	Anchor          string    `json:"anchor" bson:"anchor"`
	History         []Ref     `json:"history" bson:"history"`
}

type NativeDynamicsReceipt struct {
	Protocol         string    `json:"protocol" bson:"protocol"`
	Disposition      string    `json:"disposition" bson:"disposition"`
	Capture          Ref       `json:"capture" bson:"capture"`
	Event            Ref       `json:"event" bson:"event"`
	State            Ref       `json:"state" bson:"state"`
	ProcessingSeq    *int64    `json:"processing_seq" bson:"processing_seq"`
	SourceAcceptedAt time.Time `json:"source_accepted_at" bson:"source_accepted_at"`
	AppliedNow       bool      `json:"applied_now" bson:"applied_now"`
	LearningConsumed bool      `json:"learning_consumed" bson:"learning_consumed"`
	Reason           string    `json:"reason" bson:"reason"`
}

type NativeDynamicsPurge struct {
	DependencyKey   string `json:"dependency_key" bson:"dependency_key"`
	ThroughRevision int64  `json:"through_revision" bson:"through_revision"`
	RoleIndex       int    `json:"role_index" bson:"role_index"`
	After           string `json:"after" bson:"after"`
	Done            bool   `json:"done" bson:"done"`
}

type NativeDynamicsPayload struct {
	Purge            *NativeDynamicsPurge       `json:"purge" bson:"purge"`
	Protocol         string                     `json:"protocol" bson:"protocol"`
	Key              NativeDynamicsKey          `json:"key" bson:"key"`
	Config           *NativeDynamicsConfig      `json:"config" bson:"config"`
	ConfigHash       string                     `json:"config_hash" bson:"config_hash"`
	Input            *MemoryOutcomeInput        `json:"input" bson:"input"`
	Evaluation       Ref                        `json:"evaluation" bson:"evaluation"`
	Stamp            *NativeOutcomeCaptureStamp `json:"stamp" bson:"stamp"`
	State            *NativeDynamicsState       `json:"state" bson:"state"`
	Receipt          *NativeDynamicsReceipt     `json:"receipt" bson:"receipt"`
	IdentitySHA      string                     `json:"identity_sha" bson:"identity_sha"`
	ClaimSHA         string                     `json:"claim_sha" bson:"claim_sha"`
	ObservedPassed   *bool                      `json:"observed_passed" bson:"observed_passed"`
	Dependencies     []NativeDynamicsDependency `json:"dependencies" bson:"dependencies"`
	DependencyKeys   []string                   `json:"dependency_keys" bson:"dependency_keys"`
	SourceAcceptedAt time.Time                  `json:"source_accepted_at" bson:"source_accepted_at"`
	Reason           string                     `json:"reason" bson:"reason"`
}

type NativeDynamicsActivationInput struct {
	Scope     Scope
	Target    Ref
	Dimension string
	Config    NativeDynamicsConfig
	RequestID string
}

type NativeDynamicsQuery struct {
	Scope     Scope
	Target    Ref
	Dimension string
	Version   string
	CutoffNS  int64
}

type NativeDynamicsView struct {
	Status     string   `json:"status"`
	Value      *float64 `json:"value"`
	State      Ref      `json:"state"`
	Epoch      int64    `json:"epoch"`
	LastSeq    int64    `json:"last_seq"`
	ConfigHash string   `json:"config_hash"`
	Authority  string   `json:"authority"`
}

func nativeHash(kind string, v any) string {
	return ContentHash(nativeDynamicsProtocol + "\x00" + kind + "\x00" + structuredText(v))
}
func nativeID(role string, v any) string         { return "nd_" + role + "_" + nativeHash(role, v) }
func nativeStateID(key NativeDynamicsKey) string { return nativeID("state", key) }
func nativeEnrollmentID(scope Scope, target Ref, dimension string) string {
	return nativeID("enrollment", struct {
		Scope     Scope
		Target    Ref
		Dimension string
	}{scope, target, dimension})
}
func nativeCaptureID(scope Scope, evaluation Ref) string {
	return nativeID("capture", struct {
		Scope      Scope
		Evaluation Ref
	}{scope, evaluation})
}
func nativeDependencyKey(scope Scope, ref Ref) string {
	return nativeHash("dependency", struct {
		Scope          Scope
		Collection, ID string
	}{scope, ref.Collection, ref.ID})
}
func nativeConfigID(scope Scope, version string) string {
	return nativeID("config", struct {
		Scope   Scope
		Version string
	}{scope, version})
}
func nativeFloatBits(v float64) string {
	if v == 0 {
		v = 0
	}
	return fmt.Sprintf("%016x", math.Float64bits(v))
}
func nativeConfigHash(c NativeDynamicsConfig) string {
	return nativeHash("config", struct {
		Version, Initial, Eta, Unit, Strength string
		Tau, History, Dependencies            int64
		Decay, Positive                       bool
	}{c.Version, nativeFloatBits(*c.InitialValue), nativeFloatBits(*c.EtaPositive), nativeFloatBits(*c.UnitStrength), nativeFloatBits(*c.ObservationStrength), *c.TauNS, *c.MaxHistory, *c.MaxDependencies, *c.DecayEnabled, *c.PositiveLearning})
}
func nativeUnit(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 1
}
func nativeConfigValid(c NativeDynamicsConfig) bool {
	return validRunLabel(c.Version, 128) && nativeUnit(c.InitialValue) && nativeUnit(c.EtaPositive) && nativeUnit(c.UnitStrength) && nativeUnit(c.ObservationStrength) && c.TauNS != nil && *c.TauNS > 0 && c.DecayEnabled != nil && c.PositiveLearning != nil && c.MaxHistory != nil && *c.MaxHistory > 0 && *c.MaxHistory <= 256 && c.MaxDependencies != nil && *c.MaxDependencies > 0 && *c.MaxDependencies <= 500
}
func nativeDimension(d string) bool { return d == "personal_compliance" || d == "work_task" }
func nativeClone[T any](in T) (T, error) {
	var out T
	b, e := json.Marshal(in)
	if e != nil {
		return out, ErrInvalid
	}
	if json.Unmarshal(b, &out) != nil {
		return out, ErrInvalid
	}
	return out, nil
}
func nativeRef(r Record) Ref { return Ref{Collection: colJobs, ID: r.ID, Revision: r.Revision} }
func nativeJob(scope Scope, role, id string, p NativeDynamicsPayload) Record {
	return Record{ID: id, Scope: scope, Kind: nativeDynamicsPrefix + role, Status: JobStatusDone, Revision: 1, NativeDynamics: &p}
}
func nativePayload(r Record) (*NativeDynamicsPayload, error) {
	if r.NativeDynamics == nil || r.NativeDynamics.Protocol != nativeDynamicsProtocol {
		return nil, ErrConflict
	}
	return nativeClone(r.NativeDynamics)
}
func nativeDependencyKeys(scope Scope, deps []NativeDynamicsDependency) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, nativeDependencyKey(scope, d.Ref))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
