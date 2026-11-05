package memory

import (
	"encoding/json"
	"errors"
	"testing"
)

func changeInput(c ChangeRequest) MaintenanceInput {
	return MaintenanceInput{Scope: scopeA, Proposal: MaintenanceProposal{Type: changeType, Reason: c.Action, Change: &c}}
}

func TestChangeProposalIsOnlyRecordedAndDecidedByAPerson(t *testing.T) {
	f := newFixture(t)
	dir := f.put(colDirectives, Record{ID: "d1", Kind: "directive", Status: "active"})
	in := changeInput(ChangeRequest{Action: "directive_delete", Target: dir.ID, Revision: dir.Revision})
	p, err := f.svc.ProposeMaintenance(testCtx, f.agent, in)
	if err != nil || p.Status != maintenanceOpen || p.Maintenance.Change == nil {
		t.Fatalf("propose = %+v, %v", p, err)
	}
	if again, _ := f.svc.ProposeMaintenance(testCtx, f.agent, in); !again.Duplicate || again.ID != p.ID {
		t.Fatal("the same change was proposed twice")
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.agent, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent decided", err)
	}
	for _, bad := range []string{"swap", "forget_both"} {
		if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: bad}); err == nil {
			t.Fatalf("decision %s was taken", bad)
		}
	}
	done, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"})
	if err != nil || done.Status != "accepted" {
		t.Fatalf("accept = %+v, %v", done, err)
	}
	if got, err := f.repo.Get(testCtx, colDirectives, scopeA, dir.ID); err != nil || got.Status != "active" || got.Revision != dir.Revision {
		t.Fatalf("accepting changed the directive: %+v, %v", got, err)
	}
}

func TestChangeProposalChecksShapeTargetAndText(t *testing.T) {
	f := newFixture(t)
	dir := f.put(colDirectives, Record{ID: "d1", Kind: "directive", Status: "active"})
	f.put(colJobs, Record{ID: "j1", Kind: "file_memory_extract"})
	cases := []struct {
		name string
		c    ChangeRequest
		want error
	}{
		{"unknown action", ChangeRequest{Action: "drop_table"}, ErrInvalid},
		{"missing revision", ChangeRequest{Action: "directive_delete", Target: dir.ID}, ErrInvalid},
		{"target on untargeted", ChangeRequest{Action: "memories_clear", Target: "x"}, ErrInvalid},
		{"params on paramless", ChangeRequest{Action: "memories_clear", Params: json.RawMessage(`{"a":1}`)}, ErrInvalid},
		{"page without title", ChangeRequest{Action: "knowledge_page_create", Params: json.RawMessage(`{"text":"x"}`)}, ErrInvalid},
		{"unknown param", ChangeRequest{Action: "knowledge_folder_create", Params: json.RawMessage(`{"path":"a","x":1}`)}, ErrInvalid},
		{"bad config", ChangeRequest{Action: "space_config_change", Params: json.RawMessage(`{"nope":true}`)}, ErrInvalid},
		{"space without project", ChangeRequest{Action: "space_create", Params: json.RawMessage(`{"workspace_id":"ws-a"}`)}, ErrInvalid},
		{"space with bad settings", ChangeRequest{Action: "space_create", Params: json.RawMessage(`{"project_id":"p2","config":{"nope":true}}`)}, ErrInvalid},
		{"space delete with target", ChangeRequest{Action: "space_delete", Target: "proj-a"}, ErrInvalid},
		{"stale revision", ChangeRequest{Action: "directive_delete", Target: dir.ID, Revision: dir.Revision + 1}, ErrConflict},
		{"missing target", ChangeRequest{Action: "mental_model_refresh", Target: "nope"}, ErrNotFound},
		{"not a document", ChangeRequest{Action: "document_delete", Target: "j1"}, ErrNotFound},
		{"secret", ChangeRequest{Action: "knowledge_page_create", Params: json.RawMessage(`{"title":"t","text":"key AKIAIOSFODNN7EXAMPLE"}`)}, ErrSecretDetected},
		{"private", ChangeRequest{Action: "knowledge_page_create", Params: json.RawMessage(`{"title":"t","text":"<private>x</private>"}`)}, ErrInvalid},
	}
	for _, tc := range cases {
		if _, err := f.svc.ProposeMaintenance(testCtx, f.agent, changeInput(tc.c)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	for _, ok := range []ChangeRequest{
		{Action: "knowledge_folder_create", Params: json.RawMessage(`{"path":"notes/a"}`)},
		{Action: "knowledge_page_create", Params: json.RawMessage(`{"title":"제목","text":"본문"}`)},
		{Action: "space_config_change", Params: json.RawMessage(`{"mcp_tools":["recall"]}`)},
		{Action: "memories_clear"},
		{Action: "space_create", Params: json.RawMessage(`{"project_id":"proj-new","config":{"mcp_tools":["recall"]}}`)},
		{Action: "space_delete"},
	} {
		if _, err := f.svc.ProposeMaintenance(testCtx, f.agent, changeInput(ok)); err != nil {
			t.Errorf("%s: %v", ok.Action, err)
		}
	}
	// A change proposal carries no memory pair or signal fields.
	in := changeInput(ChangeRequest{Action: "memories_clear"})
	in.Proposal.Keep = Ref{Collection: colMemories, ID: "m", Revision: 1}
	if _, err := f.svc.ProposeMaintenance(testCtx, f.agent, in); !errors.Is(err, ErrInvalid) {
		t.Fatal("mixed proposal", err)
	}
}

func TestListDocumentsPagesDocumentsWithTheirLatestUpload(t *testing.T) {
	f := newOpsFixture(t)
	for _, id := range []string{"doc-a", "doc-b", "doc-c"} {
		f.put(colJobs, Record{ID: id, Kind: fileExtractDocKind})
	}
	f.put(colJobs, Record{ID: "other", Kind: "file_memory_extract"})
	first, err := f.svc.ListExtractDocuments(testCtx, f.human, scopeA, DocumentPage{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	rest, err := f.svc.ListExtractDocuments(testCtx, f.human, scopeA, DocumentPage{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
	seen := map[string]bool{}
	for _, it := range append(first.Items, rest.Items...) {
		seen[it.ID] = true
	}
	if len(seen) != 3 || seen["other"] {
		t.Fatalf("listed %v", seen)
	}
	if _, err := f.svc.ListExtractDocuments(testCtx, "no-such-token", scopeA, DocumentPage{}); err == nil {
		t.Fatal("listed without a token")
	}
}
