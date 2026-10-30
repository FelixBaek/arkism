package memory

import (
	"bytes"
	"encoding/json"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type productionViewReader struct {
	receiptChainIndependentHost
	refs []Ref
}

func (r *productionViewReader) ReceiptAcknowledgement(cycle string) (capture.ReceiptAcknowledgement, error) {
	var wire struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(r.Bytes(), &wire) != nil {
		return capture.ReceiptAcknowledgement{}, receiptchain.ErrInvalid
	}
	reads := []capture.ReceiptSourceRead{}
	r.refs = []Ref{}
	for _, m := range regexp.MustCompile(`\(id: ([^)]+)\)`).FindAllStringSubmatch(wire.Hook.Context, -1) {
		read, e := r.readMemory(m[1], "prompt")
		if e != nil {
			return capture.ReceiptAcknowledgement{}, e
		}
		reads = append(reads, read)
		r.refs = append(r.refs, Ref(read.Source.Ref))
	}
	raw, _ := json.Marshal(r.refs)
	answer := []byte("observed-reader-output:" + receiptchain.Hash(raw))
	return capture.ReceiptAcknowledgement{CycleID: cycle, ReaderSHA256: r.reader, InputSHA256: receiptchain.Hash(r.Bytes()), Reads: reads, Answer: answer}, nil
}

type productionViewHTTP struct {
	mu            sync.Mutex
	build, search int
}

func (s *productionViewHTTP) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if r.URL.Path == "/v1/context/build" {
			s.build++
		}
		if r.URL.Path == "/v1/search" {
			s.search++
		}
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}
func productionViewSetup(t *testing.T) (*receiptChainPGFixture, *productionViewHTTP) {
	t.Helper()
	f := receiptChainPG(t)
	spy := &productionViewHTTP{}
	server := httptest.NewServer(spy.handler(Handler(&Service{Repo: f.repo, Auth: f.p.worker.Auth})))
	t.Cleanup(server.Close)
	host.BaseURL = server.URL
	t.Setenv("ARKISM_RECALL_BUDGET", "user=8:6000")
	return f, spy
}
func productionViewOption(scope Scope, exclude ...Ref) *capture.ContextViewRecall {
	refs := []receiptchain.Ref{}
	for _, r := range exclude {
		refs = append(refs, receiptchain.Ref(r))
	}
	return &capture.ContextViewRecall{Protocol: ContextViewProtocol, Scope: receiptchain.Scope(scope), Exclude: refs, TokenEncoding: "cl100k_base", BudgetTokens: 64000, MaxItems: 8}
}
func productionViewInvoke(t *testing.T, f *receiptChainPGFixture, sid string, view *capture.ContextViewRecall) (*receiptchain.Session, []Ref) {
	t.Helper()
	reader := capture.ReceiptReaderIdentity("receipt_fixture_host", sid)
	session, e := receiptchain.New(receiptchain.Scope(f.p.in.Scope), reader, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	rawToken, e := osReadProductionFixtureToken()
	if e != nil {
		t.Fatal(e)
	}
	out := &productionViewReader{receiptChainIndependentHost: receiptChainIndependentHost{reader: reader, baseURL: host.BaseURL, token: rawToken, client: &http.Client{Timeout: 2 * time.Second}, scope: f.p.in.Scope}}
	if e = capture.HookWithReceiptChainView(map[string]any{"session_id": sid, "prompt": "receiptfixture viewcanary production invocation"}, "UserPromptSubmit", out, session, view); e != nil {
		t.Fatal(e)
	}
	for _, record := range session.Snapshot().Records {
		if record.ActualUse != nil || record.TaskSuccess != nil || record.CausalContribution != nil {
			t.Fatal("delivery became semantic verdict")
		}
	}
	return session, out.refs
}

// Only the synthetic token file explicitly created by receiptChainPG is read.
func osReadProductionFixtureToken() (string, error) {
	b, e := os.ReadFile(host.TokenFile)
	return strings.TrimSpace(string(b)), e
}
func productionHas(refs []Ref, id string) bool {
	for _, r := range refs {
		if r.ID == id {
			return true
		}
	}
	return false
}
func productionPublish(t *testing.T, f *receiptChainPGFixture, s *receiptchain.Session) {
	t.Helper()
	out, e := f.producer.PublishReceiptChain(testCtx, s, f.spool, receiptChainAgent(f.p.in.Scope))
	if e != nil || out.Stored != "observed" || out.Readback != "observed" || out.Spooled != "observed" {
		t.Fatal("production chain persistence", e)
	}
}
func TestProductionViewPostgresActualInvocationPhases(t *testing.T) {
	f, spy := productionViewSetup(t)
	sid := "production-current-" + ID()
	target := f.targets[0]
	view := productionViewOption(f.p.in.Scope, target)
	session, refs := productionViewInvoke(t, f, sid, view)
	if productionHas(refs, target.ID) || len(refs) != 2 {
		t.Fatal("excluded current ref delivered", refs)
	}
	productionPublish(t, f, session)
	productionExcludedReceipt(t, session, target.ID)
	next, refs := productionViewInvoke(t, f, sid, productionViewOption(f.p.in.Scope))
	if !productionHas(refs, target.ID) || len(refs) != 1 {
		t.Fatal("one-shot exclusion became persistent", refs)
	}
	productionPublish(t, f, next)
	fresh, refs := productionViewInvoke(t, f, "production-new-"+ID(), productionViewOption(f.p.in.Scope))
	if len(refs) != 3 || !productionHas(refs, target.ID) {
		t.Fatal("newreader remained masked")
	}
	productionPublish(t, f, fresh)
	if spy.build != 3 || spy.search != 0 {
		t.Fatal("production endpoint not exercised", spy.build, spy.search)
	}
	if len(f.p.rows(t, "consumption")) != 0 || len(f.p.rows(t, "state")) != 0 {
		t.Fatal("view changed native learning")
	}
}
func TestProductionViewPostgresExactRevisionAndSameBody(t *testing.T) {
	f, spy := productionViewSetup(t)
	target := f.targets[0]
	wrong := target
	wrong.Revision++
	_, refs := productionViewInvoke(t, f, "revision-"+ID(), productionViewOption(f.p.in.Scope, wrong))
	if !productionHas(refs, target.ID) {
		t.Fatal("different revision overmasked")
	}
	fixture := newFixture(t)
	fixture.svc.Repo = f.p.admin
	text := "receiptfixture same body" + strings.Repeat(" body", 40)
	a := viewMemory(t, fixture, f.p.in.Scope, text, "same-body:a")
	b := viewMemory(t, fixture, f.p.in.Scope, text, "same-body:b")
	_, refs = productionViewInvoke(t, f, "samebody-"+ID(), productionViewOption(f.p.in.Scope, Ref{colMemories, a.ID, a.Revision}))
	if productionHas(refs, a.ID) || !productionHas(refs, b.ID) {
		t.Fatal("body equality overmasked distinct ref", refs)
	}
	if spy.search != 0 || spy.build != 2 {
		t.Fatal("extra retrieval")
	}
}
func TestProductionViewPostgresCorrectionWithdrawal(t *testing.T) {
	f, _ := productionViewSetup(t)
	fixture := newFixture(t)
	fixture.svc.Repo = f.p.admin
	old := viewMemory(t, fixture, f.p.in.Scope, "viewcanary old lasting preference"+strings.Repeat(" old", 40), "view:old")
	sid := "persistent-" + ID()
	_, refs := productionViewInvoke(t, f, sid, productionViewOption(f.p.in.Scope))
	if !productionHas(refs, old.ID) {
		t.Fatal("old absent")
	}
	corrected := productionLongCorrection(t, fixture, old)
	productionAssertPersistent(t, f, sid, old.ID, corrected.ID, true)
	if e := fixture.svc.Delete(testCtx, fixture.human, colMemories, corrected.ID, ChangeInput{Scope: f.p.in.Scope, ExpectedRevision: corrected.Revision}); e != nil {
		t.Fatal(e)
	}
	productionAssertPersistent(t, f, sid, old.ID, corrected.ID, false)
}
func productionAssertPersistent(t *testing.T, f *receiptChainPGFixture, sid, oldID, currentID string, present bool) {
	t.Helper()
	for phase, reader := range []string{sid, sid, "persistent-new-" + ID()} {
		session, refs := productionViewInvoke(t, f, reader, productionViewOption(f.p.in.Scope))
		if productionHas(refs, oldID) {
			t.Fatal("superseded old delivered")
		}
		if present && phase != 1 && !productionHas(refs, currentID) {
			t.Fatal("corrected current absent")
		}
		if present && phase == 1 && !productionPriorContains(session, currentID) {
			t.Fatal("next input lost retained path evidence")
		}
		if !present && productionHas(refs, currentID) {
			t.Fatal("withdrawn current delivered")
		}
	}
}

func productionExcludedReceipt(t *testing.T, session *receiptchain.Session, targetID string) {
	t.Helper()
	for _, r := range session.Snapshot().Records {
		if r.Path == "prompt" && r.Stage != "prior_path" {
			for _, s := range r.Sources {
				if s.Ref.ID == targetID {
					t.Fatal("excluded ref receipted")
				}
			}
		}
	}
}

func productionLongCorrection(t *testing.T, f *fixture, old Record) Record {
	t.Helper()
	claim := explicit("viewcanary corrected preference" + strings.Repeat(" new", 40))
	claim.Subject = "viewcanary"
	claim.Supersedes = &Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}
	captured, e := f.svc.Capture(testCtx, f.human, captureInput(scopeA, "production-view-correction", claim))
	if e != nil || len(captured.Items) != 1 {
		t.Fatal("capture correction", e)
	}
	candidate := f.get(f.human, "memory_candidates", scopeA, captured.Items[0].CandidateID)
	current := f.get(f.human, colMemories, scopeA, old.ID)
	relation := f.get(f.human, "memory_relations", scopeA, candidate.ConflictID)
	if _, e = f.svc.ReviewCandidate(testCtx, f.agent, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"}); e == nil {
		t.Fatal("agent approved correction")
	}
	m, e := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve", OperationID: "production-human-confirm", Conflict: &CandidateReviewConflict{ID: relation.ID, ExpectedRevision: relation.Revision, Memory: Ref{colMemories, current.ID, current.Revision}, Resolution: "replace_old", Reason: "explicit human correction"}})
	if e != nil || m.Status != "active" || m.ApprovedBy == "" {
		t.Fatal("human correction", e)
	}
	return m
}

func TestProductionViewPostgresMalformedRefNoFallback(t *testing.T) {
	f, spy := productionViewSetup(t)
	view := productionViewOption(f.p.in.Scope, Ref{colMemories, f.targets[0].ID, 0})
	var out bytes.Buffer
	e := capture.HookWithReceiptChainView(map[string]any{"session_id": "malformed-" + ID(), "prompt": "receiptfixture production invalid ref"}, "UserPromptSubmit", &out, nil, view)
	if e == nil || out.Len() != 0 || spy.search != 0 || spy.build != 1 {
		t.Fatal("invalid selector broadened", e, spy.build, spy.search)
	}
}

func productionPriorContains(s *receiptchain.Session, id string) bool {
	for _, r := range s.Snapshot().Records {
		if r.Stage == "prior_path" {
			for _, source := range r.Sources {
				if source.Ref.ID == id {
					return true
				}
			}
		}
	}
	return false
}
