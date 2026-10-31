package memory

// CAP-03/04/05 replay against the real server paths. The isolated capability lab
// stubbed four server paths (ResolveSkills/resolveSkillRef, checkSkillUsageEvents,
// resolveSkillTrial, and the server half of the crew trial checker) behind a
// loopback fake hub. These tests call the real functions instead, on the fake and
// the disposable PostgreSQL repository, and compare with the lab's recorded
// expectations. No product behaviour is changed by this file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func forCapabilityRepos(t *testing.T, run func(t *testing.T, f *fixture)) {
	t.Run("fake", func(t *testing.T) { run(t, newFixture(t)) })
	t.Run("postgres", func(t *testing.T) {
		f := newFixture(t)
		f.svc.Repo = isolatedPostgres(t)
		run(t, f)
	})
}

// capabilitySkill imports v1 (active) and returns a helper that approves and
// applies a later content as the new latest-approved version.
func capabilitySkill(t *testing.T, f *fixture, id string) (ImportSkillResult, func(content string) ImportSkillResult) {
	t.Helper()
	in := ImportSkillInput{Scope: scopeA, SkillID: id, Path: "/skills/" + id + "/SKILL.md", Content: "---\nname: " + id + "\n---\nbody v1", Analysis: detailedSkillAnalysis()}
	v1, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil || v1.Version != 1 {
		t.Fatal("import v1", err)
	}
	publish := func(content string) ImportSkillResult {
		t.Helper()
		in.Content = content
		next, err := f.svc.ImportSkill(testCtx, f.human, in)
		if err != nil {
			t.Fatal("import next", err)
		}
		ev := f.create(f.human2, "evaluations", ratingInput(CreateInput{Scope: scopeA, Text: "independent check passed", Passed: true, Sources: refs(f.source()),
			Target: &Ref{Collection: colSkillVersions, ID: next.Record.ID, Revision: next.Record.Revision}}))
		f.transition(f.human, colSkillVersions, next.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: next.Record.Revision, Status: "approved", ValidationID: ev.ID})
		parent := f.get(f.human, colSkills, scopeA, id)
		f.apply(f.human, id, ChangeInput{Scope: scopeA, ExpectedRevision: parent.Revision, VersionID: next.Record.ID}, false)
		return next
	}
	return v1, publish
}

func resolveOne(t *testing.T, svc *Service, token, ref string) (ResolvedSkill, error) {
	t.Helper()
	out, err := svc.ResolveSkills(testCtx, token, SkillResolveInput{Scope: scopeA, Refs: []string{ref}})
	if err != nil {
		return ResolvedSkill{}, err
	}
	return out.Items[0], nil
}

func recordUsage(svc *Service, item ResolvedSkill, status string) error {
	now := time.Now().UTC()
	raw, _ := json.Marshal(SkillUsage{SkillID: item.SkillID, Version: item.Version, VersionID: item.VersionID, ContentHash: item.ContentHash, Runtime: "codex", Status: status, StartedAt: now, CompletedAt: now})
	return svc.Repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return checkSkillUsageEvents(ctx, tx, scopeA, []TraceEventInput{{Kind: skillUsageEventKind, Payload: raw}}, Scope{})
	})
}

// CAP-03 stub paths: the fake hub answered every request; the real server refuses.
func TestCapReplayServerPaths(t *testing.T) {
	forCapabilityRepos(t, func(t *testing.T, f *fixture) {
		pinned, latest := capReplayResolveAndPin(t, f)
		capReplayUsage(t, f, pinned, latest)
		capReplayTrialResolution(t, f)
	})
}

// server_resolve_authorization, then pin_save_ok / pin_save_failure (server half).
func capReplayResolveAndPin(t *testing.T, f *fixture) (pinned, latest ResolvedSkill) {
	t.Helper()
	v1, publish := capabilitySkill(t, f, "cap03-demo")

	// server_resolve_authorization: typed per-ref answers and a scope denial.
	out, err := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{"cap03-demo@latest-approved", "cap03-demo@9", "missing@1", "bad-ref"}})
	if err != nil || out.Items[0].Version != 1 || out.Items[1].Error != "version_not_found" || out.Items[2].Error != "skill_not_found" || out.Items[3].Error != "invalid_ref" {
		t.Fatalf("resolve: %v %+v", err, out.Items)
	}
	f.auth.actors["tok-cap-other-scope"] = fakeActor{principal: "reader", role: "agent", external: true, wrongScope: true, verbs: actor("", "", true, allVerbs...).verbs}
	if _, err = resolveOne(t, f.svc, "tok-cap-other-scope", "cap03-demo@latest-approved"); !errors.Is(err, ErrDenied) {
		t.Fatal("other-scope reader resolved", err)
	}

	// After v2 is approved the exact pin still serves v1 and latest-approved moves to v2.
	served := out.Items[0]
	v2 := publish("---\nname: cap03-demo\n---\nbody v2")
	pinned, err = resolveOne(t, f.svc, f.agent, "cap03-demo@1")
	if err != nil || pinned.VersionID != v1.Record.ID || pinned.ContentHash != served.ContentHash || pinned.ValidationStatus != "superseded" {
		t.Fatalf("pinned replay: %v %+v", err, pinned)
	}
	latest, err = resolveOne(t, f.svc, f.agent, "cap03-demo@latest-approved")
	if err != nil || latest.VersionID != v2.Record.ID || latest.Version != 2 || !latest.LatestApproved {
		t.Fatalf("latest after approval: %v %+v", err, latest)
	}
	return pinned, latest
}

// server_usage_acceptance: the fake trace endpoint accepted any body; the
// server binds usage to a served version and hash. Status stays caller-asserted.
func capReplayUsage(t *testing.T, f *fixture, pinned, latest ResolvedSkill) {
	t.Helper()
	if err := recordUsage(f.svc, pinned, "success"); err != nil {
		t.Fatal("served pin usage refused", err)
	}
	if err := recordUsage(f.svc, latest, "failure"); err != nil {
		t.Fatal("served latest usage refused", err)
	}
	forged := pinned
	forged.ContentHash = latest.ContentHash
	if err := recordUsage(f.svc, forged, "success"); !errors.Is(err, ErrConflict) {
		t.Fatal("usage for an unserved hash accepted", err)
	}
	forged = latest
	forged.Version = 3
	if err := recordUsage(f.svc, forged, "success"); !errors.Is(err, ErrConflict) {
		t.Fatal("usage for an unknown version accepted", err)
	}
}

// server_trial_resolution: the fake echoed any trial_id as active; the
// server reads the trial record and refuses an unknown one.
func capReplayTrialResolution(t *testing.T, f *fixture) {
	t.Helper()
	trial := SkillResolveInput{Scope: scopeA, Refs: []string{"cap03-demo@latest-approved"}, TrialID: "trial-1", Runtime: "codex"}
	if got, err := f.svc.ResolveSkills(testCtx, f.agent, trial); err == nil || len(got.Items) != 0 {
		t.Fatalf("unknown trial served: %v %+v", err, got.Items)
	}
	trial.Runtime = ""
	if _, err := f.svc.ResolveSkills(testCtx, f.agent, trial); !errors.Is(err, ErrInvalid) {
		t.Fatal("trial without runtime", err)
	}
}

// CAP-03 independent_trial_checker (server half of crew processTrial): the
// manager observes, only a separate checker credential may record outcomes.
// The host half (processTrial itself) stays covered by internal/host/crew tests.
func TestCapReplayTrialChecker(t *testing.T) {
	f, candidate, trial, in := skillAutomationFixture(t)
	f.auth.actors["checker"] = actor("independent-trial-checker", "service", true, allVerbs...)
	manager := testPrincipalToken("learner")
	observe := func() string {
		t.Helper()
		out, err := f.svc.ObserveSkillTrial(testCtx, manager, trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
		if err != nil {
			t.Fatal(err)
		}
		return out.State
	}
	if state := observe(); state != "awaiting_independent_evaluation" {
		t.Fatal("processTrial only rates uses in this state", state)
	}
	for i, source := range in.TrialEvidence {
		input := SkillTrialOutcomeInput{Scope: scopeA, TrialScope: scopeA, TrialID: trial.ID, EventID: "cap03-" + source.ID, Source: source, Evidence: []Ref{source}, CheckerSHA256: ContentHash("pinned checker"), Passed: true}
		if _, err := f.svc.RecordSkillTrialOutcome(testCtx, manager, input); !errors.Is(err, ErrDenied) {
			t.Fatal("manager credential rated its own trial", i, err)
		}
		if _, err := f.svc.RecordSkillTrialOutcome(testCtx, "checker", input); err != nil {
			t.Fatal(err)
		}
	}
	if state := observe(); state != "ready_for_comparison" {
		t.Fatal("after independent outcomes", state)
	}
}

// CAP-04 adapters, reduced to what each one reads from the hub. They are the lab
// models (capability-lab/cap_04/adapters.py), fed with real server answers.
// selective_revalidation is absent: it needs meaning/schema premises the server
// does not return (see the field check below).
type capabilityDecision struct{ action, status string }

func capabilityDecide(adapter string, checkpoint ResolvedSkill, latest func() (ResolvedSkill, error), pin func() (ResolvedSkill, error)) capabilityDecision {
	switch adapter {
	case "current": // pin saved: the session keeps its exact ref; offline uses the cached pin.
		if r, err := pin(); err == nil && r.VersionID != checkpoint.VersionID {
			return capabilityDecision{"unexpected_pin_swap", "decided"}
		}
		return capabilityDecision{"use_before", "decided"}
	case "current_pin_save_failed": // follows the hub; offline falls back to the stale cache.
		if _, err := latest(); err != nil {
			return capabilityDecision{"use_before", "decided"}
		}
		return capabilityDecision{"use_current", "decided"}
	case "always_fresh":
		if _, err := latest(); err != nil {
			return capabilityDecision{"stop", "refused"}
		}
		return capabilityDecision{"use_current", "decided"}
	case "pinned_available": // never contacts the hub.
		return capabilityDecision{"use_before", "decided"}
	case "all_change_stop":
		r, err := latest()
		if err != nil {
			return capabilityDecision{"none", "unknown"}
		}
		if r.VersionID == checkpoint.VersionID && r.ContentHash == checkpoint.ContentHash {
			return capabilityDecision{"use_current", "decided"}
		}
		return capabilityDecision{"stop", "refused"}
	}
	return capabilityDecision{}
}

type unreachableRepo struct{ Repository }

func (unreachableRepo) Transaction(context.Context, func(context.Context, Tx) error) error {
	return errors.New("hub unreachable")
}

// CAP-04 comparison and CAP-05 no-change report on real server answers.
// Expected actions are capability-lab/cap_04/out/a/results.jsonl, uniform per
// condition for these five adapters (12 inputs each).
func TestCapReplayComparisonAndNoChangeReport(t *testing.T) {
	want := map[string]map[string]capabilityDecision{
		"current":                 {"unchanged": {"use_before", "decided"}, "compatible": {"use_before", "decided"}, "incompatible": {"use_before", "decided"}, "unknown": {"use_before", "decided"}},
		"current_pin_save_failed": {"unchanged": {"use_current", "decided"}, "compatible": {"use_current", "decided"}, "incompatible": {"use_current", "decided"}, "unknown": {"use_before", "decided"}},
		"always_fresh":            {"unchanged": {"use_current", "decided"}, "compatible": {"use_current", "decided"}, "incompatible": {"use_current", "decided"}, "unknown": {"stop", "refused"}},
		"pinned_available":        {"unchanged": {"use_before", "decided"}, "compatible": {"use_before", "decided"}, "incompatible": {"use_before", "decided"}, "unknown": {"use_before", "decided"}},
		"all_change_stop":         {"unchanged": {"use_current", "decided"}, "compatible": {"stop", "refused"}, "incompatible": {"stop", "refused"}, "unknown": {"none", "unknown"}},
	}
	// The lab's compatible inputs keep the declared meaning; incompatible ones change it.
	next := map[string]string{
		"compatible":   "---\nname: %s\n---\nbody v1\noptional note: may be empty",
		"incompatible": "---\nname: %s\n---\nbody v2: the input path now names an output path",
	}
	forCapabilityRepos(t, func(t *testing.T, f *fixture) {
		served := map[string]map[string]ResolvedSkill{}
		for _, condition := range []string{"unchanged", "compatible", "incompatible", "unknown"} {
			served[condition] = capReplayCondition(t, f, condition, next[condition], want)
		}

		// CAP-05 verdict -> effect: after a pin save failure the incompatible view is
		// served and its usage is accepted, the same as the pinned old view. The
		// server performs no compatibility check (residual: 12/48 effects in the lab).
		for _, view := range []string{"before", "current"} {
			if err := recordUsage(f.svc, served["incompatible"][view], "success"); err != nil {
				t.Fatal("effect on incompatible input refused", view, err)
			}
		}
		// No-change report: the server answer carries no meaning or schema premise,
		// (provenance carries only origin identity), so compatible and incompatible
		// changes are indistinguishable to it and
		// selective revalidation (or the CAP-05 shadow) cannot be fed from it.
		if a, b := capReplayShape(served["compatible"]["current"]), capReplayShape(served["incompatible"]["current"]); a != b {
			t.Fatalf("server distinguishes compatible from incompatible: %s vs %s", a, b)
		}
	})
}

// capReplayCondition replays every adapter on one condition and returns the
// pinned ("before") and latest ("current") views; unknown returns none.
func capReplayCondition(t *testing.T, f *fixture, condition, nextBody string, want map[string]map[string]capabilityDecision) map[string]ResolvedSkill {
	t.Helper()
	id := "cap-" + condition
	_, publish := capabilitySkill(t, f, id)
	// Checkpoint: the view loaded before the change (input -> checkpoint).
	checkpoint, err := resolveOne(t, f.svc, f.agent, id+"@latest-approved")
	if err != nil || checkpoint.Error != "" {
		t.Fatal("checkpoint", err, checkpoint.Error)
	}
	if nextBody != "" {
		publish(fmt.Sprintf(nextBody, id))
	}
	svc := f.svc
	if condition == "unknown" {
		svc = &Service{Repo: unreachableRepo{f.svc.Repo}, Auth: f.svc.Auth}
	}
	latest := func() (ResolvedSkill, error) { return resolveOne(t, svc, f.agent, id+"@latest-approved") }
	pin := func() (ResolvedSkill, error) { return resolveOne(t, svc, f.agent, id+"@1") }
	for adapter, row := range want {
		if got := capabilityDecide(adapter, checkpoint, latest, pin); got != row[condition] {
			t.Errorf("%s/%s: server replay %v, lab %v", adapter, condition, got, row[condition])
		}
	}
	if condition == "unknown" {
		return map[string]ResolvedSkill{}
	}
	before, err := pin()
	if err != nil || before.VersionID != checkpoint.VersionID {
		t.Fatal("pinned view", err)
	}
	current, err := latest()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]ResolvedSkill{"before": before, "current": current}
}

// capReplayShape drops the identity fields so only what the server says about
// the change itself is compared.
func capReplayShape(r ResolvedSkill) string {
	raw, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"ref", "skill_id", "version_id", "content_hash", "instructions", "name", "resolved_version", "provenance_summary"} {
		delete(m, k)
	}
	out, _ := json.Marshal(m)
	return string(out)
}
