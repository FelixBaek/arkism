package memory

import (
	"strings"
	"testing"
)

func TestSpaceRenamerMapsDerivedIDsAndRewritesReferences(t *testing.T) {
	to := Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: "renamed"}
	sub := Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "u1"}
	toSub := Scope{WorkspaceID: to.WorkspaceID, ProjectID: to.ProjectID, UserID: "u1"}
	m := newSpaceRenamer(scopeA, to, []Scope{scopeA, sub})
	from, into := wikiPageID(sub, "a"), wikiPageID(toSub, "a")
	m.plan(colWikiPages, Record{ID: from, Scope: sub, Wiki: &WikiRecord{Page: &WikiPage{Slug: "a"}}})
	m.plan(colRouting, Record{ID: routingID(scopeA), Scope: scopeA})
	m.plan(colMemories, Record{ID: "mem-1", Scope: scopeA})
	link := wikiLinkID(sub, from, from)
	m.planLink(Record{ID: link, Scope: sub, Wiki: &WikiRecord{Link: &WikiLink{FromID: from, ToID: from}}})
	if m.id(from) != into || m.id(routingID(scopeA)) != routingID(to) || m.id("mem-1") != "mem-1" || m.id(link) != wikiLinkID(toSub, into, into) {
		t.Fatalf("ids %v", m.ids)
	}
	raw := `{"id":"` + from + `","scope":{"workspace_id":"` + sub.WorkspaceID + `","project_id":"` + sub.ProjectID + `","user_id":"u1"},"n":12345678901234567890,"refs":{"` + from + `":["<b>",` + `"` + from + `"]}}`
	out, err := m.rewrite([]byte(raw))
	got := string(out)
	if err != nil || strings.Contains(got, from) || strings.Count(got, into) != 3 || !strings.Contains(got, `"project_id":"renamed"`) ||
		!strings.Contains(got, "12345678901234567890") || !strings.Contains(got, `"<b>"`) {
		t.Fatalf("rewrite %s %v", got, err)
	}
}

func TestSpaceRenamerMovesPrivacyRequestIDAndHash(t *testing.T) {
	to := Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: "renamed"}
	req := PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "k1", TargetType: "source_event", TargetIDs: []string{"src-1"}, Reason: "asked", RetentionPolicyRef: retentionExternal}
	oldID, oldHash := privacyJobID(scopeA, "human-1", "k1"), ContentHash(structuredText(req))
	pd := &PrivacyDeletion{RequesterID: "human-1", IdempotencyKey: "k1", TargetType: req.TargetType, TargetIDs: req.TargetIDs, Reason: req.Reason, RetentionPolicyRef: req.RetentionPolicyRef, RequestHash: oldHash, PurgeReportRef: "jobs/" + oldID + "#privacy_deletion.report"}
	m := newSpaceRenamer(scopeA, to, []Scope{scopeA})
	m.plan(colJobs, Record{ID: oldID, Scope: scopeA, Kind: privacyDeletionKind, PrivacyDeletion: pd})
	req.Scope = to
	newID, newHash := privacyJobID(to, "human-1", "k1"), ContentHash(structuredText(req))
	if m.id(oldID) != newID || m.id(oldHash) != newHash || m.id(pd.PurgeReportRef) != "jobs/"+newID+"#privacy_deletion.report" || len(m.ids) != 1 {
		t.Fatalf("ids %v texts %v", m.ids, m.texts)
	}
	// A hash the row's fields do not reproduce stays as it was.
	pd.RequestHash = "other"
	m = newSpaceRenamer(scopeA, to, []Scope{scopeA})
	m.plan(colJobs, Record{ID: oldID, Scope: scopeA, Kind: privacyDeletionKind, PrivacyDeletion: pd})
	if m.id(oldID) != newID || m.id("other") != "other" {
		t.Fatalf("ids %v texts %v", m.ids, m.texts)
	}
}
