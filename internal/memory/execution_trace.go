package memory

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxTraceEvents     = 64
	maxTraceEventBytes = 64 << 10
)

type TraceEventInput struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Payload     json.RawMessage `json:"payload"`
	Sensitivity string          `json:"sensitivity,omitempty"`
}

type CompletedAgentRunInput struct {
	// AgentID names the executing agent when the trace lives in the shared
	// (agent-less) user scope; with a scope agent_id it must match or be omitted.
	AgentID       string    `json:"agent_id,omitempty"`
	OriginAgentID string    `json:"origin_agent_id,omitempty"`
	ID            string    `json:"id,omitempty"`
	TaskType      string    `json:"task_type"`
	Outcome       string    `json:"outcome"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Text          string    `json:"text"`
}

type TraceIngestInput struct {
	Scope          Scope                  `json:"scope"`
	TraceID        string                 `json:"trace_id"`
	ThreadID       string                 `json:"thread_id"`
	LangSmithRunID string                 `json:"langsmith_run_id,omitempty"`
	AgentRun       CompletedAgentRunInput `json:"agent_run"`
	Events         []TraceEventInput      `json:"events"`
}

type TraceIngestResult struct {
	Trace Ref `json:"trace"`
	Run   Ref `json:"run"`
	// SourceEvents point at the mirrored sources records so learning can use
	// the existing immutable SourceRef checks. Native source_events retain the
	// execution-only payload detail; ordinary documents remain sources only.
	SourceEvents []Ref `json:"source_events"`
	// Learning is set when a failed skill.usage became evaluation + lesson;
	// LearningError is a code when that derivation failed (the trace stays stored).
	Learning      *UsageLearning `json:"learning,omitempty"`
	LearningError string         `json:"learning_error,omitempty"`
}

type inspectedTraceEvent struct {
	TraceEventInput
	Inspection Inspection
}

type preparedTrace struct {
	input            TraceIngestInput
	events           []inspectedTraceEvent
	lineageByEvent   map[string][]string
	lineageSourceIDs []string
	requestHash      string
	sensitivity      string
	actor            Decision
}

type completedTraceStore interface {
	storeCompletedTrace(context.Context, preparedTrace) (TraceIngestResult, error)
}

func decodeCompletedTrace(w http.ResponseWriter, r *http.Request, in *TraceIngestInput) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxTraceEvents*maxTraceEventBytes+(1<<20))
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(in); err != nil {
		respond(w, nil, ErrInvalid)
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		respond(w, nil, ErrInvalid)
		return false
	}
	return true
}

func (s *Service) IngestCompletedTrace(ctx context.Context, token string, in TraceIngestInput) (TraceIngestResult, error) {
	prepared, raw, err := prepareCompletedTrace(in)
	if err != nil {
		if errors.Is(err, ErrSecretDetected) && s.Repo != nil {
			actor, authErr := s.authorize(ctx, token, "write:execution_traces", in.Scope, in.TraceID, "")
			if authErr != nil {
				return TraceIngestResult{}, authErr
			}
			if auditErr := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
				return audit(ctx, tx, in.Scope, actor.PrincipalID, "inspection:blocked:"+inspectionVersion, "")
			}); auditErr != nil {
				return TraceIngestResult{}, auditErr
			}
		}
		return TraceIngestResult{}, err
	}
	in = prepared.input
	actor, err := s.authorize(ctx, token, "write:execution_traces", in.Scope, in.TraceID, string(raw))
	if err != nil {
		return TraceIngestResult{}, err
	}
	if !actor.ContentSafe || !traceWriterRole(actor.Role) {
		return TraceIngestResult{}, ErrDenied
	}
	requestCtx := ctx // the hub's own reflection must not inherit the writer's binding
	ctx = bindRequestActor(ctx, actor)
	for _, action := range []string{"write:agent_runs", "write:sources"} {
		decision, authErr := s.authorize(ctx, token, action, in.Scope, in.AgentRun.ID, string(raw))
		if authErr != nil {
			return TraceIngestResult{}, authErr
		}
		if !decision.ContentSafe {
			return TraceIngestResult{}, ErrDenied
		}
	}
	if s.Repo == nil {
		return TraceIngestResult{}, ErrUnavailable
	}
	prepared.actor = actor
	var out TraceIngestResult
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkSkillUsageEvents(ctx, tx, in.Scope, in.Events, s.SkillScope); err != nil {
			return err
		}
		store, ok := tx.(completedTraceStore)
		if !ok {
			return ErrUnavailable
		}
		var storeErr error
		out, storeErr = store.storeCompletedTrace(ctx, prepared)
		return storeErr
	})
	if err != nil {
		return TraceIngestResult{}, err
	}
	if learning, lerr := s.reflectFailedUsage(requestCtx, in, out); lerr != nil {
		out.LearningError, _ = errorResponse(lerr)
	} else {
		out.Learning = learning
	}
	return out, nil
}

func prepareCompletedTrace(in TraceIngestInput) (preparedTrace, []byte, error) {
	agent := cmp.Or(in.Scope.AgentID, in.AgentRun.AgentID)
	if !validScope(in.Scope) || !traceIdentifier(agent) || in.AgentRun.AgentID != "" && in.AgentRun.AgentID != agent || !traceIdentifier(in.TraceID) || !traceIdentifier(in.ThreadID) || in.LangSmithRunID != "" && !traceIdentifier(in.LangSmithRunID) || len(in.Events) == 0 || len(in.Events) > maxTraceEvents {
		return preparedTrace{}, nil, ErrInvalid
	}
	if in.AgentRun.ID == "" {
		in.AgentRun.ID = "run-" + ContentHash("agent-run:" + in.TraceID)[:32]
	}
	if in.AgentRun.OriginAgentID != "" && !validRunLabel(in.AgentRun.OriginAgentID, 256) {
		return preparedTrace{}, nil, ErrInvalid
	}
	if !traceIdentifier(in.AgentRun.ID) || !validRunLabel(in.AgentRun.TaskType, 128) || !terminalOutcome(in.AgentRun.Outcome) || !traceTime(in.AgentRun.StartedAt) || !traceTime(in.AgentRun.FinishedAt) || in.AgentRun.FinishedAt.Before(in.AgentRun.StartedAt) || in.AgentRun.FinishedAt.After(time.Now()) || strings.TrimSpace(in.AgentRun.Text) == "" || len(in.AgentRun.Text) > 64000 || !utf8.ValidString(in.AgentRun.Text) {
		return preparedTrace{}, nil, ErrInvalid
	}
	if containsSecret(in.AgentRun.Text) {
		return preparedTrace{}, nil, ErrSecretDetected
	}
	in.AgentRun.StartedAt = in.AgentRun.StartedAt.UTC().Truncate(time.Millisecond)
	in.AgentRun.FinishedAt = in.AgentRun.FinishedAt.UTC().Truncate(time.Millisecond)
	seen := make(map[string]bool, len(in.Events))
	lineageSeen := map[string]bool{}
	lineageByEvent := make(map[string][]string, len(in.Events))
	checked := make([]inspectedTraceEvent, 0, len(in.Events))
	sensitivity := "public"
	for i := range in.Events {
		event := in.Events[i]
		if !traceIdentifier(event.ID) || seen[event.ID] || !validRunLabel(event.Kind, 128) || !traceTime(event.OccurredAt) || event.OccurredAt.Before(in.AgentRun.StartedAt) || event.OccurredAt.After(in.AgentRun.FinishedAt) || len(event.Payload) == 0 || len(event.Payload) > maxTraceEventBytes || !validSensitivity(event.Sensitivity) {
			return preparedTrace{}, nil, ErrInvalid
		}
		seen[event.ID] = true
		inspection, err := inspectTraceJSON(event.Payload)
		if err != nil {
			return preparedTrace{}, nil, err
		}
		lineage, err := tracePayloadSourceIDs(event.Payload)
		if err != nil {
			return preparedTrace{}, nil, err
		}
		// Payloads become note sources whose origin_agent_id is shown as provenance.
		var origin struct {
			OriginAgentID *string `json:"origin_agent_id"`
		}
		if json.Unmarshal(event.Payload, &origin) != nil || origin.OriginAgentID != nil && *origin.OriginAgentID != in.AgentRun.OriginAgentID {
			return preparedTrace{}, nil, ErrInvalid
		}
		lineageByEvent[event.ID] = lineage
		for _, id := range lineage {
			lineageSeen[id] = true
		}
		if event.Sensitivity == "" {
			event.Sensitivity = "internal"
		}
		if inspection.PersonalData && event.Sensitivity != "restricted" {
			event.Sensitivity = "private"
		}
		event.OccurredAt = event.OccurredAt.UTC().Truncate(time.Millisecond)
		in.Events[i] = event
		checked = append(checked, inspectedTraceEvent{TraceEventInput: event, Inspection: inspection})
		if sensitivityRank(event.Sensitivity) > sensitivityRank(sensitivity) {
			sensitivity = event.Sensitivity
		}
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > maxTraceEvents*maxTraceEventBytes+(1<<20) {
		return preparedTrace{}, nil, ErrInvalid
	}
	lineage := make([]string, 0, len(lineageSeen))
	for id := range lineageSeen {
		lineage = append(lineage, id)
	}
	slices.Sort(lineage)
	if len(in.Events)+len(lineage) > 64 {
		return preparedTrace{}, nil, ErrInvalid
	}
	return preparedTrace{input: in, events: checked, lineageByEvent: lineageByEvent, lineageSourceIDs: lineage, requestHash: ContentHash(string(raw)), sensitivity: sensitivity}, raw, nil
}

func tracePayloadSourceIDs(raw json.RawMessage) ([]string, error) {
	type provenance struct {
		SourceEventID string      `json:"source_event_id"`
		TraceID       string      `json:"trace_id"`
		Sources       []SourceRef `json:"sources"`
	}
	var payload struct {
		SourceDependencies []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"source_dependencies"`
		MemoryContext *struct {
			Items []struct {
				provenance
				MemoryProvenance *provenance `json:"memory_provenance"`
			} `json:"items"`
		} `json:"memory_context"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	if len(payload.SourceDependencies) > 64 {
		return nil, ErrInvalid
	}
	for _, ref := range payload.SourceDependencies {
		if ref.Kind != "source_event" || !traceIdentifier(ref.ID) {
			return nil, ErrInvalid
		}
		seen[ref.ID] = true
	}
	if payload.MemoryContext == nil {
		return sortedSet(seen), nil
	}
	for _, item := range payload.MemoryContext.Items {
		values := []provenance{item.provenance}
		if item.MemoryProvenance != nil {
			values = append(values, *item.MemoryProvenance)
		}
		itemHasSource := false
		for _, value := range values {
			ids := []string{value.SourceEventID}
			for _, source := range value.Sources {
				ids = append(ids, source.SourceID)
			}
			for _, id := range ids {
				if id == "" {
					continue
				}
				if !traceIdentifier(id) {
					return nil, ErrInvalid
				}
				itemHasSource = true
				seen[id] = true
			}
			if value.TraceID != "" && !traceIdentifier(value.TraceID) {
				return nil, ErrInvalid
			}
		}
		// A trace-only snapshot cannot be followed back to the source whose
		// deletion must erase this copied context. Fail closed at ingestion.
		if !itemHasSource {
			return nil, ErrInvalid
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func inspectTraceJSON(raw json.RawMessage) (Inspection, error) {
	inspection := Inspection{RulesVersion: inspectionVersion}
	// Scan the serialized object as well as individual string values so
	// key/value forms such as {"password":"..."} use the shared rules.
	if containsSecret(string(raw)) {
		return inspection, ErrSecretDetected
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return inspection, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return inspection, ErrInvalid
	}
	var walk func(any) error
	walk = func(value any) error {
		switch typed := value.(type) {
		case string:
			if containsSecret(typed) {
				return ErrSecretDetected
			}
			inspection.PersonalData = inspection.PersonalData || personalPattern.MatchString(typed)
		case []any:
			for _, item := range typed {
				if err := walk(item); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, item := range typed {
				if containsSecret(key) {
					return ErrSecretDetected
				}
				if err := walk(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspection, walk(value)
}

func traceIdentifier(value string) bool {
	return validRunLabel(value, 256) && !containsSecret(value)
}

func traceTime(value time.Time) bool {
	return storableTime(value) && value.Equal(value.Truncate(time.Millisecond))
}

func traceWriterRole(role string) bool {
	return role == "human" || role == "agent" || role == "service" || role == "pipeline"
}

func sensitivityRank(value string) int {
	switch value {
	case "restricted":
		return 3
	case "private":
		return 2
	case "internal":
		return 1
	default:
		return 0
	}
}
