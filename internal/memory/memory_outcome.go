package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type MemoryOutcomeInput struct {
	Scope         Scope  `json:"scope"`
	Target        Ref    `json:"target"`
	Trace         Ref    `json:"trace"`
	Evidence      []Ref  `json:"evidence"`
	EventID       string `json:"event_id"`
	CheckerSHA256 string `json:"checker_sha256"`
	Dimension     string `json:"dimension"`
	Passed        bool   `json:"passed"`
}

func (v *MemoryOutcomeInput) UnmarshalJSON(data []byte) error {
	type plain MemoryOutcomeInput
	return decodeComparison(data, (*plain)(v), []string{"scope", "target", "trace", "evidence", "event_id", "checker_sha256", "dimension", "passed"})
}
func memoryOutcomeRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/memory-outcomes", func(w http.ResponseWriter, r *http.Request) {
		var in MemoryOutcomeInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RecordMemoryOutcome(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
func (s *Service) RecordMemoryOutcome(ctx context.Context, token string, in MemoryOutcomeInput) (Ref, error) {
	if !validScope(in.Scope) || in.Target.Collection != "memories" || in.Trace.Collection != "execution_traces" || !validWorkCheckpointRef(in.Target) || !validWorkCheckpointRef(in.Trace) || !workCheckpointIDPattern.MatchString(in.EventID) || !sourceHashPattern.MatchString(in.CheckerSHA256) || len(in.Evidence) < 1 || len(in.Evidence) > 64 || (in.Dimension != "personal_compliance" && in.Dimension != "work_task") {
		return Ref{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "write:evaluations", in.Scope, "", "")
	if e != nil {
		return Ref{}, e
	}
	if actor.Role != roleService {
		return Ref{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	if _, e = s.authorize(ctx, token, "read:agent_runs", in.Scope, "", ""); e != nil {
		return Ref{}, e
	}
	for _, ref := range append(append([]Ref{}, in.Evidence...), in.Target, in.Trace) {
		if !validWorkCheckpointRef(ref) || (ref != in.Target && ref != in.Trace && ref.Collection != "sources") {
			return Ref{}, ErrInvalid
		}
		if _, e = s.authorize(ctx, token, "read:"+ref.Collection, in.Scope, ref.ID, ""); e != nil {
			return Ref{}, e
		}
	}
	id := "memory-outcome-" + ContentHash(structuredText(in.Scope)+"\n"+actor.PrincipalID+"\n"+in.EventID)
	hash := ContentHash(structuredText(in))
	result := Ref{Collection: "evaluations", ID: id, Revision: 1}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		read := func(ref Ref) (Record, error) {
			r, e := tx.Get(ctx, ref.Collection, in.Scope, ref.ID)
			if e != nil {
				return r, e
			}
			if r.Revision != ref.Revision || !live(r) || r.ContentErased || r.PrivacyQuarantined {
				return r, ErrConflict
			}
			if e = checkSources(ctx, tx, r); e != nil {
				return r, e
			}
			return r, nil
		}
		target, e := read(in.Target)
		if e != nil {
			return e
		}
		if target.CreatedBy == actor.PrincipalID || target.ApprovedBy == actor.PrincipalID {
			return ErrDenied
		}
		trace, e := read(in.Trace)
		if e != nil {
			return e
		}
		if trace.CreatedBy == actor.PrincipalID {
			return ErrDenied
		}
		completed := false
		for _, ref := range trace.Relations {
			if ref.Collection == "agent_runs" {
				run, e := tx.Get(ctx, "agent_runs", in.Scope, ref.ID)
				if e != nil {
					return e
				}
				if run.Run != nil && run.Run.TaskType == "crew_run" {
					return ErrDenied // OI-25: crew runner traces never judge memories
				}
				if run.Revision == ref.Revision && live(run) && !run.ContentErased && !run.PrivacyQuarantined && run.Run != nil && run.Run.FinishedAt != nil && terminalOutcome(run.Run.Outcome) {
					completed = true
				}
			}
		}
		if !completed {
			return ErrConflict
		}
		allowed := map[string]bool{}
		for _, ref := range trace.Sources {
			allowed[ref.SourceID] = true
		}
		seen := map[string]bool{}
		sources := []SourceRef{}
		relations := []Ref{in.Target, in.Trace}
		exposed := false
		for _, ref := range in.Evidence {
			if seen[ref.ID] || !allowed[ref.ID] {
				return ErrInvalid
			}
			seen[ref.ID] = true
			source, e := read(ref)
			if e != nil {
				return e
			}
			if source.TraceID != trace.ID || source.SourceEventID == "" || source.CreatedBy == actor.PrincipalID {
				return ErrConflict
			}
			var payload struct {
				Retrieved     []Ref `json:"retrieved"`
				MemoryContext *struct {
					Items []Ref `json:"items"`
				} `json:"memory_context"`
			}
			if json.Unmarshal([]byte(source.Text), &payload) != nil {
				return ErrInvalid
			}
			for _, r := range payload.Retrieved {
				if r == in.Target {
					exposed = true
				}
			}
			sources = append(sources, sourceReference(source, "memory-outcome"))
			relations = append(relations, ref)
		}
		if !exposed {
			return ErrDenied
		}
		old, e := tx.Get(ctx, "evaluations", in.Scope, id)
		if e == nil {
			if old.RequestHash != hash || !live(old) || old.ContentErased || old.PrivacyQuarantined {
				return ErrConflict
			}
			return checkEvaluationEvidence(ctx, tx, old)
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		now := time.Now().UTC()
		passed := in.Passed
		record := Record{ID: id, Scope: in.Scope, Kind: "memory_outcome", Status: "active", Revision: 1, Text: "Independent pinned memory checker " + in.CheckerSHA256, Tags: []string{"dimension:" + in.Dimension}, Sources: sources, Relations: relations, Target: &in.Target, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}, Passed: passed, RequestHash: hash, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
		if e = checkEvaluationEvidence(ctx, tx, record); e != nil {
			return e
		}
		if e = insertSourceRecord(ctx, tx, "evaluations", record); e != nil {
			return e
		}
		if e = recordMemoryEvaluation(ctx, tx, record); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "evaluate:memory_outcome", id)
	})
	if e != nil {
		return Ref{}, e
	}
	return result, nil
}
