package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"
)

// A watch is the owner's consent, given with the apply click, to roll a newly applied version back
// when real use gets worse. Checks run under the owner's own credential through the ordinary
// rollback path, so every approval and validation rule still applies; no new authority is created.
const (
	skillWatchKind        = "skill_watch"
	skillWatchPeriod      = 7 * 24 * time.Hour
	skillWatchMinFailures = 3
	// A previous version with no evaluation comes back only on a clear rise in failed real uses.
	skillWatchOutcomeMinFailures = 5
	// Open watches the worker checks per hourly pass, least recently checked first.
	skillWatchBatch = 50
)

type SkillWatch struct {
	Version         Ref       `json:"version"`
	PreviousVersion string    `json:"previous_version"`
	AppliedAt       time.Time `json:"applied_at"`
	Until           time.Time `json:"until"`
	// watching, kept (period over), ended (someone changed the active version),
	// rolled_back, or rollback_blocked (the previous version cannot be restored without a new evaluation).
	State            string    `json:"state"`
	Failures         int       `json:"failures"`
	BaselineFailures int       `json:"baseline_failures"`
	CheckedAt        time.Time `json:"checked_at"`
}

type SkillWatchInput struct {
	Scope     Scope  `json:"scope"`
	VersionID string `json:"version_id,omitempty"`
}

type SkillWatchView struct {
	SkillWatch
	// CanRollback: the previous version kept its own passing evaluation, so a rollback can succeed.
	CanRollback bool   `json:"can_rollback"`
	Authority   string `json:"authority"`
}

func skillWatchID(scope Scope, skillID string) string {
	return "skill-watch-" + ContentHash(structuredText(scope)+"/"+skillID)
}

func (s *Service) skillWatchActor(ctx context.Context, token string, scope Scope, skillID string, actions ...string) (Decision, error) {
	if !validScope(scope) || !validRunLabel(skillID, 256) {
		return Decision{}, ErrInvalid
	}
	var d Decision
	for _, action := range actions {
		var e error
		if d, e = s.authorize(ctx, token, action, scope, skillID, ""); e != nil {
			return d, e
		}
	}
	// Automation keeps its own deployment-owned trial monitor; a watch speaks for a person.
	if d.Role != roleHuman {
		return d, ErrDenied
	}
	return d, nil
}

func readSkillWatch(r Record) (SkillWatch, bool) {
	var w SkillWatch
	ok := r.Kind == skillWatchKind && !r.ContentErased && !r.PrivacyQuarantined && json.Unmarshal([]byte(r.Text), &w) == nil
	return w, ok
}

func (s *Service) watchView(ctx context.Context, scope Scope, w SkillWatch) SkillWatchView {
	previous, e := s.Repo.Get(ctx, colSkillVersions, scope, w.PreviousVersion)
	return SkillWatchView{SkillWatch: w, CanRollback: e == nil && previous.Status == "superseded" && (previous.ValidationID != "" || w.State == "watching"), Authority: AuthorityDataOnly}
}

// watchedPreviousVersion: during the watch the owner may go back by hand to the version in use right
// before the apply, even without an evaluation; they already used it. Any other rollback needs one.
func (s *Service) watchedPreviousVersion(ctx context.Context, in ChangeInput, skillID string) bool {
	record, e := s.Repo.Get(ctx, colJobs, in.Scope, skillWatchID(in.Scope, skillID))
	if e != nil {
		return false
	}
	w, ok := readSkillWatch(record)
	if !ok || record.SkillID != skillID || record.Status != "managed" || w.State != "watching" || w.PreviousVersion != in.VersionID || !time.Now().Before(w.Until) {
		return false
	}
	skill, e := s.Repo.Get(ctx, colSkills, in.Scope, skillID)
	// The revision is checked again inside the rollback transaction, so the applied version cannot change in between.
	return e == nil && skill.Revision == in.ExpectedRevision && skill.ActiveVersion == w.Version.ID
}

// WatchSkill starts watching the active version right after the owner applied it.
func (s *Service) WatchSkill(ctx context.Context, token, skillID string, in SkillWatchInput) (SkillWatchView, error) {
	actor, e := s.skillWatchActor(ctx, token, in.Scope, skillID, "apply:skills", "rollback:skills")
	if e != nil {
		return SkillWatchView{}, e
	}
	if !validRunLabel(in.VersionID, 256) {
		return SkillWatchView{}, ErrInvalid
	}
	ctx = bindRequestActor(ctx, actor)
	var out SkillWatch
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		out, e = startSkillWatch(ctx, tx, actor, skillID, in)
		return e
	})
	if e != nil {
		return SkillWatchView{}, e
	}
	return s.watchView(ctx, in.Scope, out), nil
}

// startSkillWatch writes the watch job for the active version; the same apply asked twice returns the existing watch.
func startSkillWatch(ctx context.Context, tx Tx, actor Decision, skillID string, in SkillWatchInput) (SkillWatch, error) {
	skill, e := tx.Get(ctx, colSkills, in.Scope, skillID)
	if e != nil {
		return SkillWatch{}, e
	}
	version, e := tx.Get(ctx, colSkillVersions, in.Scope, in.VersionID)
	if e != nil {
		return SkillWatch{}, e
	}
	if !live(skill) || skill.ActiveVersion != version.ID || version.SkillID != skillID || version.Status != "active" || version.BaseVersion == "" || !slices.Contains(skillHistory(skill), version.BaseVersion) {
		return SkillWatch{}, ErrConflict
	}
	now := time.Now().UTC()
	id := skillWatchID(in.Scope, skillID)
	prior, e := tx.Get(ctx, colJobs, in.Scope, id)
	if e == nil {
		if w, ok := readSkillWatch(prior); ok && w.Version.ID == version.ID && w.Version.Revision == version.Revision {
			return w, nil // the same apply asked twice
		}
	} else if !errors.Is(e, ErrNotFound) {
		return SkillWatch{}, e
	}
	out := SkillWatch{Version: Ref{Collection: colSkillVersions, ID: version.ID, Revision: version.Revision}, PreviousVersion: version.BaseVersion, AppliedAt: version.UpdatedAt, Until: version.UpdatedAt.Add(skillWatchPeriod), State: "watching", CheckedAt: now}
	record := Record{ID: id, Scope: in.Scope, Kind: skillWatchKind, Status: "managed", Revision: 1, SkillID: skillID, Target: &out.Version, Text: structuredText(out), CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
	if e == nil {
		record.Revision, record.CreatedAt = prior.Revision+1, prior.CreatedAt
		e = tx.Replace(ctx, colJobs, record, prior.Revision)
	} else {
		e = tx.Insert(ctx, colJobs, record)
	}
	if e != nil {
		return out, e
	}
	return out, audit(ctx, tx, in.Scope, actor.PrincipalID, "watch:skills", skillID)
}

// CheckSkillWatch is the owner's read of the watch; it also runs the check when one is due.
func (s *Service) CheckSkillWatch(ctx context.Context, token, skillID string, in SkillWatchInput) (SkillWatchView, error) {
	actor, e := s.skillWatchActor(ctx, token, in.Scope, skillID, "read:skills", "rollback:skills")
	if e != nil {
		return SkillWatchView{}, e
	}
	ctx = bindRequestActor(ctx, actor)
	record, e := s.Repo.Get(ctx, colJobs, in.Scope, skillWatchID(in.Scope, skillID))
	if e != nil {
		return SkillWatchView{}, e
	}
	w, ok := readSkillWatch(record)
	if !ok || record.SkillID != skillID {
		return SkillWatchView{}, ErrNotFound
	}
	if w, e = s.checkSkillWatch(ctx, token, actor, record, w); e != nil {
		return SkillWatchView{}, e
	}
	return s.watchView(ctx, in.Scope, w), nil
}

// RunSkillWatches is the worker's own pass over open watches, so a rollback does not wait for someone to
// open the skill. It acts on the consent the owner gave with the apply click, under the worker's credential.
func (s *Service) RunSkillWatches(ctx context.Context, workerToken string) (int, error) {
	lister, ok := s.Repo.(interface {
		OpenSkillWatches(context.Context, int) ([]Record, error)
	})
	if !ok {
		return 0, nil
	}
	rows, e := lister.OpenSkillWatches(ctx, skillWatchBatch)
	if e != nil {
		return 0, e
	}
	checked, failed := 0, error(nil)
	for _, record := range rows {
		w, ok := readSkillWatch(record)
		if !ok || w.State != "watching" || record.Status != "managed" {
			continue
		}
		var d Decision
		if d, e = s.authorize(ctx, workerToken, "rollback:skills", record.Scope, record.SkillID, ""); e == nil {
			_, e = s.authorize(ctx, workerToken, "read:skills", record.Scope, record.SkillID, "")
		}
		if e == nil && d.Role != roleService {
			e = ErrDenied
		}
		if e == nil {
			_, e = s.checkSkillWatch(bindRequestActor(ctx, d), workerToken, d, record, w)
		}
		if e != nil {
			failed = errors.Join(failed, e)
			continue
		}
		checked++
	}
	return checked, failed
}

// checkSkillWatch compares failed real uses of the new version with the previous version's failures
// over the same length of time before the apply, and rolls back when the new one is clearly worse.
func (s *Service) checkSkillWatch(ctx context.Context, token string, actor Decision, record Record, w SkillWatch) (SkillWatch, error) {
	if w.State != "watching" {
		return w, nil
	}
	scope, skillID := record.Scope, record.SkillID
	skill, e := s.Repo.Get(ctx, colSkills, scope, skillID)
	if e != nil {
		return w, e
	}
	now := time.Now().UTC()
	switch {
	case skill.ActiveVersion != w.Version.ID:
		w.State = "ended"
	case !now.Before(w.Until):
		w.State = "kept"
	default:
		if w, e = s.judgeSkillWatch(ctx, token, actor, record, skill, w, now); e != nil {
			return w, e
		}
	}
	w.CheckedAt = now
	e = s.saveSkillWatch(ctx, record, w, now)
	return w, e
}

// judgeSkillWatch counts failed uses on both sides of the apply and rolls back when the new version is clearly worse.
func (s *Service) judgeSkillWatch(ctx context.Context, token string, actor Decision, record, skill Record, w SkillWatch, now time.Time) (SkillWatch, error) {
	scope, skillID := record.Scope, record.SkillID
	current, e := s.Repo.Get(ctx, colSkillVersions, scope, w.Version.ID)
	if e != nil {
		return w, e
	}
	if w.Failures, e = s.watchFailures(ctx, token, scope, skillID, current, w.AppliedAt, now, true); e != nil {
		return w, e
	}
	previous, e := s.Repo.Get(ctx, colSkillVersions, scope, w.PreviousVersion)
	if e != nil {
		return w, e
	}
	if w.BaselineFailures, e = s.watchFailures(ctx, token, scope, skillID, previous, w.AppliedAt.Add(-now.Sub(w.AppliedAt)), w.AppliedAt, false); e != nil {
		return w, e
	}
	evaluated := previous.ValidationID != ""
	if !watchWorse(w.Failures, w.BaselineFailures, evaluated) {
		return w, nil
	}
	// The ordinary rollback; a previous version without an evaluation comes back on failed uses alone.
	_, e = s.applySkillVersion(ctx, actor, SkillAutomationPolicy{}, skillID, ChangeInput{Scope: scope, ExpectedRevision: skill.Revision, VersionID: w.PreviousVersion}, true, !evaluated)
	switch {
	case e == nil:
		w.State = "rolled_back"
	case errors.Is(e, ErrDenied) || errors.Is(e, ErrConflict) || errors.Is(e, ErrInvalid) || errors.Is(e, ErrNotFound):
		w.State = "rollback_blocked"
	default:
		return w, e
	}
	return w, nil
}

// saveSkillWatch stores the checked watch on its job, closing the job once the watch has ended.
func (s *Service) saveSkillWatch(ctx context.Context, record Record, w SkillWatch, now time.Time) error {
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, e := tx.Get(ctx, colJobs, record.Scope, record.ID)
		if e != nil {
			return e
		}
		if current.Revision != record.Revision {
			return ErrConflict
		}
		current.Text = structuredText(w)
		if w.State != "watching" {
			current.Status = JobStatusDone
		}
		current.Revision++
		current.UpdatedAt = now
		return tx.Replace(ctx, colJobs, current, record.Revision)
	})
}

// watchWorse: after an evaluated previous version, any rise past a few failures; without an evaluation the
// failures alone must be clear: at least skillWatchOutcomeMinFailures and at least twice the previous count.
func watchWorse(failures, baseline int, evaluated bool) bool {
	if evaluated {
		return failures >= skillWatchMinFailures && failures > baseline
	}
	return failures >= skillWatchOutcomeMinFailures && failures >= 2*baseline
}

// watchFailures counts failed uses of version that started in [from, to). strict also binds each use to
// the version's exact content, as the trial monitor does for the live version.
// shortcut: counts the use's own failure status, not independent failing ratings of a success; add those if owners rate uses.
func (s *Service) watchFailures(ctx context.Context, token string, scope Scope, skillID string, version Record, from, to time.Time, strict bool) (int, error) {
	reader, ok := s.Repo.(interface {
		SkillRegressionSourceIDs(context.Context, Scope, string, time.Time) ([]string, error)
	})
	if !ok {
		return 0, ErrUnavailable
	}
	ids, e := reader.SkillRegressionSourceIDs(ctx, scope, version.ID, from)
	if e != nil {
		return 0, e
	}
	n := 0
	for _, id := range ids {
		if _, e = s.authorize(ctx, token, "read:sources", scope, id, ""); e != nil {
			return 0, e
		}
		src, e := s.Repo.Get(ctx, "sources", scope, id)
		if e != nil {
			return 0, e
		}
		var u SkillUsage
		if !live(src) || src.ContentErased || json.Unmarshal([]byte(src.Text), &u) != nil || u.SkillID != skillID || u.VersionID != version.ID || u.OfflineCache || u.StartedAt.Before(from) || !u.StartedAt.Before(to) {
			continue
		}
		if strict && invalidRegressionUsage(src.Text, &u, version, skillID) {
			continue
		}
		if u.Status == "failure" || u.Status == "validation_failed" || u.ToolFailures > 0 || u.RuntimeErrors > 0 {
			n++
		}
	}
	return n, nil
}

func skillWatchRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/skills/{id}/watch", func(w http.ResponseWriter, r *http.Request) {
		var in SkillWatchInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.WatchSkill(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/skills/{id}/watch/check", func(w http.ResponseWriter, r *http.Request) {
		var in SkillWatchInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CheckSkillWatch(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}

// OpenSkillWatches lists open watches in every scope, least recently checked first.
func (p *Postgres) OpenSkillWatches(ctx context.Context, limit int) ([]Record, error) {
	rows, e := p.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection='jobs' AND kind=$1 AND status='managed' ORDER BY updated_at, id LIMIT $2`, skillWatchKind, limit)
	if e != nil {
		return nil, pgErr(e)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var payload []byte
		if e = rows.Scan(&payload); e != nil {
			return nil, ErrUnavailable
		}
		r, e := pgDecode(payload)
		if e != nil {
			return nil, ErrUnavailable
		}
		out = append(out, r)
	}
	return out, pgErr(rows.Err())
}
