package memory

// Skill promotion: a lesson the agent learned again and again (the same or reworded lesson on one task
// type across at least three runs, confidence at least 0.7: promotionReason) becomes a skill. The dream
// drafts it with its model through the skill generation path, updating the catalog skill the skill
// search finds for the lesson when there is one, and makes the draft the skill's active version. Undo
// puts the skill's earlier active version back. A version promoted this way whose recorded uses mostly
// fail is taken back again (dream_skill_outcome.go). Each lesson group is promoted once: a later run neither
// drafts it again nor brings back a promotion a person undid.

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// skills promotes the qualifying lesson groups of every task type with a lesson waiting.
func (d *dreamer) skills(ctx context.Context) error {
	if err := d.demoteSkills(ctx); err != nil || !d.modelReady() {
		return err
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

// dreamSkillTarget is the skill a lesson group updates: the one its subject names (skill:<id>); for any
// other subject the catalog skill the skill search finds, or a new skill named after the subject, but
// only one the dream made itself. ok false: the only match is a skill a person owns.
func (d *dreamer) dreamSkillTarget(ctx context.Context, subject, lesson string) (string, string, bool) {
	id := strings.TrimPrefix(subject, "skill:")
	if id == subject {
		if hint := d.s.existingSkillFor(ctx, d.token, d.scope, lesson); hint != "" && d.dreamOwned(ctx, hint) {
			id = hint
		} else if !d.dreamOwned(ctx, id) {
			return "", "", false
		}
	}
	path := "/skills/" + id + "/SKILL.md"
	if v, ok := d.activeVersion(ctx, id); ok && v.Analysis != nil && strings.HasSuffix(v.Analysis.SourcePath, "SKILL.md") {
		path = v.Analysis.SourcePath
	}
	return id, path, true
}

// activeVersion is the skill's active version, when it has one.
func (d *dreamer) activeVersion(ctx context.Context, skillID string) (Record, bool) {
	parent, err := d.s.Repo.Get(ctx, colSkills, d.scope, skillID)
	if err != nil || parent.ActiveVersion == "" {
		return Record{}, false
	}
	v, err := d.s.Repo.Get(ctx, colSkillVersions, d.scope, parent.ActiveVersion)
	return v, err == nil
}

// dreamOwned: the skill does not exist yet, or a dream made its active version.
func (d *dreamer) dreamOwned(ctx context.Context, skillID string) bool {
	if _, err := d.s.Repo.Get(ctx, colSkills, d.scope, skillID); errors.Is(err, ErrNotFound) {
		return true
	}
	v, ok := d.activeVersion(ctx, skillID)
	return ok && v.DreamPromoted != ""
}

// promote drafts one lesson group into a skill version and activates it. stop: the call budget is spent.
// A group is named by its subject and its oldest lesson, so a reworded or reordered group is the same
// group: once promoted, or undone by a person, it is never drafted again.
func (d *dreamer) promote(ctx context.Context, subject string, g ReflectionView) (bool, error) {
	lessons, texts, oldest, external := d.groupLessons(ctx, g.CandidateRefs)
	eventID := "dream-skill-" + ContentHash(subject + "\n" + oldest)[:40]
	target, rev := "skills/"+subject+"#"+eventID[12:24], int64(len(lessons))
	if d.skipped(target, rev) {
		return false, nil
	}
	if reason := d.promotionBlocked(ctx, eventID, len(lessons), external); reason != "" {
		d.skip(target, rev, reason, g.Confidence)
		return false, nil
	}
	if d.run.ModelCalls >= d.set.modelCalls {
		d.hit("model_limit")
		return true, nil
	}
	skillID, path, ok := d.dreamSkillTarget(ctx, subject, g.Interpretation)
	if !ok {
		d.skip(target, rev, "skill_owned_by_person", g.Confidence)
		return false, nil
	}
	if slices.Contains(d.demotedNow, skillID) {
		// Writing the skill again in the run that took its version back would leave that step
		// impossible to undo. Not logged as a skip, so the next run drafts the group.
		return false, nil
	}
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
	if reason := d.draftRefusal(ctx, made.Record, before.ActiveVersion, texts); reason != "" {
		d.skip(target, rev, reason, g.Confidence) // the draft stays a candidate version for a person
		return false, nil
	}
	return false, d.activate(ctx, made, before.ActiveVersion, target, rev, g, lessons)
}

// promotionBlocked: why a lesson group is not drafted ("" when it may be).
func (d *dreamer) promotionBlocked(ctx context.Context, eventID string, lessons int, external bool) string {
	switch {
	case d.receipt(ctx, eventID):
		return "skill_already_promoted"
	case external:
		return "lesson_external_source" // content an agent captured from outside never writes a skill on its own
	case lessons < improvementMinRuns:
		return "lessons_already_promoted"
	}
	return ""
}

func (d *dreamer) receipt(ctx context.Context, eventID string) bool {
	_, err := d.s.Repo.Get(ctx, colJobs, d.scope, skillGenerationReceiptID(d.scope, Decision{PrincipalID: d.principal, Role: roleService}, eventID))
	return err == nil
}

// draftRefusal: why a draft is not activated without a person ("" when it may be). It must be usable
// as written (draftUnusable), add no
// address, command, tool grant or permission that neither the version it replaces nor the lessons
// carry, a revision must not be worse than the file it replaces (revisionWorse), and a skill the
// deployment runs trials for waits for its trial.
func (d *dreamer) draftRefusal(ctx context.Context, v Record, baseVersion string, lessons []string) string {
	allowed, baseFile := strings.Join(lessons, "\n"), ""
	var base *SkillAnalysis
	if b, err := d.s.Repo.Get(ctx, colSkillVersions, d.scope, baseVersion); baseVersion != "" && err == nil {
		baseFile, base = d.versionFile(ctx, b), b.Analysis
		allowed += "\n" + analysisProse(base) + baseFile
	}
	file := d.versionFile(ctx, v)
	if reason := draftUnusable(v.Analysis, file, baseFile != ""); reason != "" {
		return reason
	}
	if rule, token := riskyAddition(analysisProse(v.Analysis)+file, allowed, v.Analysis, base); rule != "" {
		return "skill_risky_addition:" + rule + ":" + token
	}
	if reason := revisionWorse(baseFile, file, lessons); baseFile != "" && reason != "" {
		return reason
	}
	if slices.ContainsFunc(d.s.SkillAutomationPolicies, func(p SkillAutomationPolicy) bool {
		return p.Scope == d.scope && slices.Contains(p.SkillIDs, v.SkillID) && p.ExpiresAt.After(time.Now())
	}) {
		return "skill_awaits_trial" // the trial path promotes it once it passes
	}
	return ""
}

// versionFile is the text of a skill version's file, each source after a line break.
func (d *dreamer) versionFile(ctx context.Context, v Record) string {
	out := ""
	for _, ref := range v.Sources {
		if src, err := d.s.Repo.Get(ctx, "sources", d.scope, ref.SourceID); err == nil && (src.Kind == "skill_file" || src.Kind == proposedSkillSourceKind) {
			out += "\n" + src.Text
		}
	}
	return out
}

// skillStepLine is a numbered step of a skill file: "1.", "1)", "Step 1", "1단계" or a circled number,
// also as a heading, in bold or behind a list mark. A plain bullet is no step.
var skillStepLine = regexp.MustCompile(`(?m)^[ \t]*(?:#{1,6}[ \t]+)?(?:[-*][ \t]+)?(?:\*\*|__)?(?:\d{1,2}[.)]|(?i:step)[ \t]*\d{1,2}\b|\d{1,2}[ \t]*단계|[①-⑳])`)

// draftUnusable: why nobody could follow a draft the dream is about to activate ("" when they can). It
// says when not to use the skill, and a new skill lists at least two numbered steps; a revision keeps
// the form of the file it replaces. Only the dream asks this: a person's own draft is theirs to shape.
func draftUnusable(a *SkillAnalysis, file string, revision bool) string {
	switch {
	case a == nil || len(a.AvoidWhen) == 0:
		return "skill_draft_no_avoid_when"
	case !revision && len(skillStepLine.FindAllString(file, 2)) < 2:
		return "skill_draft_no_steps"
	}
	return ""
}

// dreamRevisionCover is the share of a revision's new lines that must reuse the wording of the lessons
// or of the file it replaces, and the share of a line found again that makes it the same line (character
// pairs, as faithful counts them).
const dreamRevisionCover = 0.6

// skillNegation is a word that turns a step into a prohibition.
// shortcut: a word list for English and Korean, so a revision in another language is only checked for dropped and invented lines; add its words when skills are written in it.
var skillNegation = regexp.MustCompile(`(?i)\b(?:not|never|no|cannot|without|avoid)\b|n't\b|않|금지|말 ?것|마라|마세요|마십시오|말아|지 ?마|못하|없이|(?:^|\s)안\s`)

// revisionWorse: why a revised skill file is worse than the one it replaces ("" when it is not). A
// revision may reword a line, merge lines or add lines. It may not turn a prohibition into a step or a
// step into a prohibition, nor let a prohibition go; it may not drop more of the original's lines than
// it writes (a line whose wording is found again in the revision was merged, not dropped); and what it
// writes must come from the lessons or the original, so a lesson that says little cannot talk the model
// into steps nobody verified.
// shortcut: a lesson that only removes a step or lifts a prohibition is left to a person; allow it once lessons name the line they retire.
func revisionWorse(base, next string, lessons []string) string {
	old, cur := textLines(base), textLines(next)
	written := []string{}
	for _, line := range cur {
		if !slices.Contains(old, line) {
			written = append(written, line)
		}
	}
	all, dropped := digestGrams(strings.Join(written, "\n")), 0
	for _, line := range old {
		if slices.Contains(cur, line) {
			continue
		}
		twin, negated := rewordedAs(line, written), skillNegation.MatchString(line)
		if twin == "" && negated || twin != "" && negated != skillNegation.MatchString(twin) {
			return "skill_revision_flips_negation"
		}
		if twin == "" && digestCover(digestGrams(line), all) < dreamRevisionCover {
			dropped++
		}
	}
	if dropped > len(written) {
		return "skill_revision_drops_steps"
	}
	if len(written) > 0 && digestCover(all, digestGrams(strings.Join(lessons, "\n")+"\n"+base)) < dreamRevisionCover {
		return "skill_revision_unfaithful"
	}
	return ""
}

// rewordedAs is the written line that keeps most of line's wording, "" when none keeps enough of it.
func rewordedAs(line string, written []string) string {
	grams, twin, best := digestGrams(line), "", dreamRevisionCover
	for _, w := range written {
		if c := digestCover(grams, digestGrams(w)); c >= best {
			twin, best = w, c
		}
	}
	return twin
}

// textLines: the non-empty lines of text, trimmed.
func textLines(text string) []string {
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// groupLessons keeps the first not yet promoted lesson of each run (skill generation counts runs, not
// rows), at most 16, with their texts, the group's oldest lesson id and whether any came from content
// an agent captured from outside.
func (d *dreamer) groupLessons(ctx context.Context, refs []Ref) ([]Ref, []string, string, bool) {
	out, texts, runs, oldest, external := []Ref{}, []string{}, map[string]bool{}, Record{}, false
	for _, ref := range refs {
		r, err := d.s.Repo.Get(ctx, ref.Collection, d.scope, ref.ID)
		if err != nil {
			continue
		}
		if oldest.ID == "" || r.CreatedAt.Before(oldest.CreatedAt) || r.CreatedAt.Equal(oldest.CreatedAt) && r.ID < oldest.ID {
			oldest = r
		}
		run, _, ok := learningRelationRefs(r.Relations)
		if !ok || runs[run.ID] || len(out) >= 16 || d.promotedLesson(ctx, r.ID) {
			continue
		}
		runs[run.ID], external = true, external || d.capturedExternally(ctx, r)
		out, texts = append(out, ref), append(texts, r.Text)
	}
	return out, texts, oldest.ID, external
}

// capturedExternally: a source of the lesson is outside content (a page, a document, a tool's output)
// that an agent or service captured rather than a person supplied. A source it cannot read counts too.
func (d *dreamer) capturedExternally(ctx context.Context, lesson Record) bool {
	for _, ref := range lesson.Sources {
		src, err := d.s.Repo.Get(ctx, "sources", d.scope, ref.SourceID)
		if err != nil || src.CreatedByType != roleHuman && slices.Contains(externalSourceKinds, src.Kind) {
			return true
		}
	}
	return false
}

var externalSourceKinds = []string{"url", "document", "image", "design_page", "tool_result"}

// promotedLesson: a dream promoted this lesson before; it never counts toward another promotion, also
// after a person undid that one.
func (d *dreamer) promotedLesson(ctx context.Context, id string) bool {
	_, err := d.s.Repo.Get(ctx, colJobs, d.scope, promotedLessonID(d.scope, id))
	return err == nil
}

// activate makes the drafted version the skill's active one and retires the version it replaces, in
// one transaction that checks nothing moved since the draft.
func (d *dreamer) activate(ctx context.Context, made ImportSkillResult, beforeActive, target string, rev int64, g ReflectionView, lessons []Ref) error {
	v := made.Record
	rows := []dreamRow{{Collection: colSkills, ID: v.SkillID, Header: true, BeforeActive: beforeActive}, {Collection: colSkillVersions, ID: v.ID}}
	if beforeActive != "" {
		rows = append(rows, dreamRow{Collection: colSkillVersions, ID: beforeActive, BeforeStatus: statusActive})
	}
	reason := "lesson repeated in " + strconv.Itoa(g.DistinctRuns) + " runs, no trial configured"
	if beforeActive != "" {
		reason += ", replaces " + beforeActive
	}
	c := DreamChange{Op: "promote_skill", Target: colSkills + "/" + v.SkillID, Reason: reason, Confidence: g.Confidence, Before: beforeActive, After: v.ID}
	err := d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := d.activateInTx(ctx, tx, v, beforeActive); err != nil {
			return err
		}
		if err := markPromoted(ctx, tx, d.scope, lessons); err != nil {
			return err
		}
		return d.logInTx(ctx, tx, &c, rows)
	})
	if changed(err) {
		d.skip(target, rev, "changed", g.Confidence)
		return nil
	} else if err != nil {
		return err
	}
	d.logged(c)
	return nil
}

// activateInTx makes v the skill's active version if nothing moved since the draft.
func (d *dreamer) activateInTx(ctx context.Context, tx Tx, v Record, beforeActive string) error {
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
	return audit(ctx, tx, d.scope, d.principal, "dream:apply:skills", skill.ID)
}

// markPromoted records each lesson a promotion used. A lesson itself never changes, since the promoted
// version cites it at its exact revision. The marks are not undo rows: undo keeps them, so the same
// lessons never promote again.
func markPromoted(ctx context.Context, tx Tx, scope Scope, lessons []Ref) error {
	now := time.Now().UTC()
	for _, ref := range lessons {
		err := tx.Insert(ctx, colJobs, Record{ID: promotedLessonID(scope, ref.ID), Scope: scope, Kind: dreamPromotedLessonKind, Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now})
		if err != nil && !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return nil
}

const dreamPromotedLessonKind = "dream_promoted_lesson"

func promotedLessonID(scope Scope, lessonID string) string {
	return "dream-lesson-" + ContentHash(structuredText(scope) + "\n" + lessonID)[:40]
}
