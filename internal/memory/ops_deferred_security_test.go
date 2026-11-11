package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func legacyDeferred(t *testing.T, f *fixture, id string) Record {
	t.Helper()
	return f.put(colJobs, Record{ID: id, Kind: deferredCallKind, Status: JobStatusDone, CreatedBy: "human-1", CreatedByType: roleHuman, Deferred: &DeferredCall{Route: "POST /v1/skills/analyze", Result: json.RawMessage(`{"record":{"text":"synthetic-cached-response-canary"}}`)}})
}
func TestDeferredLegacyResponseIsCreatorOnly(t *testing.T) {
	f := newOpsFixture(t)
	row := legacyDeferred(t, f, "legacy-owner")
	for _, role := range []string{roleHuman, roleService, roleAgent} {
		token := testPrincipalToken("other-deferred-" + role)
		f.auth.actors[token] = actor("other-"+role, role, true, allVerbs...)
		f.auth.actors[token].verbs["ops"] = true
		if _, err := f.svc.GetOperation(testCtx, token, scopeA, row.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s read another creator's call: %v", role, err)
		}
		page, err := f.svc.ListOperations(testCtx, token, OpsListInput{Scope: scopeA})
		if err != nil || len(page.Items) != 0 {
			t.Errorf("%s listed another creator's call: %v %+v", role, err, page)
		}
	}
}
func TestDeferredLegacyBodyIsUnavailableAfterAuthorityOrSourceChanges(t *testing.T) {
	for _, reason := range []string{"revoked-original-action", "erased-source", "still-valid"} {
		t.Run(reason, func(t *testing.T) {
			f := newOpsFixture(t)
			row := legacyDeferred(t, f, "legacy-"+reason)
			if reason == "revoked-original-action" {
				f.auth.actors[f.human].verbs["!analyze:skills"] = true
			}
			if reason == "erased-source" {
				f.put("sources", Record{ID: "erased", Kind: "message", Status: statusActive, ContentErased: true})
			}
			for range 2 {
				out, err := f.svc.GetOperation(testCtx, f.human, scopeA, row.ID)
				if err != nil || out.Status != JobStatusFailed || out.Error != "result_unavailable" || len(out.Result) != 0 {
					t.Fatalf("stale cached response returned: %v %+v", err, out)
				}
			}
			raw, err := f.svc.Repo.Get(testCtx, colJobs, scopeA, row.ID)
			if err != nil || strings.Contains(structuredText(raw), "synthetic-cached-response-canary") {
				t.Fatal("legacy response remains in live row", err)
			}
		})
	}
}
func TestDeferredLegacyRunningPollEnds(t *testing.T) {
	f := newOpsFixture(t)
	row := f.put(colJobs, Record{ID: "legacy-running", Kind: deferredCallKind, Status: JobStatusRunning, CreatedBy: "human-1", CreatedByType: roleHuman, LeaseUntil: time.Now().Add(time.Hour), Deferred: &DeferredCall{Route: "POST /v1/skills/generate"}})
	out, err := f.svc.GetOperation(testCtx, f.human, scopeA, row.ID)
	if err != nil || out.Status != JobStatusFailed || out.Error != "result_unavailable" {
		t.Fatal("legacy poll never terminates", out, err)
	}
}

type deferredCleanupUnavailable struct{ opsFakeRepo }

func (r deferredCleanupUnavailable) Transaction(context.Context, func(context.Context, Tx) error) error {
	return ErrUnavailable
}

func TestDeferredLegacyScrubFailureNeverReturnsBody(t *testing.T) {
	f := newOpsFixture(t)
	row := legacyDeferred(t, f, "legacy-readonly")
	f.svc.Repo = deferredCleanupUnavailable{opsFakeRepo{f.repo}}
	out, err := f.svc.GetOperation(testCtx, f.human, scopeA, row.ID)
	if err != nil || out.Result != nil || out.Error != "result_unavailable" {
		t.Fatal("cleanup failure exposed body", out, err)
	}
	raw, _ := f.repo.Get(testCtx, colJobs, scopeA, row.ID)
	if !strings.Contains(structuredText(raw), "synthetic-cached-response-canary") {
		t.Fatal("fixture did not preserve cleanup failure")
	}
}
func TestDeferredLegacyListScrubsOwnedBodiesAndHidesOwnerlessRows(t *testing.T) {
	f := newOpsFixture(t)
	row := legacyDeferred(t, f, "legacy-list")
	f.put(colJobs, Record{ID: "legacy-no-owner", Kind: deferredCallKind, Status: JobStatusDone, Deferred: &DeferredCall{Result: json.RawMessage(`{"text":"unowned-canary"}`)}})
	page, err := f.svc.ListOperations(testCtx, f.human, OpsListInput{Scope: scopeA})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != row.ID || page.Items[0].Result != nil || page.Items[0].Error != "result_unavailable" {
		t.Fatal("unsafe list", page, err)
	}
	raw, _ := f.repo.Get(testCtx, colJobs, scopeA, row.ID)
	if strings.Contains(structuredText(raw), "synthetic-cached-response-canary") {
		t.Fatal("listed owned response not scrubbed")
	}
	if _, err := f.svc.GetOperation(testCtx, f.human, scopeA, "legacy-no-owner"); !errors.Is(err, ErrNotFound) {
		t.Fatal("ownerless response accessible", err)
	}
}
