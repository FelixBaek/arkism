package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Real loopback HTTP and distinct bearer credentials, with isolated in-memory
// storage/policy. Human principals below are fixtures, not a live human review.
func skillHTTPCall(t *testing.T, server *httptest.Server, token, path string, in, out any, want int) {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("path=%s status=%d elapsed_ms=%.3f", path, response.StatusCode, float64(time.Since(start).Microseconds())/1000)
	if response.StatusCode != want {
		t.Fatalf("status=%d want=%d body=%s", response.StatusCode, want, data)
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		t.Fatal("invalid response JSON")
	}
}

func TestCrossAgentSkillHTTPLifecycle(t *testing.T) {
	f := newFixture(t)
	const tokenB = "synthetic-reader-b"
	f.auth.actors[tokenB] = actor("reader-b", "agent", true, allVerbs...)
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	// Learning evidence is synthetic setup; the proposal itself uses agent A HTTP.
	in := proposalFixture(t, f)
	var proposed ImportSkillResult
	skillHTTPCall(t, server, f.agent, "/v1/skills/propose", in, &proposed, 200)
	resolve := func(scope Scope, ref string) ResolvedSkill {
		t.Helper()
		var result SkillResolveResult
		skillHTTPCall(t, server, tokenB, "/v1/skills/resolve", SkillResolveInput{Scope: scope, Refs: []string{ref}}, &result, 200)
		if len(result.Items) != 1 {
			t.Fatal("missing result")
		}
		return result.Items[0]
	}
	latest := in.Skill.SkillID + "@latest-approved"
	if got := resolve(scopeA, latest); got.Error != "no_approved_version" || got.Instructions != "" {
		t.Fatal("unapproved latest leaked")
	}
	// Explicit pins are deliberately review/replay access, including candidates.
	if got := resolve(scopeA, in.Skill.SkillID+"@1"); got.ValidationStatus != "candidate" || got.Instructions != in.Skill.Content {
		t.Fatal("candidate pin contract changed")
	}
	for _, token := range []string{f.agent, tokenB} {
		skillHTTPCall(t, server, token, "/v1/skill_versions/"+proposed.Record.ID+"/transition", ChangeInput{Scope: scopeA, ExpectedRevision: proposed.Record.Revision, Status: "approved"}, nil, 403)
	}
	passed := true
	target := Ref{Collection: "skill_versions", ID: proposed.Record.ID, Revision: proposed.Record.Revision}
	evaluation := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "synthetic independent comparison", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed, ComparisonReport: &SkillComparisonReport{PlanSHA256: proposed.ComparisonPlanSHA256, BaselineScore: .5, CandidateScore: .8, CandidateCost: 1, CandidateDurationMS: 500, RegressionPassed: true, SafetyPassed: true}}})
	var approved, parent Record
	skillHTTPCall(t, server, f.human, "/v1/skill_versions/"+proposed.Record.ID+"/transition", ChangeInput{Scope: scopeA, ExpectedRevision: proposed.Record.Revision, Status: "approved", ValidationID: evaluation.ID}, &approved, 200)
	skillHTTPCall(t, server, f.human, "/v1/skills/"+in.Skill.SkillID+"/apply", ChangeInput{Scope: scopeA, ExpectedRevision: proposed.Skill.Revision, VersionID: approved.ID}, &parent, 200)
	if got := resolve(scopeA, latest); got.Instructions != in.Skill.Content || got.Provenance == nil || got.Provenance.SourceID != proposed.Record.Sources[0].SourceID || !got.LatestApproved {
		t.Fatal("approved reuse/provenance missing")
	}
	for _, otherScope := range []Scope{scopeB, scopeFar, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other-user"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other-agent"}} {
		if got := resolve(otherScope, latest); got.Instructions != "" || got.Error != "skill_not_found" {
			t.Fatal("cross scope disclosure")
		}
	}
	skillHTTPCall(t, server, f.human, "/v1/skills/"+in.Skill.SkillID+"/availability", SkillAvailabilityInput{Scope: scopeA, ExpectedRevision: parent.Revision, Status: "deprecated"}, nil, 200)
	if got := resolve(scopeA, latest); got.Instructions != "" || got.Error != "skill_deprecated" {
		t.Fatal("deprecated latest reused")
	}
	if got := resolve(scopeA, in.Skill.SkillID+"@1"); !got.Deprecated || got.Instructions == "" {
		t.Fatal("explicit deprecated replay contract changed")
	}
}

func TestSkillResolveHTTPHonorsResourceReadDenial(t *testing.T) {
	for _, collection := range []string{"skills", "skill_versions", "sources"} {
		t.Run(collection, func(t *testing.T) {
			f := newFixture(t)
			imported, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "isolated-review", Path: "/synthetic/SKILL.md", Content: "# Synthetic private procedure\nRun focused checks.", Analysis: detailedSkillAnalysis()})
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]string{"skills": imported.Skill.ID, "skill_versions": imported.Record.ID, "sources": imported.Record.Sources[0].SourceID}
			f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
				if token == f.agent && req.Action == "read:"+collection && req.ResourceID == ids[collection] {
					return Decision{}, ErrDenied
				}
				return f.auth.Authorize(ctx, token, req)
			})
			if _, err := f.svc.Get(testCtx, f.agent, collection, scopeA, ids[collection]); err != ErrDenied {
				t.Fatalf("direct read not denied: %v", err)
			}
			server := httptest.NewServer(Handler(f.svc))
			defer server.Close()
			skillHTTPCall(t, server, f.agent, "/v1/skills/resolve", SkillResolveInput{Scope: scopeA, Refs: []string{"isolated-review@latest-approved"}}, nil, 403)
		})
	}
}

func TestSkillResolveRejectsChangedReaderIdentity(t *testing.T) {
	for _, change := range []string{"principal", "role"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			imported, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "identity-review", Path: "/synthetic/SKILL.md", Content: "# Synthetic procedure", Analysis: detailedSkillAnalysis()})
			if err != nil {
				t.Fatal(err)
			}
			f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
				d, err := f.auth.Authorize(ctx, token, req)
				if token == f.agent && req.Action == "read:skill_versions" && req.ResourceID == imported.Record.ID {
					if change == "principal" {
						d.PrincipalID = "other-reader"
					} else {
						d.Role = "human"
					}
				}
				return d, err
			})
			server := httptest.NewServer(Handler(f.svc))
			defer server.Close()
			skillHTTPCall(t, server, f.agent, "/v1/skills/resolve", SkillResolveInput{Scope: scopeA, Refs: []string{"identity-review@latest-approved"}}, nil, 403)
		})
	}
}

// Policy evaluation runs outside the read transaction, so a policy callback
// can revoke a source. The publication fence must then discard the snapshot.
func TestSkillResolveRechecksAfterResourceAuthorization(t *testing.T) {
	f := newFixture(t)
	imported, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "revoked-during-policy", Path: "/synthetic/SKILL.md", Content: "# Synthetic procedure", Analysis: detailedSkillAnalysis()})
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if token == f.agent && req.Action == "read:sources" && req.ResourceID == imported.Record.Sources[0].SourceID && !changed {
			changed = true
			source, e := f.repo.Get(ctx, "sources", scopeA, req.ResourceID)
			if e != nil {
				t.Fatal(e)
			}
			before := source.Revision
			source.Revision++
			source.Status = "expired"
			if e = f.repo.Replace(ctx, "sources", source, before); e != nil {
				t.Fatal(e)
			}
		}
		return f.auth.Authorize(ctx, token, req)
	})
	got, err := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{"revoked-during-policy@latest-approved"}})
	if err != ErrConflict || len(got.Items) != 0 {
		t.Fatalf("revoked content returned: %v %#v", err, got)
	}
}

func TestPostgresSkillResolveWithSingleConnection(t *testing.T) {
	pg := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = pg
	imported, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "single-connection", Path: "/synthetic/SKILL.md", Content: "# Synthetic procedure", Analysis: detailedSkillAnalysis()})
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "single-reader", Role: "agent", Token: f.agent, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	app := restrictedPostgres(t, pg)
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Repo, f.svc.Auth = app, auth
	ctx, cancel := context.WithTimeout(ContextWithPostgresToken(testCtx, f.agent), 5*time.Second)
	defer cancel()
	got, err := f.svc.ResolveSkills(ctx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{"single-connection@latest-approved"}})
	if err != nil || len(got.Items) != 1 || got.Items[0].VersionID != imported.Record.ID {
		t.Fatalf("single connection resolution: %v %#v", err, got)
	}
}

// Error codes and trial status are stored information too. A denied parent
// must look identical whether missing, deprecated, unapproved or unavailable.
func TestSkillResolveErrorItemsHonorParentReadDenial(t *testing.T) {
	for _, state := range []string{"missing", "deprecated", "unapproved", "missing-version", "unavailable", "trial"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			imported, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "private-status", Path: "/synthetic/SKILL.md", Content: "# Private procedure", Analysis: detailedSkillAnalysis()})
			if err != nil {
				t.Fatal(err)
			}
			parent := imported.Skill
			in := SkillResolveInput{Scope: scopeA, Refs: []string{parent.ID + "@latest-approved"}}
			want := ""
			switch state {
			case "missing":
				in.Refs = []string{"private-missing@latest-approved"}
				want = "skill_not_found"
			case "deprecated":
				parent.Status = "deprecated"
				want = "skill_deprecated"
			case "unapproved", "trial":
				parent.ActiveVersion = ""
				want = "no_approved_version"
			case "missing-version":
				in.Refs = []string{parent.ID + "@99"}
				want = "version_not_found"
			case "unavailable":
				version := imported.Record
				version.Analysis = nil
				if err = f.repo.Replace(testCtx, colSkillVersions, version, version.Revision); err != nil {
					t.Fatal(err)
				}
				want = "skill_unavailable"
			}
			if err = f.repo.Replace(testCtx, colSkills, parent, parent.Revision); err != nil {
				t.Fatal(err)
			}
			if state == "trial" {
				in.TrialID, in.Runtime = "private-trial", "codex"
				trial := Record{ID: in.TrialID, Scope: scopeA, SkillID: parent.ID, Status: "expired", Revision: 1, SkillTrial: &SkillTrial{Runtime: in.Runtime, EndsAt: time.Now().Add(-time.Hour)}}
				if err = f.repo.Insert(testCtx, colSkillTrials, trial); err != nil {
					t.Fatal(err)
				}
			}
			visible, err := f.svc.ResolveSkills(testCtx, f.agent, in)
			if err != nil || len(visible.Items) != 1 || visible.Items[0].Error != want {
				t.Fatal("fixture did not reach intended error state", visible, err)
			}
			if state == "trial" && visible.Items[0].TrialStatus != "expired" {
				t.Fatal("fixture did not expose trial status")
			}
			id, _, _ := parseSkillRef(in.Refs[0])
			f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
				if token == f.agent && req.Action == "read:skills" && req.ResourceID == id {
					return Decision{}, ErrDenied
				}
				return f.auth.Authorize(ctx, token, req)
			})
			got, err := f.svc.ResolveSkills(testCtx, f.agent, in)
			if err != ErrDenied || len(got.Items) != 0 {
				t.Fatal("private error/status disclosed", got, err)
			}
			server := httptest.NewServer(Handler(f.svc))
			defer server.Close()
			skillHTTPCall(t, server, f.agent, "/v1/skills/resolve", in, nil, http.StatusForbidden)
		})
	}
}
