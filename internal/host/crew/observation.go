package crew

// Opt-in observation is independent from operational usage/reporting. It never
// changes dispatch/retry/approval decisions and never journals task/output text.
import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

type ObservationTransportWriter = crewrun.ObservationJournalWriter

func OpenObservationTransportWriter(req crewrun.ObservationTransportRequest) (*ObservationTransportWriter, error) {
	return crewrun.OpenObservationJournalWriter(req)
}

const hostObservationKey = "_observation_session"
const hostObservationActiveKey = "_observation_active"
const hostObservationRouteKey = "_observation_route"

type hostObservationSession struct {
	writer        *ObservationTransportWriter
	request       crewrun.ObservationTransportRequest
	mu            sync.Mutex
	sequence      int64
	failedCallKey string
}
type hostObservationCall struct {
	session        *hostObservationSession
	identity       crewrun.AttemptIdentity
	started        time.Time
	begun          bool
	finished       bool
	knownDispatch  bool
	notDispatched  bool
	reported       map[string]any
	estimated      map[string]any
	providerSchema *string
}

func observationSafeSpec(spec map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range spec {
		if key != "observation" && !strings.HasPrefix(key, "_observation_") {
			result[key] = value
		}
	}
	return result
}
func hostObservationSessionFrom(spec map[string]any) *hostObservationSession {
	session, _ := spec[hostObservationKey].(*hostObservationSession)
	return session
}
func openHostObservation(spec map[string]any, rawRequest []byte) (map[string]any, *hostObservationSession) {
	if len(rawRequest) == 0 {
		return spec, nil
	}
	req, err := crewrun.DecodeObservationTransportRequest(rawRequest)
	if err != nil {
		return observationSafeSpec(spec), nil
	}
	writer, err := OpenObservationTransportWriter(req)
	if err != nil {
		if writer != nil {
			writer.MarkIncomplete()
			_ = writer.CloseBoundary()
		}
		return observationSafeSpec(spec), nil
	}
	session := &hostObservationSession{writer: writer, request: req}
	out := observationSafeSpec(spec)
	out[hostObservationKey] = session
	return out, session
}
func closeHostObservation(session *hostObservationSession) {
	if session != nil && session.writer != nil {
		_ = session.writer.CloseBoundary()
	}
}
func startHostObservation(spec map[string]any) (*hostObservationCall, bool) {
	if existing, ok := spec[hostObservationActiveKey].(*hostObservationCall); ok {
		return existing, false
	}
	session := hostObservationSessionFrom(spec)
	if session == nil {
		return nil, false
	}
	session.mu.Lock()
	index := session.sequence
	session.sequence++
	failedCallKey := session.failedCallKey
	session.mu.Unlock()
	parent := session.request.Parent
	identity := parent.Identity
	identity.AttemptID = ""
	identity.AttemptRef = nil
	identity.CanonicalAttemptID = nil
	identity.ParentAttemptID = &parent.Identity.AttemptID
	identity.IdentityBasis = "observed_host_leaf"
	identity.RetryIndex = 0
	identity.CallKey = fmt.Sprintf("host/%s/%d", parent.Identity.CallKey, index)
	identity.FallbackParentCallKey = nil
	if route, ok := spec[hostObservationRouteKey].(int); ok && route > 0 && failedCallKey != "" {
		identity.FallbackParentCallKey = &failedCallKey
	}
	id, err := crewrun.StableAttemptID(identity)
	if err != nil {
		session.writer.MarkIncomplete()
		return nil, false
	}
	identity.AttemptID = id
	return &hostObservationCall{session: session, identity: identity}, true
}
func (call *hostObservationCall) begin() {
	if call == nil || call.begun {
		return
	}
	call.started = time.Now()
	call.begun = true
	if err := call.session.writer.Begin(call.identity.AttemptID); err != nil {
		call.session.writer.MarkIncomplete()
	}
}
func (call *hostObservationCall) dispatched() {
	if call != nil {
		call.begin()
		call.knownDispatch = true
	}
}
func (call *hostObservationCall) incomplete() {
	if call != nil {
		call.session.writer.MarkIncomplete()
	}
}
func (call *hostObservationCall) offerReported(reported map[string]any, schema string) {
	if call != nil {
		call.reported = reported
		if schema != "" {
			value := schema
			call.providerSchema = &value
		}
	}
}
func (call *hostObservationCall) offerEstimate(estimated map[string]any) {
	if call != nil {
		call.estimated = estimated
	}
}
func observationUsageMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}
func hostObservationUsage(call *hostObservationCall, spec, res map[string]any) crewrun.UsageObservation {
	reported, estimated := call.reported, call.estimated
	if reported == nil && res != nil {
		if usage := observationUsageMap(res["usage"]); usage != nil {
			if usage["estimated"] == true {
				estimated = usage
			} else {
				reported = usage
			}
		}
	}
	if estimated == nil && res != nil {
		estimated = usageOf(observationSafeSpec(spec), res)
	}
	reported = cleanObservationUsage(call, reported, false)
	estimated = cleanObservationUsage(call, estimated, true)
	usage, err := NormalizeUsageObservation(reported, estimated, call.providerSchema, nil, nil)
	if err != nil {
		call.incomplete()
		usage = crewrun.UnknownUsageObservation()
	}
	if !call.started.IsZero() {
		finish := time.Now()
		started, finished, duration := call.started.UnixNano(), finish.UnixNano(), finish.Sub(call.started).Nanoseconds()
		if finished < started {
			finished = started
		}
		usage.StartedAtNS = &started
		usage.FinishedAtNS = &finished
		usage.DurationNS = &duration
		usage.DurationBasis = "observed_monotonic"
	}
	return usage
}
func cleanObservationUsage(call *hostObservationCall, source map[string]any, estimate bool) map[string]any {
	if source == nil {
		return nil
	}
	clean := map[string]any{}
	if !estimate && source["estimated"] == true {
		call.incomplete()
		return nil
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens", "cost_usd"} {
		value, ok := source[key]
		if !ok || value == nil {
			continue
		}
		var err error
		if key == "cost_usd" {
			_, err = usageObservationCost(value, "$.cost_usd")
		} else {
			_, err = usageObservationInteger(value, "$."+key)
		}
		if err != nil {
			call.incomplete()
			continue
		}
		clean[key] = value
	}
	return clean
}
func finishHostObservation(call *hostObservationCall, owned bool, spec, res map[string]any, callErr error) {
	if call == nil || !owned || call.finished || !call.begun {
		return
	}
	call.finished = true
	usage := hostObservationUsage(call, spec, res)
	status, dispatch, code := "failed", "unknown", "execution_error"
	if call.knownDispatch {
		dispatch = "dispatched"
	}
	if call.notDispatched {
		dispatch = "not_dispatched"
	}
	if res != nil && res["ok"] == true {
		status = "succeeded"
		dispatch = "dispatched"
		code = ""
	} else if res != nil && strings.HasPrefix(sOr(res["error"]), "timeout") {
		status = "timeout"
		code = "timeout"
	} else if callErr != nil {
		code = "transport_error"
	}
	parent := call.session.request.Parent
	record, err := crewrun.SealAttemptObservation(crewrun.AttemptObservation{ContractID: crewrun.CrewObservationContractID, Identity: call.identity, Task: parent.Task, Status: status, DispatchState: dispatch, Usage: usage, ErrorCode: code, ObservationRevision: 1, UsageRole: "provider_leaf", LeafCoverage: "not_applicable"})
	if err != nil {
		call.incomplete()
		return
	}
	run := parent.Identity.Run
	proof, err := crewrun.SealObservationParentLink(crewrun.ObservationParentLinkEvidence{ContractID: crewrun.CrewObservationContractID, Scope: run.Scope, Task: parent.Task, RootRunCollection: *run.RootRunCollection, RootRunID: *run.RootRunID, ParentAttemptID: parent.Identity.AttemptID, ChildAttemptID: record.Identity.AttemptID, ParentRunRef: run.RunRef, ChildRunRef: record.Identity.Run.RunRef, Relationship: "host_leaf", SourceSHA256: parent.RecordSHA256})
	if err != nil {
		call.incomplete()
		return
	}
	if err = call.session.writer.Record(record, proof); err != nil {
		call.incomplete()
	} else {
		call.recorded(record)
	}
	if res != nil {
		res["usage_observation"] = usage
	}
}
func observedInjectedRun(spec map[string]any, run runFn) (map[string]any, error) {
	call, owned := startHostObservation(spec)
	if call == nil {
		return run(spec)
	}
	call.begin()
	active := withKey(spec, hostObservationActiveKey, call)
	res, err := run(active)
	finishHostObservation(call, owned, spec, res, err)
	return res, err
}
func observedBackendRun(spec map[string]any) (map[string]any, error) {
	call, owned := startHostObservation(spec)
	if call != nil {
		call.begin()
		call.incomplete()
	}
	res, err := runBackend(observationSafeSpec(spec))
	finishHostObservation(call, owned, spec, res, err)
	return res, err
}
func rawHostObservationRequest(raw string) []byte {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return nil
	}
	request := fields["observation"]
	if bytesAreNull(request) {
		return nil
	}
	return request
}
func bytesAreNull(raw []byte) bool { return len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" }

func customObservationUsage(value any) map[string]any {
	source := obj(value)
	if source == nil {
		return nil
	}
	result := map[string]any{}
	for _, pair := range [][2]string{{"prompt_tokens", "input_tokens"}, {"completion_tokens", "output_tokens"}, {"input_tokens", "input_tokens"}, {"output_tokens", "output_tokens"}, {"cost_usd", "cost_usd"}} {
		if value, ok := source[pair[0]]; ok {
			result[pair[1]] = value
		}
	}
	for from, to := range map[string]string{"cached_tokens": "cache_read_tokens"} {
		if value, ok := obj(source["prompt_tokens_details"])[from]; ok {
			result[to] = value
		}
	}
	if value, ok := obj(source["completion_tokens_details"])["reasoning_tokens"]; ok {
		result["reasoning_tokens"] = value
	}
	return result
}

func assistObservationSession(rec, item map[string]any, request *crewrun.ObservationTransportRequest) *hostObservationSession {
	if request == nil {
		return nil
	}
	run := request.Parent.Identity.Run
	if run.RunRef.Collection != "crew_assists" || run.Definition.Ref.Collection != "crew" || run.RunRef.ID != sOr(rec["id"]) || run.Definition.Ref.ID != sOr(item["id"]) {
		return nil
	}
	actualRun, runErr := usageObservationInteger(rec["revision"], "$.revision")
	actualDefinition, definitionErr := usageObservationInteger(item["revision"], "$.revision")
	if runErr != nil || definitionErr != nil || actualRun < 1 || actualDefinition < 1 || run.RunRef.Revision != actualRun || run.Definition.Ref.Revision != actualDefinition {
		return nil
	}
	for _, source := range []map[string]any{rec, item} {
		scope := obj(source["scope"])
		actual := crewrun.ObservationScope{WorkspaceID: sOr(scope["workspace_id"]), ProjectID: sOr(scope["project_id"]), UserID: sOr(scope["user_id"]), AgentID: sOr(scope["agent_id"])}
		if actual.WorkspaceID == "" || actual.ProjectID == "" || actual != run.Scope {
			return nil
		}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil
	}
	_, session := openHostObservation(map[string]any{}, raw)
	return session
}

func (call *hostObservationCall) recorded(record crewrun.AttemptObservation) {
	call.session.mu.Lock()
	defer call.session.mu.Unlock()
	call.session.failedCallKey = ""
	if record.Status == "failed" || record.Status == "timeout" {
		call.session.failedCallKey = record.Identity.CallKey
	}
}
func observationFallbackSpec(spec map[string]any) map[string]any {
	if hostObservationSessionFrom(spec) == nil {
		return spec
	}
	return withKey(spec, hostObservationRouteKey, 1)
}
