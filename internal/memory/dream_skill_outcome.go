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

// skillNotHelping: the version's uses say it does not help. Either at least skillWatchOutcomeMinFailures
// uses failed and those are at least twice the uses that went well, or most of its uses failed while
// the version it replaced, with a few uses of its own, failed less often.
func skillNotHelping(uses, failed, baseUses, baseFailed int) bool {
	helped := uses - failed
	return watchWorse(failed, helped, false) ||
		failed >= skillWatchMinFailures && failed > helped && baseUses >= skillWatchMinFailures && failed*baseUses > baseFailed*uses
}

type skillOutcomeReader interface {
	SkillVersionOutcomes(context.Context, Scope, string) (int, int, error)
}

// demoteSkills takes back the dream-promoted active versions whose uses say they do not help. It asks
// no model, so it also runs when none is configured.
func (d *dreamer) demoteSkills(ctx context.Context) error {
	reader, ok := d.s.Repo.(skillOutcomeReader)
	if !ok {
		return nil // only Postgres keeps use events
	}
	skills, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, colSkills, statusActive, dreamScanMax)
	if err != nil {
		return err
	}
	granted := false
	for _, skill := range skills {
		v, err := d.s.Repo.Get(ctx, colSkillVersions, d.scope, skill.ActiveVersion)
		if skill.ActiveVersion == "" || err != nil || v.DreamPromoted == "" || v.Status != statusActive {
			continue
		}
		reason, confidence, err := d.notHelping(ctx, reader, v)
		if err != nil {
			return err
		}
		if reason == "" || d.demoted(ctx, v.ID) {
			continue
		}
		if d.stop() || !granted && !d.allowed(ctx, "skills", "read:skills", "apply:skills") {
			return nil
		}
		granted = true
		if err = d.demote(ctx, skill, v, reason, confidence); err != nil {
			return err
		}
	}
	return nil
}

// notHelping reads the version's recorded uses and those of the version it replaced; the reason is
// empty while they do not say the version fails.
func (d *dreamer) notHelping(ctx context.Context, reader skillOutcomeReader, v Record) (string, float64, error) {
	uses, failed, err := reader.SkillVersionOutcomes(ctx, d.scope, v.ID)
	if err != nil {
		return "", 0, err
	}
	baseUses, baseFailed := 0, 0
	if v.BaseVersion != "" {
		if baseUses, baseFailed, err = reader.SkillVersionOutcomes(ctx, d.scope, v.BaseVersion); err != nil {
			return "", 0, err
		}
	}
	if !skillNotHelping(uses, failed, baseUses, baseFailed) {
		return "", 0, nil
	}
	reason := "failed " + strconv.Itoa(failed) + " of " + strconv.Itoa(uses) + " uses"
	if baseUses > 0 {
		reason += ", the version before failed " + strconv.Itoa(baseFailed) + " of " + strconv.Itoa(baseUses)
	}
	return reason, float64(failed) / float64(uses), nil
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
		if c.After, err = d.reactivateBase(ctx, tx, v.BaseVersion, now); err != nil {
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

// reactivateBase makes the replaced version active again and returns its id; "" when there is none
// or it is no longer as the promotion left it.
func (d *dreamer) reactivateBase(ctx context.Context, tx Tx, id string, now time.Time) (string, error) {
	base, err := tx.Get(ctx, colSkillVersions, d.scope, id)
	if id == "" || err != nil || base.Status != "superseded" || base.ContentErased || base.PrivacyQuarantined {
		return "", nil //nolint:nilerr // a base that is gone or unreadable means the skill is retired instead
	}
	prev := base.Revision
	base.Status, base.Revision, base.UpdatedAt = statusActive, prev+1, now
	if err = tx.Replace(ctx, colSkillVersions, base, prev); err != nil {
		return "", err
	}
	return base.ID, enqueue(ctx, tx, colSkillVersions, base)
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
