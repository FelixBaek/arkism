package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"
)

const colSkillTrials = "skill_trials"

// SkillTrial contains only references and bounds. The active skill is never changed.
type SkillTrial struct {
	Candidate       Ref        `json:"candidate" bson:"candidate"`
	BaselineVersion string     `json:"baseline_version" bson:"baseline_version"`
	Runtime         string     `json:"runtime" bson:"runtime"`
	EndsAt          time.Time  `json:"ends_at" bson:"ends_at"`
	StoppedBy       string     `json:"stopped_by,omitempty" bson:"stopped_by,omitempty"`
	StoppedAt       *time.Time `json:"stopped_at,omitempty" bson:"stopped_at,omitempty"`
}
type SkillTrialInput struct {
	Scope     Scope     `json:"scope"`
	EventID   string    `json:"event_id"`
	SkillID   string    `json:"skill_id"`
	Candidate Ref       `json:"candidate"`
	Runtime   string    `json:"runtime"`
	EndsAt    time.Time `json:"ends_at"`
}

type SkillTrialStopInput struct {
	Scope            Scope  `json:"scope"`
	SkillID          string `json:"skill_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func trialRuntime(runtime string) bool {
	return slices.Contains([]string{"codex", "claude_code", "zcode", "gemini", "grok"}, runtime)
}

func skillTrialCandidate(ctx context.Context, tx Tx, scope Scope, skillID string, trial *SkillTrial) (Record, error) {
	parent, err := tx.Get(ctx, colSkills, scope, skillID)
	if err != nil {
		return Record{}, err
	}
	if !live(parent) || parent.ContentErased || parent.ActiveVersion != trial.BaselineVersion {
		return Record{}, ErrConflict
	}
	if err = checkSources(ctx, tx, parent); err != nil {
		return Record{}, err
	}
	if parent.ActiveVersion != "" {
		base, err := tx.Get(ctx, colSkillVersions, scope, parent.ActiveVersion)
		if err != nil {
			return Record{}, err
		}
		if err = checkActiveSkill(ctx, tx, base); err != nil {
			return Record{}, err
		}
	}
	candidate, err := tx.Get(ctx, colSkillVersions, scope, trial.Candidate.ID)
	if err != nil {
		return Record{}, err
	}
	if candidate.Revision != trial.Candidate.Revision || candidate.SkillID != skillID || candidate.Status != "approved" || candidate.ApprovedBy == "" || candidate.Analysis == nil || candidate.ComparisonPlan == nil || candidate.ContentErased || candidate.PrivacyQuarantined || candidate.EffectiveAt != nil && candidate.EffectiveAt.After(time.Now()) || candidate.BaseVersion != trial.BaselineVersion || candidate.ExpiresAt != nil && !candidate.ExpiresAt.After(time.Now()) || !slices.Contains(skillHistory(parent), candidate.ID) {
		return Record{}, ErrConflict
	}
	if effectiveCompatibility(candidate, parent)[trial.Runtime] == "unsupported" {
		return Record{}, ErrDenied
	}
	if err = checkSources(ctx, tx, candidate); err != nil {
		return Record{}, err
	}
	if err = checkRelations(ctx, tx, candidate); err != nil {
		return Record{}, err
	}
	if err = validation(ctx, tx, candidate, candidate.ValidationID, false); err != nil {
		return Record{}, err
	}
	return candidate, nil
}
func trialDomainError(err error) bool {
	return errors.Is(err, ErrConflict) || errors.Is(err, ErrDenied) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid)
}

func (s *Service) StartSkillTrial(ctx context.Context, token string, in SkillTrialInput) (Record, error) {
	if !validScope(in.Scope) || !idempotencyPattern.MatchString(in.EventID) || !validRunLabel(in.SkillID, 256) || in.Candidate.Collection != colSkillVersions || !validRunLabel(in.Candidate.ID, 256) || in.Candidate.Revision < 1 || !trialRuntime(in.Runtime) || in.EndsAt.IsZero() {
		return Record{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "apply:skills", in.Scope, in.SkillID, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		policy, e := s.authorizeSkillAutomation(ctx, token, d, in.Scope, in.SkillID, in.Runtime)
		if e != nil {
			return Record{}, e
		}
		if in.EndsAt.After(time.Now().Add(time.Duration(policy.MaxTrialSeconds)*time.Second)) || in.EndsAt.After(policy.ExpiresAt) {
			return Record{}, ErrDenied
		}
		ctx = bindRequestActor(ctx, d)
	}
	raw, _ := json.Marshal(in)
	binding, _ := json.Marshal([]any{in.Scope, d.PrincipalID, in.EventID})
	id, hash := "trial-"+ContentHash(string(binding)), ContentHash(string(raw))
	var result Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		prior, e := tx.Get(ctx, colSkillTrials, in.Scope, id)
		if e == nil {
			if prior.RequestHash != hash || prior.ContentErased || prior.PrivacyQuarantined {
				return ErrConflict
			}
			result = prior
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if !in.EndsAt.After(time.Now()) {
			return ErrInvalid
		}
		candidate, e := tx.Get(ctx, colSkillVersions, in.Scope, in.Candidate.ID)
		if e != nil {
			return e
		}
		trial := &SkillTrial{Candidate: in.Candidate, BaselineVersion: candidate.BaseVersion, Runtime: in.Runtime, EndsAt: in.EndsAt.UTC()}
		if _, e = skillTrialCandidate(ctx, tx, in.Scope, in.SkillID, trial); e != nil {
			return e
		}
		now := time.Now().UTC()
		result = Record{ID: id, Scope: in.Scope, Kind: "skill_trial", Status: "active", Revision: 1, SkillID: in.SkillID, SkillTrial: trial, RequestHash: hash, Sources: candidate.Sources, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
		if e = tx.Insert(ctx, colSkillTrials, result); e != nil {
			return e
		}
		if d.Role == roleService {
			if e = enqueueSkillTrialCheck(ctx, tx, result); e != nil {
				return e
			}
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "start:skill_trials", id)
	})
	return result, err
}
func (s *Service) StopSkillTrial(ctx context.Context, token, id string, in SkillTrialStopInput) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 128) || !validRunLabel(in.SkillID, 256) || in.ExpectedRevision < 1 {
		return Record{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "apply:skills", in.Scope, in.SkillID, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		if _, e := s.authorizeSkillAutomation(ctx, token, d, in.Scope, in.SkillID, ""); e != nil {
			return Record{}, e
		}
		ctx = bindRequestActor(ctx, d)
	}
	var result Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colSkillTrials, in.Scope, id)
		if e != nil {
			return e
		}
		if r.SkillTrial == nil || r.SkillID != in.SkillID || r.ContentErased || r.PrivacyQuarantined {
			return ErrNotFound
		}
		if r.Status == "stopped" && (r.Revision == in.ExpectedRevision || r.Revision == in.ExpectedRevision+1) {
			result = r
			return nil
		}
		if r.Revision != in.ExpectedRevision || r.Status != "active" {
			return ErrConflict
		}
		prev := r.Revision
		now := time.Now().UTC()
		trial := *r.SkillTrial
		trial.StoppedAt = &now
		trial.StoppedBy = d.PrincipalID
		r.SkillTrial = &trial
		r.Status = "stopped"
		r.Revision++
		r.UpdatedAt = now
		if e = tx.Replace(ctx, colSkillTrials, r, prev); e != nil {
			return e
		}
		result = r
		if e = closeSkillTrialCheck(ctx, tx, r); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "stop:skill_trials", id)
	})
	return result, err
}

// Explicit trial selection is checked online on every load. Pins remain replay-only.
func resolveSkillTrial(ctx context.Context, tx Tx, in SkillResolveInput, ref string) (ResolvedSkill, error) {
	r, err := tx.Get(ctx, colSkillTrials, in.Scope, in.TrialID)
	if err != nil {
		return ResolvedSkill{}, err
	}
	if r.SkillTrial == nil || r.ContentErased || r.PrivacyQuarantined || r.SkillTrial.Runtime != in.Runtime {
		return ResolvedSkill{}, ErrDenied
	}
	id, number, ok := parseSkillRef(ref)
	if !ok || id != r.SkillID || number != 0 {
		return ResolvedSkill{}, ErrInvalid
	}
	trialState := r.Status
	if trialState == "active" && !r.SkillTrial.EndsAt.After(time.Now()) {
		trialState = "expired"
	}
	if trialState == "active" {
		if _, err = skillTrialCandidate(ctx, tx, in.Scope, r.SkillID, r.SkillTrial); err != nil {
			if !trialDomainError(err) {
				return ResolvedSkill{}, err
			}
			trialState = "invalidated"
		}
	}
	chosen := ref
	if trialState == "active" {
		parent, e := tx.Get(ctx, colSkills, in.Scope, id)
		if e != nil {
			return ResolvedSkill{}, e
		}
		chosen = id + "@" + strconv.Itoa(slices.Index(skillHistory(parent), r.SkillTrial.Candidate.ID)+1)
	}
	item, err := resolveSkillRef(ctx, tx, in.Scope, chosen)
	item.Ref = ref
	item.TrialID = r.ID
	item.TrialStatus = trialState
	return item, err
}

// Inspection returns the version that a fresh online trial load would serve,
// without sending its instructions to the management screen.
type SkillTrialView struct {
	Record            Record `json:"record"`
	Status            string `json:"status"`
	ResolvedVersionID string `json:"resolved_version_id,omitempty"`
	ResolvedVersion   int    `json:"resolved_version,omitempty"`
	ResolutionError   string `json:"resolution_error,omitempty"`
}
type SkillTrialReadInput struct {
	Scope   Scope  `json:"scope"`
	SkillID string `json:"skill_id"`
}
type SkillTrialListInput struct {
	Scope       Scope  `json:"scope"`
	SkillID     string `json:"skill_id"`
	CandidateID string `json:"candidate_id"`
	Limit       int    `json:"limit"`
	After       string `json:"after,omitempty"`
}
type SkillTrialPage struct {
	Authority  string           `json:"authority"`
	Items      []SkillTrialView `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func (s *Service) authorizeTrialRead(ctx context.Context, token string, scope Scope, skillID string) error {
	if !validScope(scope) || !validRunLabel(skillID, 256) {
		return ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:skills", scope, skillID, ""); err != nil {
		return err
	}
	_, err := s.authorize(ctx, token, "read:skill_versions", scope, "", "")
	return err
}
func inspectSkillTrial(ctx context.Context, tx Tx, scope Scope, skillID, id string) (SkillTrialView, error) {
	r, err := tx.Get(ctx, colSkillTrials, scope, id)
	if err != nil {
		return SkillTrialView{}, err
	}
	if r.SkillID != skillID || r.SkillTrial == nil || r.PrivacyQuarantined || r.ContentErased {
		return SkillTrialView{}, ErrNotFound
	}
	item, err := resolveSkillTrial(ctx, tx, SkillResolveInput{Scope: scope, TrialID: id, Runtime: r.SkillTrial.Runtime}, skillID+"@latest-approved")
	if err != nil {
		return SkillTrialView{}, err
	}
	return SkillTrialView{Record: r, Status: item.TrialStatus, ResolvedVersionID: item.VersionID, ResolvedVersion: item.Version, ResolutionError: item.Error}, nil
}
func (s *Service) InspectSkillTrial(ctx context.Context, token, id string, in SkillTrialReadInput) (SkillTrialView, error) {
	if !validRunLabel(id, 128) {
		return SkillTrialView{}, ErrInvalid
	}
	if err := s.authorizeTrialRead(ctx, token, in.Scope, in.SkillID); err != nil {
		return SkillTrialView{}, err
	}
	var out SkillTrialView
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = inspectSkillTrial(ctx, tx, in.Scope, in.SkillID, id)
		return err
	})
	return out, err
}
func (s *Service) ListSkillTrials(ctx context.Context, token string, in SkillTrialListInput) (SkillTrialPage, error) {
	out := SkillTrialPage{Authority: AuthorityDataOnly, Items: []SkillTrialView{}}
	if !validRunLabel(in.CandidateID, 256) || in.Limit < 1 || in.Limit > 100 {
		return out, ErrInvalid
	}
	if err := s.authorizeTrialRead(ctx, token, in.Scope, in.SkillID); err != nil {
		return out, err
	}
	binding := ContentHash(structuredText([]any{in.Scope, in.SkillID, in.CandidateID}))
	after := ""
	if in.After != "" {
		if len(in.After) > 1024 {
			return out, ErrInvalid
		}
		raw, err := base64.RawURLEncoding.DecodeString(in.After)
		var cursor skillCursor
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Binding != binding || !validRunLabel(cursor.ID, 128) {
			return out, ErrInvalid
		}
		after = cursor.ID
	}
	rows, err := s.Repo.ListSkillTrialRecords(ctx, in.Scope, in.SkillID, in.CandidateID, in.Limit, after)
	if err != nil {
		return out, err
	}
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		raw, _ := json.Marshal(skillCursor{ID: rows[len(rows)-1].ID, Binding: binding})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Items = []SkillTrialView{}
		for _, row := range rows {
			item, e := inspectSkillTrial(ctx, tx, in.Scope, in.SkillID, row.ID)
			if errors.Is(e, ErrNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			if item.Record.SkillTrial.Candidate.ID != in.CandidateID {
				return ErrConflict
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	return out, err
}
