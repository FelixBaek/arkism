package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCandidateReviewEditPreservesProposal(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "original proposal", Summary: "stale summary", Keywords: []string{"old-key"}, Sources: refs(src)}))
	in := CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "edit_approve", Content: "reviewed decision"}
	m, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if m.Status != "active" || m.Text != in.Content || m.Trust != 1 || m.Summary != "" || len(m.Keywords) != 0 || m.Embedding != nil || m.ContentHash != ContentHash(in.Content) {
		t.Fatal("edited memory", m)
	}
	old := f.get(f.human, "memory_candidates", scopeA, c.ID)
	if old.Text != c.Text || old.Summary != c.Summary || old.Status != "approved" || old.Review == nil || old.Review.Action != "edit_approve" || old.Review.By == "" || structuredText(old.Sources) != structuredText(c.Sources) {
		t.Fatal("proposal provenance lost")
	}
	jobs := f.repo.rows("jobs")
	found := 0
	for _, job := range jobs {
		if job.Kind == "embedding" && job.Target.ID == m.ID {
			found++
			if job.Target.Revision != m.Revision {
				t.Fatal("job revision")
			}
		}
	}
	if found != 1 {
		t.Fatal("missing single embedding job")
	}
	if _, e = f.svc.ReviewCandidate(testCtx, f.human, c.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("duplicate review", e)
	}
}
func TestCandidateReviewRejectAndGuards(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	makeCandidate := func(text string) Record {
		return f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src)}))
	}
	c := makeCandidate("rejectable candidate")
	in := CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "reject", Reason: "evidence insufficient"}
	if _, e := f.svc.ReviewCandidate(testCtx, f.agent, c.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("agent review", e)
	}
	secret := in
	secret.Reason = "password=synthetic-test-secret"
	if _, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, secret); !errors.Is(e, ErrSecretDetected) {
		t.Fatal("secret reason", e)
	}
	empty := in
	empty.Reason = " "
	if _, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, empty); !errors.Is(e, ErrInvalid) {
		t.Fatal("empty reason", e)
	}
	f.repo.failAudit = true
	if r, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, in); e == nil || r.ID != "" {
		t.Fatal("failed audit returned result")
	}
	f.repo.failAudit = false
	if old := f.get(f.human, "memory_candidates", scopeA, c.ID); old.Status != "candidate" {
		t.Fatal("audit rollback")
	}
	// Rejection must remain possible after its evidence disappears.
	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	r, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, in)
	if e != nil || r.Status != "rejected" || r.Review.Reason != in.Reason {
		t.Fatal("reject broken evidence", e)
	}
	if len(f.repo.rows("memories")) != 0 {
		t.Fatal("rejection created memory")
	}
}
func TestCandidateApprovalAtomicRollbackAndPrivateEdit(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "private candidate", Sources: refs(src)}))
	in := CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "edit_approve", Content: "Contact reviewer@example.test"}
	f.repo.failAudit = true
	if _, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, in); e == nil {
		t.Fatal("audit fail open")
	}
	if len(f.repo.rows("memories")) != 0 {
		t.Fatal("memory escaped rollback")
	}
	for _, job := range f.repo.rows("jobs") {
		if job.Kind == "embedding" {
			t.Fatal("job escaped rollback")
		}
	}
	f.repo.failAudit = false
	m, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, in)
	if e != nil || m.Sensitivity != "private" || m.Inspection == nil || !m.Inspection.PersonalData {
		t.Fatal("PII classification", e)
	}
	for _, event := range f.repo.rows("audit_events") {
		if event.Text != "" || event.Summary != "" || event.Review != nil {
			t.Fatal("audit contains review body")
		}
	}
}

func TestCandidateReviewHTTPRequiresPolicyContentSafety(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "approval HTTP candidate", Sources: refs(src)}))
	in := CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"}
	payload, _ := json.Marshal(in)
	for _, tc := range []struct {
		safe   bool
		status int
	}{{false, 403}, {true, 200}} {
		var sent Authorization
		f.svc.Auth = policyDecision(Decision{Allowed: true, PrincipalID: "reviewer", Role: "human", DecisionID: "decision-1", ContentSafe: tc.safe}, &sent)
		req := httptest.NewRequest("POST", "/v1/memory_candidates/"+c.ID+"/review", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+callerToken)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("HTTP got %d want %d", w.Code, tc.status)
		}
		if sent.Action != "transition:memory_candidates" || sent.ResourceID != c.ID || sent.Content == "" {
			t.Fatal("missing policy action/content")
		}
		if tc.status == 200 {
			var m Record
			if json.Unmarshal(w.Body.Bytes(), &m) != nil || m.Status != "active" || m.Text != c.Text {
				t.Fatal("approve did not activate original content")
			}
		}
	}
}

func TestCandidateReviewBatchPartialFailure(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	makeItem := func(text string) CandidateReviewBatchItem {
		r := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src)}))
		return CandidateReviewBatchItem{ID: r.ID, ExpectedRevision: r.Revision, Action: "approve"}
	}
	first, last := makeItem("batch first"), makeItem("batch last")
	bad := makeItem("batch invalid")
	bad.Action = "reject" // missing required reason
	denied := makeItem("batch denied")
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: denied.ID}
	in := CandidateReviewBatchInput{Scope: scopeA, Items: []CandidateReviewBatchItem{first, bad, denied, first, last}}
	out, e := f.svc.ReviewCandidates(testCtx, f.human, in)
	if e != nil || len(out.Items) != 5 {
		t.Fatal("batch", e)
	}
	for i, want := range []int{200, 400, 403, 409, 200} {
		item := out.Items[i]
		if item.Index != i || item.Status != want {
			t.Fatalf("result %d: %+v", i, item)
		}
		if want == 200 && (item.Record == nil || item.Record.Status != "active" || item.Code != "") {
			t.Fatal("missing success record")
		}
		if want != 200 && (item.Record != nil || item.Code == "") {
			t.Fatal("failure exposed record")
		}
	}
	if len(f.repo.rows("memories")) != 2 {
		t.Fatal("partial success lost")
	}
	for _, id := range []string{bad.ID, denied.ID} {
		r, e := f.repo.Get(testCtx, "memory_candidates", scopeA, id)
		if e != nil || r.Status != "candidate" {
			t.Fatal("failed item mutated", e)
		}
	}
}

type cancelAfterReviewRepo struct {
	Repository
	cancel context.CancelFunc
	calls  int
}

func (r *cancelAfterReviewRepo) Transaction(ctx context.Context, f func(context.Context, Tx) error) error {
	r.calls++
	e := r.Repository.Transaction(ctx, f)
	if e == nil {
		r.cancel()
	}
	return e
}
func TestCandidateReviewBatchCancellation(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	in := CandidateReviewBatchInput{Scope: scopeA}
	for _, text := range []string{"first committed", "second not attempted"} {
		r := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src)}))
		in.Items = append(in.Items, CandidateReviewBatchItem{ID: r.ID, ExpectedRevision: r.Revision, Action: "approve"})
	}
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	repo := &cancelAfterReviewRepo{Repository: f.repo, cancel: cancel}
	f.svc.Repo = repo
	out, e := f.svc.ReviewCandidates(ctx, f.human, in)
	if e != nil || out.Items[0].Status != 200 || out.Items[1].Status != 503 || repo.calls != 1 {
		t.Fatal("cancelled batch continued", out, e)
	}
	if len(f.repo.rows("memories")) != 1 {
		t.Fatal("committed success lost or unattempted item changed")
	}
}
func TestCandidateReviewBatchHTTPBoundary(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "HTTP batch candidate", Sources: refs(src)}))
	item := CandidateReviewBatchItem{ID: c.ID, ExpectedRevision: c.Revision, Action: "approve"}
	in := CandidateReviewBatchInput{Scope: scopeA, Items: []CandidateReviewBatchItem{item}}
	h := Handler(f.svc)
	request := func(payload []byte, token string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/memory_candidates/review_batch", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("batch HTTP %d want %d", w.Code, want)
		}
		return w.Body.String()
	}
	raw, _ := json.Marshal(in)
	request(raw, f.agent, 403)
	request(raw, "", 403)
	request(bytes.Replace(raw, []byte(`"action":"approve"`), []byte(`"action":"approve","scope":{}`), 1), f.human, 400)
	over := in
	over.Items = make([]CandidateReviewBatchItem, 21)
	rawOver, _ := json.Marshal(over)
	request(rawOver, f.human, 400)
	empty := in
	empty.Items = nil
	rawEmpty, _ := json.Marshal(empty)
	request(rawEmpty, f.human, 400)
	if len(f.repo.rows("memories")) != 0 {
		t.Fatal("invalid envelope wrote data")
	}
	secret := in
	secret.Items = []CandidateReviewBatchItem{{ID: "password=synthetic-secret", ExpectedRevision: 1, Action: "approve"}}
	rawSecret, _ := json.Marshal(secret)
	body := request(rawSecret, f.human, 200)
	if strings.Contains(body, "synthetic-secret") || strings.Contains(body, "password") {
		t.Fatal("failed item echoed ID")
	}
	var result CandidateReviewBatchOutput
	if json.Unmarshal([]byte(body), &result) != nil || len(result.Items) != 1 || result.Items[0].Status != 403 {
		t.Fatal("secret ID not denied")
	}
	body = request(raw, f.human, 200)
	if json.Unmarshal([]byte(body), &result) != nil || result.Items[0].Record == nil || result.Items[0].Record.Status != "active" {
		t.Fatal("HTTP batch did not commit")
	}
}
