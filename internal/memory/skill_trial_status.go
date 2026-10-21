package memory

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// SkillTrialStatus exposes canonical lifecycle and worker scheduling only. It
// does not run the observer, check a skill, roll back, or infer test success.
type SkillTrialStatus struct {
	Authority        string                     `json:"authority"`
	Scope            Scope                      `json:"scope"`
	SkillID          string                     `json:"skill_id"`
	Trial            Ref                        `json:"trial"`
	Candidate        Ref                        `json:"candidate"`
	State            string                     `json:"state"`
	ObservationState string                     `json:"observation_state"`
	Scheduler        *SkillTrialSchedulerStatus `json:"scheduler,omitempty"`
}
type SkillTrialSchedulerStatus struct {
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	NextCheck *time.Time `json:"next_check,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (s *Service) ReadSkillTrialStatus(ctx context.Context, token, id string, in SkillTrialReadInput) (SkillTrialStatus, error) {
	out := SkillTrialStatus{Authority: AuthorityDataOnly, Scope: in.Scope, SkillID: in.SkillID, ObservationState: "not_available"}
	if !validRunLabel(id, 128) {
		return out, ErrInvalid
	}
	if err := s.authorizeTrialRead(ctx, token, in.Scope, in.SkillID); err != nil {
		return out, err
	}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		trial, err := tx.Get(ctx, colSkillTrials, in.Scope, id)
		if err != nil {
			return err
		}
		if trial.SkillTrial == nil || trial.SkillID != in.SkillID || trial.ContentErased || trial.PrivacyQuarantined {
			return ErrNotFound
		}
		parent, err := tx.Get(ctx, colSkills, in.Scope, in.SkillID)
		if err != nil {
			return err
		}
		candidate, err := tx.Get(ctx, colSkillVersions, in.Scope, trial.SkillTrial.Candidate.ID)
		if err != nil {
			return err
		}
		if parent.ContentErased || parent.PrivacyQuarantined || candidate.ContentErased || candidate.PrivacyQuarantined || candidate.SkillID != in.SkillID {
			return ErrNotFound
		}
		out.Trial = Ref{Collection: colSkillTrials, ID: id, Revision: trial.Revision}
		out.Candidate = Ref{Collection: colSkillVersions, ID: candidate.ID, Revision: candidate.Revision}
		state, err := skillTrialTerminal(ctx, tx, trial)
		if err != nil {
			return err
		}
		if state == "" {
			state = "active"
			if _, err := skillTrialCandidate(ctx, tx, in.Scope, in.SkillID, trial.SkillTrial); err != nil {
				if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
					return err
				}
				state = "invalidated"
			}
		}
		if state == "stopped" && trial.VerificationStatus == "regression_observed" {
			state = "stopped_regression"
		}
		out.State = state
		job, err := tx.Get(ctx, colJobs, in.Scope, skillTrialCheckID(in.Scope, id))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if job.ContentErased || job.PrivacyQuarantined || job.Kind != skillTrialCheckKind || job.SkillID != in.SkillID || job.Target == nil || job.Target.Collection != colSkillTrials || job.Target.ID != id {
			return ErrConflict
		}
		out.Scheduler = &SkillTrialSchedulerStatus{Status: job.Status, Attempts: job.Attempts, NextCheck: job.RunAt, UpdatedAt: job.UpdatedAt}
		return nil
	})
	return out, err
}
func skillTrialStatusRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/skill-trials/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		value, err := s.ReadSkillTrialStatus(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, value, err)
	})
}
