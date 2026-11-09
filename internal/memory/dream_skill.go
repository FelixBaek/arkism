package memory

// Skill promotion: a lesson the agent learned again and again (the same or reworded lesson on one task
// type across at least three runs, confidence at least 0.7: promotionReason) becomes a skill. The dream
// drafts it with its model through the skill generation path, updating the catalog skill the skill
// search finds for the lesson when there is one, and makes the draft the skill's active version. Undo
// puts the skill's earlier active version back. Each lesson group is promoted once: a later run neither
// drafts it again nor brings back a promotion a person undid.

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// skills promotes the qualifying lesson groups of every task type with a lesson waiting.
func (d *dreamer) skills(ctx context.Context) error {
	if !d.modelReady() {
		return nil
	}
	subjects, err := d.lessonSubjects(ctx)
	if err != nil || len(subjects) == 0 {
		return err
	}
	if !d.allowed(ctx, "skills", "analyze:skills", "read:skills", "write:skills", "write:skill_versions", "write:sources", "apply:skills") {
		return nil
	}
	for _, subject := range subjects {
		if stop, err := d.promoteSubject(ctx, subject); err != nil || stop {
			return err
		}
	}
	return nil
}

// promoteSubject promotes the qualifying lesson groups of one task type. stop: the run is out of time or calls.
func (d *dreamer) promoteSubject(ctx context.Context, subject string) (bool, error) {
	page, err := d.s.ListReflections(ctx, d.token, d.scope, subject)
	if changed(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	for _, g := range page.Items {
		if g.Status != "candidate" || promotionReason(g.Status, g.DistinctRuns, g.Confidence) != "" {
			continue
		}
		if d.stop() {
			return true, nil
		}
		if stop, err := d.promote(ctx, subject, g); err != nil || stop {
			return stop, err
		}
	}
	return false, nil
}

// lessonSubjects: the task types of waiting lessons, in the order first seen (newest first).
func (d *dreamer) lessonSubjects(ctx context.Context) ([]string, error) {
	rows, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, "memory_candidates", "candidate", dreamScanMax)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range rows {
		if r.Kind == "lesson" && len(r.Keywords) > 0 && slices.Contains(r.Tags, learningTag(r.Keywords[0])) && !slices.Contains(out, r.Keywords[0]) && validRunLabel(r.Keywords[0], 128) {
			out = append(out, r.Keywords[0])
		}
	}
	return out, nil
}

// dreamSkillTarget is the skill a lesson group updates: the one its subject names (skill:<id>), else
// the catalog skill the skill search finds for the lesson, else a new skill named after the subject.
func (d *dreamer) dreamSkillTarget(ctx context.Context, subject, lesson string) (string, string) {
	id := strings.TrimPrefix(subject, "skill:")
	if id == subject {
		if hint := d.s.existingSkillFor(ctx, d.token, d.scope, lesson); hint != "" {
			id = hint
		}
	}
	path := "/skills/" + id + "/SKILL.md"
	if parent, err := d.s.Repo.Get(ctx, colSkills, d.scope, id); err == nil && parent.ActiveVersion != "" {
		if v, err := d.s.Repo.Get(ctx, colSkillVersions, d.scope, parent.ActiveVersion); err == nil && v.Analysis != nil && strings.HasSuffix(v.Analysis.SourcePath, "SKILL.md") {
			path = v.Analysis.SourcePath
		}
	}
	return id, path
}

// promote drafts one lesson group into a skill version and activates it. stop: the call budget is spent.
func (d *dreamer) promote(ctx context.Context, subject string, g ReflectionView) (bool, error) {
	target := "skills/" + subject + "#" + ContentHash(g.Interpretation)[:12]
	lessons := d.oneLessonPerRun(ctx, g.CandidateRefs)
	rev := int64(len(lessons))
	eventID := "dream-skill-" + ContentHash(subject + "\n" + g.Interpretation)[:40]
	if d.skipped(target, rev) {
		return false, nil
	}
	if _, err := d.s.Repo.Get(ctx, colJobs, d.scope, skillGenerationReceiptID(d.scope, Decision{PrincipalID: d.principal, Role: roleService}, eventID)); err == nil {
		d.skip(target, rev, "skill_already_promoted", g.Confidence) // promoted, or undone by a person: never again
		return false, nil
	}
	if d.run.ModelCalls >= d.set.modelCalls {
		d.hit("model_limit")
		return true, nil
	}
	skillID, path := d.dreamSkillTarget(ctx, subject, g.Interpretation)
	before, err := d.s.Repo.Get(ctx, colSkills, d.scope, skillID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	plan := SkillComparisonPlan{BaselineVersion: before.ActiveVersion, DatasetSHA256: ContentHash(structuredText(lessons)), EnvironmentSHA256: ContentHash("dream:" + dreamPromptVersion), MinScoreGain: 0.05, MaxDurationMS: 600000}
	in := GenerateSkillInput{Scope: d.scope, EventID: eventID, SkillID: skillID, Path: path, Lessons: lessons, ComparisonPlan: &plan, drafter: &ChatSkillDrafter{Config: d.s.DreamModel}}
	callCtx, cancel := context.WithDeadline(ctx, d.deadline.Add(dreamCallGrace))
	defer cancel()
	d.run.ModelCalls++ // counted when asked: a refused draft may still have reached the model
	made, err := d.s.GenerateSkill(callCtx, d.token, in)
	switch {
	case errors.Is(err, ErrModelBudget):
		d.hit("model_limit")
		return true, nil
	case err != nil || made.Reused:
		d.skip(target, rev, "skill_draft_refused", g.Confidence)
		return false, nil
	}
	return false, d.activate(ctx, made, before.ActiveVersion, target, rev, g)
}

// oneLessonPerRun keeps the first lesson of each run (skill generation counts runs, not rows), at most 16.
func (d *dreamer) oneLessonPerRun(ctx context.Context, refs []Ref) []Ref {
	out, runs := []Ref{}, map[string]bool{}
	for _, ref := range refs {
		r, err := d.s.Repo.Get(ctx, ref.Collection, d.scope, ref.ID)
		if err != nil {
			continue
		}
		run, _, ok := learningRelationRefs(r.Relations)
		if ok && !runs[run.ID] && len(out) < 16 {
			runs[run.ID] = true
			out = append(out, ref)
		}
	}
	return out
}

// activate makes the drafted version the skill's active one and retires the version it replaces, in
// one transaction that checks nothing moved since the draft.
func (d *dreamer) activate(ctx context.Context, made ImportSkillResult, beforeActive, target string, rev int64, g ReflectionView) error {
	v := made.Record
	rows := []dreamRow{{Collection: colSkills, ID: v.SkillID, Header: true, BeforeActive: beforeActive}, {Collection: colSkillVersions, ID: v.ID}}
	if beforeActive != "" {
		rows = append(rows, dreamRow{Collection: colSkillVersions, ID: beforeActive, BeforeStatus: statusActive})
	}
	err := d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error { return d.activateInTx(ctx, tx, v, beforeActive, rows) })
	if changed(err) {
		d.skip(target, rev, "changed", g.Confidence)
		return nil
	} else if err != nil {
		return err
	}
	reason := "lesson repeated in " + strconv.Itoa(g.DistinctRuns) + " runs"
	if beforeActive != "" {
		reason += ", replaces " + beforeActive
	}
	return d.record(ctx, DreamChange{Op: "promote_skill", Target: colSkills + "/" + v.SkillID, Reason: reason, Confidence: g.Confidence, Before: beforeActive, After: v.ID}, rows)
}

// activateInTx makes v the skill's active version if nothing moved since the draft, and logs the rows.
func (d *dreamer) activateInTx(ctx context.Context, tx Tx, v Record, beforeActive string, rows []dreamRow) error {
	skill, err := tx.Get(ctx, colSkills, d.scope, v.SkillID)
	if err != nil {
		return err
	}
	cur, err := tx.Get(ctx, colSkillVersions, d.scope, v.ID)
	if err != nil {
		return err
	}
	if cur.Revision != v.Revision || cur.Status != "candidate" || skill.ActiveVersion != beforeActive || cur.BaseVersion != beforeActive || !live(skill) {
		return ErrConflict
	}
	if beforeActive != "" {
		if err = supersedeSkillVersion(ctx, tx, d.scope, beforeActive); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	cur.Status, cur.ApprovedBy, cur.DreamPromoted, cur.Revision, cur.UpdatedAt = statusActive, d.principal, d.runID, cur.Revision+1, now
	if err = tx.Replace(ctx, colSkillVersions, cur, v.Revision); err != nil {
		return err
	}
	if err = enqueue(ctx, tx, colSkillVersions, cur); err != nil {
		return err
	}
	prev := skill.Revision
	skill.ActiveVersion, skill.Revision, skill.UpdatedAt = cur.ID, prev+1, now
	if err = tx.Replace(ctx, colSkills, skill, prev); err != nil {
		return err
	}
	if err = audit(ctx, tx, d.scope, d.principal, "dream:apply:skills", skill.ID); err != nil {
		return err
	}
	return afterRevisions(ctx, tx, d.scope, rows)
}
