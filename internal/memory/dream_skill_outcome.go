package memory

// Skill efficacy: a skill version the dream made active is judged by how its recorded uses ended. One
// whose uses mostly fail, or fail clearly more often than the version it replaced, is taken back: the
// skill returns to its earlier version, or has no active version when the dream made the skill. Undo
// puts the demoted version back, and a version is demoted at most once.

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// skillNotHelping: the runs that used the version say it does not help. Either at least
// skillWatchOutcomeMinFailures runs failed and those are at least twice the runs that went well, or most
// of its runs failed while the version it replaced, with a few runs of its own, failed less often.
// Runs are counted, not use reports, so one run that reports many failures never retires a skill.
func skillNotHelping(uses, failed, baseUses, baseFailed int) bool {
	helped := uses - failed
	return watchWorse(failed, helped, false) ||
		failed >= skillWatchMinFailures && failed > helped && baseUses >= skillWatchMinFailures && failed*baseUses > baseFailed*uses
}

type skillOutcomeReader interface {
	SkillRunOutcomes(context.Context, Scope, map[string]time.Time) (map[string][2]int, error)
}

// demoteSkills takes back the dream-promoted active versions whose recent runs say they do not help:
// the version's runs of the last skillWatchPeriod, against the runs of the version it replaced from
// skillWatchPeriod before the replacement on. One read for all skills. It asks no model, so it also
// runs when none is configured.
func (d *dreamer) demoteSkills(ctx context.Context) error {
	reader, ok := d.s.Repo.(skillOutcomeReader)
	if !ok {
		return nil // only Postgres keeps use events
	}
	skills, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, colSkills, statusActive, dreamScanMax)
	if err != nil {
		return err
	}
	versions, since := d.judgedVersions(ctx, skills)
	if len(versions) == 0 {
		return nil
	}
	runs, err := reader.SkillRunOutcomes(ctx, d.scope, since)
	if err != nil {
		return err
	}
	granted := false
	for _, skill := range skills {
		v, ok := versions[skill.ID]
		n, base := runs[v.ID], runs[v.BaseVersion]
		if !ok || !skillNotHelping(n[0], n[1], base[0], base[1]) || d.demoted(ctx, v.ID) {
			continue
		}
		if d.stop() || !granted && !d.allowed(ctx, "skills", "read:skills", "apply:skills") {
			return nil
		}
		granted = true
		reason := "failed in " + strconv.Itoa(n[1]) + " of " + strconv.Itoa(n[0]) + " runs"
		if base[0] > 0 {
			reason += ", the version before in " + strconv.Itoa(base[1]) + " of " + strconv.Itoa(base[0])
		}
		if err = d.demote(ctx, skill, v, reason, float64(n[1])/float64(n[0])); err != nil {
			return err
		}
	}
	return nil
}

// judgedVersions: the dream-promoted active version of each skill that has one, by skill id, and from
// when the runs of it and of the version it replaced count.
func (d *dreamer) judgedVersions(ctx context.Context, skills []Record) (map[string]Record, map[string]time.Time) {
	versions, since, recent := map[string]Record{}, map[string]time.Time{}, time.Now().Add(-skillWatchPeriod)
	for _, skill := range skills {
		v, err := d.s.Repo.Get(ctx, colSkillVersions, d.scope, skill.ActiveVersion)
		if skill.ActiveVersion == "" || err != nil || v.DreamPromoted == "" || v.Status != statusActive {
			continue
		}
		versions[skill.ID], since[v.ID] = v, recent
		if v.BaseVersion != "" {
			since[v.BaseVersion] = v.CreatedAt.Add(-skillWatchPeriod)
		}
	}
	return versions, since
}

// demote rejects the version and makes the version it replaced active again when that one is still
// there as the promotion left it; otherwise the skill is retired (no active version). One transaction
// that checks nothing moved since the outcomes were read.
func (d *dreamer) demote(ctx context.Context, skill, v Record, reason string, confidence float64) error {
	c := DreamChange{Op: "demote_skill", Target: colSkills + "/" + skill.ID, Reason: reason, Confidence: confidence, Before: v.ID}
	err := d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		parent, cur, err := d.unmoved(ctx, tx, skill, v)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		rows := []dreamRow{{Collection: colSkills, ID: skill.ID, Header: true, BeforeActive: v.ID}, {Collection: colSkillVersions, ID: v.ID, BeforeStatus: statusActive}}
		if c.After, err = d.reactivateBase(ctx, tx, parent, v.BaseVersion, now); err != nil {
			return err
		}
		if c.After != "" {
			rows = append(rows, dreamRow{Collection: colSkillVersions, ID: c.After, BeforeStatus: "superseded"})
		}
		cur.Status, cur.Revision, cur.UpdatedAt = "rejected", v.Revision+1, now
		if err = tx.Replace(ctx, colSkillVersions, cur, v.Revision); err != nil {
			return err
		}
		parent.ActiveVersion, parent.Revision, parent.UpdatedAt = c.After, skill.Revision+1, now
		if err = tx.Replace(ctx, colSkills, parent, skill.Revision); err != nil {
			return err
		}
		// Not an undo row: undo keeps the mark, so a version a person put back is not demoted again.
		if err = tx.Insert(ctx, colJobs, Record{ID: demotedVersionID(d.scope, v.ID), Scope: d.scope, Kind: dreamDemotedVersionKind, Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if err = audit(ctx, tx, d.scope, d.principal, "dream:demote:skills", skill.ID); err != nil {
			return err
		}
		return d.logInTx(ctx, tx, &c, rows)
	})
	if changed(err) {
		d.skip(c.Target, v.Revision, "changed", confidence)
		return nil
	} else if err != nil {
		return err
	}
	d.logged(c)
	d.demotedNow = append(d.demotedNow, skill.ID)
	return nil
}

// unmoved reads the skill and its version again and refuses when either changed since it was judged.
func (d *dreamer) unmoved(ctx context.Context, tx Tx, skill, v Record) (Record, Record, error) {
	parent, err := tx.Get(ctx, colSkills, d.scope, skill.ID)
	if err != nil {
		return Record{}, Record{}, err
	}
	cur, err := tx.Get(ctx, colSkillVersions, d.scope, v.ID)
	if err == nil && (parent.Revision != skill.Revision || cur.Revision != v.Revision) {
		err = ErrConflict
	}
	return parent, cur, err
}

// reactivateBase makes the replaced version active again and returns its id; "" when there is none or
// a person could not roll back to it either (checkSkillVersion: gone, changed hands, expired, its
// sources or relations no longer stand), so the skill is retired instead.
func (d *dreamer) reactivateBase(ctx context.Context, tx Tx, parent Record, id string, now time.Time) (string, error) {
	if id == "" {
		return "", nil
	}
	target, err := tx.Get(ctx, colSkillVersions, d.scope, id)
	if err == nil {
		target, err = checkSkillVersion(ctx, tx, ChangeInput{Scope: d.scope, VersionID: id}, parent.ID, parent, target, true)
	}
	if changed(err) || err == nil && (target.ContentErased || target.PrivacyQuarantined) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	prev := target.Revision
	target.Status, target.Revision, target.UpdatedAt = statusActive, prev+1, now
	if err = tx.Replace(ctx, colSkillVersions, target, prev); err != nil {
		return "", err
	}
	return target.ID, enqueue(ctx, tx, colSkillVersions, target)
}

const dreamDemotedVersionKind = "dream_demoted_version"

func demotedVersionID(scope Scope, versionID string) string {
	return "dream-demoted-" + ContentHash(structuredText(scope) + "\n" + versionID)[:40]
}

// demoted: a dream took this version back before.
func (d *dreamer) demoted(ctx context.Context, versionID string) bool {
	_, err := d.s.Repo.Get(ctx, colJobs, d.scope, demotedVersionID(d.scope, versionID))
	return !errors.Is(err, ErrNotFound)
}
