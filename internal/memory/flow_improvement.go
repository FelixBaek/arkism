package memory

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

const flowImprovementKind = "flow_skill_improvement"

// FlowImprovement links existing canonical evidence and skill lifecycles. It
// never stores prompts, grants authority, or treats its stage as an evaluation.
type FlowImprovement struct {
	SourceRun        Ref                      `json:"source_run" bson:"source_run"`
	Generation       GenerateSkillInput       `json:"generation" bson:"generation"`
	Runtime          string                   `json:"runtime" bson:"runtime"`
	TrialEndsAt      time.Time                `json:"trial_ends_at" bson:"trial_ends_at"`
	Stage            string                   `json:"stage" bson:"stage"`
	Candidate        Ref                      `json:"candidate" bson:"candidate"`
	Comparison       Ref                      `json:"comparison" bson:"comparison"`
	Trial            Ref                      `json:"trial" bson:"trial"`
	TrialComparison  Ref                      `json:"trial_comparison" bson:"trial_comparison"`
	ObservationState string                   `json:"observation_state,omitempty" bson:"observation_state,omitempty"`
	Observations     []SkillTrialObservation  `json:"observations,omitempty" bson:"observations,omitempty"`
	Pending          *FlowImprovementAttempt  `json:"pending,omitempty" bson:"pending,omitempty"`
	Receipts         []FlowImprovementReceipt `json:"receipts" bson:"receipts"`
}

type FlowImprovementAttempt struct {
	ActionID      string    `json:"action_id" bson:"action_id"`
	Action        string    `json:"action" bson:"action"`
	RequestHash   string    `json:"request_hash" bson:"request_hash"`
	PrincipalID   string    `json:"principal_id" bson:"principal_id"`
	Role          string    `json:"role" bson:"role"`
	SkillRevision int64     `json:"skill_revision" bson:"skill_revision"`
	StartedAt     time.Time `json:"started_at" bson:"started_at"`
	State         string    `json:"state" bson:"state"`
}

type FlowImprovementReceipt struct {
	FlowImprovementAttempt `bson:",inline"`
	ErrorCode              string `json:"error_code,omitempty" bson:"error_code,omitempty"`
}

type FlowImprovementStart struct {
	Scope       Scope              `json:"scope"`
	RequestID   string             `json:"request_id"`
	SourceRun   Ref                `json:"source_run"`
	Generation  GenerateSkillInput `json:"generation"`
	Runtime     string             `json:"runtime"`
	TrialEndsAt time.Time          `json:"trial_ends_at"`
}

type FlowImprovementAdvance struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	ActionID         string `json:"action_id"`
	Action           string `json:"action"`
	// Recover only reconciles an uncertain attempt with canonical child records;
	// it cannot start another generation or repeat an unconfirmed application.
	Recover     bool                   `json:"recover,omitempty"`
	Comparison  *SkillComparisonResult `json:"comparison,omitempty"`
	Application *ChangeInput           `json:"application,omitempty"`
}

type FlowImprovementView struct {
	ID          string          `json:"id"`
	Revision    int64           `json:"revision"`
	Improvement FlowImprovement `json:"improvement"`
	Authority   string          `json:"authority"`
}

func improvementView(r Record) FlowImprovementView {
	return FlowImprovementView{r.ID, r.Revision, cloneImprovement(r.FlowImprovement), AuthorityDataOnly}
}

func validImprovementStart(in FlowImprovementStart) bool {
	return validScope(in.Scope) && workCheckpointIDPattern.MatchString(in.RequestID) &&
		in.SourceRun.Collection == colCrewRuns && validRunLabel(in.SourceRun.ID, 256) && in.SourceRun.Revision > 0 &&
		in.Generation.Scope == in.Scope && !invalidGenerateSkillInput(in.Generation) && trialRuntime(in.Runtime) &&
		!in.TrialEndsAt.IsZero() && storableTime(in.TrialEndsAt)
}

func (s *Service) StartFlowImprovement(ctx context.Context, token string, in FlowImprovementStart) (FlowImprovementView, error) {
	if !validImprovementStart(in) {
		return FlowImprovementView{}, ErrInvalid
	}
	in.Generation.Lessons = slices.Clone(in.Generation.Lessons)
	plan := *in.Generation.ComparisonPlan
	in.Generation.ComparisonPlan = &plan
	slices.SortFunc(in.Generation.Lessons, func(a, b Ref) int { return strings.Compare(a.ID, b.ID) })
	actor, err := s.authorize(ctx, token, "analyze:skills", in.Scope, in.Generation.SkillID, "")
	if err != nil {
		return FlowImprovementView{}, err
	}
	if !slices.Contains([]string{roleHuman, roleService, "agent", "pipeline"}, actor.Role) {
		return FlowImprovementView{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	state := FlowImprovement{SourceRun: in.SourceRun, Generation: in.Generation, Runtime: in.Runtime, TrialEndsAt: in.TrialEndsAt, Stage: "evidence_ready", Receipts: []FlowImprovementReceipt{}}
	if err = s.authorizeImprovementMaterial(ctx, token, in.Scope, &state); err != nil {
		return FlowImprovementView{}, err
	}
	id := "flow-improvement-" + ContentHash(structuredText(in.Scope)+"\n"+actor.PrincipalID+"\n"+in.RequestID)
	hash := ContentHash(structuredText(in))
	var saved Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		prior, e := tx.Get(ctx, colJobs, in.Scope, id)
		if e == nil {
			if !readableImprovement(prior) || prior.RequestHash != hash || prior.CreatedByType != actor.Role {
				return ErrConflict
			}
			saved = prior
			return checkImprovementSource(ctx, tx, in.Scope, state.SourceRun)
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if !in.TrialEndsAt.After(time.Now()) {
			return ErrInvalid
		}
		if e = checkImprovementSource(ctx, tx, in.Scope, state.SourceRun); e != nil {
			return e
		}
		now := time.Now().UTC()
		saved = Record{ID: id, Scope: in.Scope, Kind: flowImprovementKind, Status: "managed", Revision: 1, SkillID: in.Generation.SkillID, FlowImprovement: &state, RequestHash: hash, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
		if e = tx.Insert(ctx, colJobs, saved); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "start:flow_improvement", id)
	})
	if err != nil {
		return FlowImprovementView{}, err
	}
	if err = s.authorizeImprovementMaterial(ctx, token, in.Scope, saved.FlowImprovement); err != nil {
		return FlowImprovementView{}, err
	}
	return improvementView(saved), nil
}

func readableImprovement(r Record) bool {
	return r.Kind == flowImprovementKind && r.FlowImprovement != nil && !r.ContentErased && !r.PrivacyQuarantined
}

func checkImprovementSource(ctx context.Context, tx Tx, scope Scope, ref Ref) error {
	r, e := tx.Get(ctx, colCrewRuns, scope, ref.ID)
	if e != nil {
		return e
	}
	if !readableCrewRun(r) || r.Revision != ref.Revision || !terminalCrewRun(r.CrewRun.Status) {
		return ErrConflict
	}
	// Serialize against deletion/revision changes without changing the source.
	return tx.Replace(ctx, colCrewRuns, r, r.Revision)
}

func (s *Service) authorizeImprovementMaterial(ctx context.Context, token string, scope Scope, f *FlowImprovement) error {
	r, e := s.GetCrewRun(ctx, token, scope, f.SourceRun.ID)
	if e != nil {
		return e
	}
	if r.Revision != f.SourceRun.Revision || !terminalCrewRun(r.CrewRun.Status) {
		return ErrConflict
	}
	actor, e := s.authorize(ctx, token, "read:skills", scope, f.Generation.SkillID, "")
	if e != nil {
		return e
	}
	ctx = bindRequestActor(ctx, actor)
	// These are the existing independently grounded lessons, not a free-form
	// summary of SourceRun. Their own minimum-run/evaluation rules still apply.
	evidence, e := skillLessons(ctx, s.Repo, scope, f.Generation.Lessons)
	if e != nil {
		return e
	}
	if e = s.generationReadRefs(ctx, token, scope, evidence.Relations, evidence.Sources); e != nil {
		return e
	}
	for _, ref := range []Ref{f.Candidate, f.Comparison, f.TrialComparison} {
		if ref.ID == "" {
			continue
		}
		if _, e = s.Get(ctx, token, ref.Collection, scope, ref.ID); e != nil {
			return e
		}
	}
	if e = s.authorizeImprovementObservations(ctx, token, actor, scope, f.Observations); e != nil {
		return e
	}
	if f.Trial.ID != "" {
		return s.authorizeTrialRead(ctx, token, scope, f.Generation.SkillID)
	}
	return nil
}

func (s *Service) authorizeImprovementObservations(ctx context.Context, token string, actor Decision, scope Scope, items []SkillTrialObservation) error {
	for _, item := range items {
		if item.Scope.WorkspaceID != scope.WorkspaceID || item.Scope.ProjectID != scope.ProjectID || item.Scope.UserID != scope.UserID {
			return ErrDenied
		}
		// Read access follows current grants for each scope. Advancing trial or
		// monitor additionally enforces the current automation evidence policy.
		bound := actor
		bound.Scope = item.Scope
		scoped := bindRequestActor(ctx, bound)
		if _, e := s.Get(scoped, token, "sources", item.Scope, item.Source.ID); e != nil {
			return e
		}
		if item.Evaluation != nil {
			if _, e := s.Get(scoped, token, "evaluations", item.Scope, item.Evaluation.ID); e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Service) readFlowImprovement(ctx context.Context, token, id string, scope Scope) (Record, error) {
	if !validScope(scope) || !validRunLabel(id, 128) {
		return Record{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "read:skills", scope, "", "")
	if e != nil {
		return Record{}, e
	}
	ctx = bindRequestActor(ctx, actor)
	r, e := s.Repo.Get(ctx, colJobs, scope, id)
	if e != nil {
		return Record{}, e
	}
	if !readableImprovement(r) {
		return Record{}, ErrNotFound
	}
	if e = s.authorizeImprovementMaterial(ctx, token, scope, r.FlowImprovement); e != nil {
		return Record{}, e
	}
	return r, nil
}

func (s *Service) GetFlowImprovement(ctx context.Context, token, id string, scope Scope) (FlowImprovementView, error) {
	r, e := s.readFlowImprovement(ctx, token, id, scope)
	if e != nil {
		return FlowImprovementView{}, e
	}
	return improvementView(r), nil
}

func flowImprovementRoutes(mux routeRegistrar, s *Service) {
	flowImprovementListRoute(mux, s)
	mux.HandleFunc("POST /v1/flow/improvements", func(w http.ResponseWriter, r *http.Request) {
		var in FlowImprovementStart
		if !decode(w, r, &in) {
			return
		}
		out, e := s.StartFlowImprovement(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/flow/improvements/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.GetFlowImprovement(r.Context(), bearer(r), r.PathValue("id"), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/flow/improvements/{id}/advance", func(w http.ResponseWriter, r *http.Request) {
		var in FlowImprovementAdvance
		if !decode(w, r, &in) {
			return
		}
		out, e := s.AdvanceFlowImprovement(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
