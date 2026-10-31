package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// EXP-01 native label-free materialization: the plan's events go through the product's own
// candidate review, conflict and delete paths on a disposable PG, and each query runs the
// product search. Only events.jsonl and queries.jsonl are read; a directory holding gold.jsonl
// or any other file is refused, so no label can reach the run.

type exp01Ref struct {
	Origin        string `json:"origin"`
	Collection    string `json:"collection"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	ContentSHA256 string `json:"content_sha256"`
}

// Every label-free field is declared so the decoder can refuse unknown keys (a label such as
// required_refs or cohort). Fields the product has no input for stay raw and are reported as
// dropped on each step.
type exp01Event struct {
	EventID       string          `json:"event_id"`
	StreamID      string          `json:"stream_id"`
	PlanSeq       int64           `json:"plan_seq"`
	Kind          string          `json:"kind"`
	Target        *exp01Ref       `json:"target"`
	RelatedRefs   []exp01Ref      `json:"related_refs"`
	SourcePayload *exp01Payload   `json:"source_payload"`
	ArrivedAtNS   json.RawMessage `json:"arrived_at_ns"`
	AvailableAtNS json.RawMessage `json:"available_at_ns"`
	SourceAtNS    json.RawMessage `json:"source_at_ns"`
	CauseEventIDs json.RawMessage `json:"cause_event_ids"`
	ClockDomain   json.RawMessage `json:"clock_domain"`
	Outcome       json.RawMessage `json:"outcome"`
	Scope         json.RawMessage `json:"scope"`
	TaskID        json.RawMessage `json:"task_id"`
}
type exp01Payload struct {
	Text          string          `json:"text"`
	Kind          string          `json:"kind"`
	Summary       string          `json:"summary"`
	CreatedAtNS   json.RawMessage `json:"created_at_ns"`
	EffectiveAtNS json.RawMessage `json:"effective_at_ns"`
	UpdatedAtNS   json.RawMessage `json:"updated_at_ns"`
}
type exp01Query struct {
	QueryID         string          `json:"query_id"`
	StreamID        string          `json:"stream_id"`
	CutoffPlanSeq   int64           `json:"cutoff_plan_seq"`
	QueryText       string          `json:"query_text"`
	RequestMode     string          `json:"request_mode"`
	RequestedAsOfNS *int64          `json:"requested_as_of_ns"`
	AtNS            json.RawMessage `json:"at_ns"`
	ClockDomain     json.RawMessage `json:"clock_domain"`
	Scope           json.RawMessage `json:"scope"`
}

// exp01Step is one materialized event; product_action names the product call, or none when the
// baseline search has no input for that event kind. dropped_fields lists the non-empty plan fields
// the product call did not carry (times, scope, summary, outcome), so "applied" is never read as
// a faithful copy of the plan event.
type exp01Step struct {
	EventID       string   `json:"event_id"`
	Kind          string   `json:"kind"`
	ProductAction string   `json:"product_action"`
	Status        string   `json:"status"`
	ErrorCode     string   `json:"error_code,omitempty"`
	DroppedFields []string `json:"dropped_fields"`
}

// exp01Hit is one search hit: the plan ref it was bound to plus what the product returned, so a
// scorer can check the bound text against the plan instead of trusting the binding.
type exp01Hit struct {
	Plan               exp01Ref `json:"plan"`
	ProductCollection  string   `json:"product_collection"`
	ProductID          string   `json:"product_id"`
	ProductRevision    int64    `json:"product_revision"`
	ObservedTextSHA256 string   `json:"observed_text_sha256"`
}
type exp01Answer struct {
	QueryID         string     `json:"query_id"`
	StreamID        string     `json:"stream_id"`
	RequestMode     string     `json:"request_mode"`
	RequestedAsOfNS *int64     `json:"requested_as_of_ns"`
	Search          string     `json:"search"`
	Items           []exp01Hit `json:"items"`
	History         []exp01Hit `json:"history"`
	Unmapped        int        `json:"unmapped"`
	ContentMismatch int        `json:"content_mismatch"`
	Degraded        []string   `json:"degraded"`
}

func exp01SHA(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func exp01Lines[T any](path string) ([]T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := []T{}
	for i, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var row T
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&row); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		if dec.More() {
			return nil, fmt.Errorf("%s:%d: trailing data", path, i+1)
		}
		out = append(out, row)
	}
	return out, nil
}

// exp01LabelFree refuses any directory that is not exactly the two label-free regular files
// (no gold.jsonl, no extra file, no symlink).
func exp01LabelFree(dir string) error {
	names, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	got := []string{}
	for _, n := range names {
		if !n.Type().IsRegular() {
			return fmt.Errorf("label-free input %s is not a regular file", n.Name())
		}
		got = append(got, n.Name())
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "events.jsonl" || got[1] != "queries.jsonl" {
		return fmt.Errorf("label-free input must hold only events.jsonl and queries.jsonl, got %v", got)
	}
	return nil
}

// exp01Valid refuses an event the harness cannot apply without guessing, before any product call.
func exp01Valid(e exp01Event) error {
	switch {
	case e.EventID == "" || len(e.StreamID) < 16:
		return errors.New("event_id or stream_id missing")
	case e.Kind == "material_available" && (e.Target == nil || e.SourcePayload == nil):
		return errors.New("material_available needs target and source_payload")
	case (e.Kind == "correction_proposed" || e.Kind == "correction_committed") && (e.Target == nil || len(e.RelatedRefs) != 1):
		return errors.New(e.Kind + " needs target and one related ref")
	case e.Kind == "material_deleted" && e.Target == nil:
		return errors.New("material_deleted needs target")
	}
	return nil
}

func exp01Inputs(dir string) ([]exp01Event, []exp01Query, map[string]string, error) {
	if err := exp01LabelFree(dir); err != nil {
		return nil, nil, nil, err
	}
	sums := map[string]string{}
	for _, n := range []string{"events.jsonl", "queries.jsonl"} {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, nil, nil, err
		}
		sums[n] = exp01SHA(b)
	}
	events, err := exp01Lines[exp01Event](filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, nil, nil, err
	}
	for _, e := range events {
		if err = exp01Valid(e); err != nil {
			return nil, nil, nil, fmt.Errorf("event %q: %w", e.EventID, err)
		}
	}
	queries, err := exp01Lines[exp01Query](filepath.Join(dir, "queries.jsonl"))
	return events, queries, sums, err
}

// exp01Dropped names the non-empty plan fields of e that no product call carries.
func exp01Dropped(e exp01Event) []string {
	out := []string{}
	for _, f := range []struct {
		name string
		raw  json.RawMessage
	}{{"arrived_at_ns", e.ArrivedAtNS}, {"available_at_ns", e.AvailableAtNS}, {"source_at_ns", e.SourceAtNS},
		{"cause_event_ids", e.CauseEventIDs}, {"outcome", e.Outcome}, {"scope", e.Scope}, {"task_id", e.TaskID}} {
		if v := string(bytes.TrimSpace(f.raw)); v != "" && v != "null" && v != "[]" && v != "{}" {
			out = append(out, f.name)
		}
	}
	if p := e.SourcePayload; p != nil {
		if p.Summary != "" {
			out = append(out, "source_payload.summary")
		}
		for _, f := range []struct {
			name string
			raw  json.RawMessage
		}{{"source_payload.created_at_ns", p.CreatedAtNS}, {"source_payload.effective_at_ns", p.EffectiveAtNS}, {"source_payload.updated_at_ns", p.UpdatedAtNS}} {
			if v := string(bytes.TrimSpace(f.raw)); v != "" && v != "null" {
				out = append(out, f.name)
			}
		}
	}
	return out
}

type exp01Bound struct {
	ref     exp01Ref
	textSHA string
}

type exp01Stream struct {
	f     *fixture
	scope Scope
	// plan ref (id@revision) -> product record and the plan text it was made from
	records map[string]Record
	text    map[string]string
	// product collection:id -> plan ref; a product id is never re-pointed to another plan ref
	plan map[string]exp01Bound
	// plan refs a later correction_proposed in this stream names as the replacement; they wait as
	// candidates instead of being approved on arrival. Derived from events, not from labels.
	awaitsCorrection map[string]bool
	conflict         map[string]ConflictOutput
}

// exp01Kinds maps plan material kinds onto product memory kinds; a plan preference is the product's
// durable user memory. A kind missing here is reported as not materializable, never guessed.
var exp01Kinds = map[string]string{"preference": "user"}

func exp01Key(r exp01Ref) string { return fmt.Sprintf("%s@%d", r.ID, r.Revision) }

func (s *exp01Stream) bind(r exp01Ref, col string, rec Record) error {
	k := col + ":" + rec.ID
	if b, ok := s.plan[k]; ok && b.ref != r {
		return fmt.Errorf("product %s already bound to %s", k, exp01Key(b.ref))
	}
	s.records[exp01Key(r)] = rec
	s.plan[k] = exp01Bound{ref: r, textSHA: s.text[exp01Key(r)]}
	return nil
}

func (s *exp01Stream) current(id string) (Record, error) {
	return s.f.svc.Repo.Get(testCtx, colMemories, s.scope, id)
}

// exp01Fail marks a step the product refused, with the product's error code.
func exp01Fail(step exp01Step, err error) exp01Step {
	step.Status = "product_refused"
	step.ErrorCode, _ = errorResponse(err)
	return step
}

// exp01Cascade marks a step the harness could not run because an earlier step left no product record.
func exp01Cascade(step exp01Step, code string) exp01Step {
	step.Status, step.ErrorCode = "harness_cascade", code
	return step
}

func (s *exp01Stream) lookup(r exp01Ref) (Record, bool) {
	rec, ok := s.records[exp01Key(r)]
	return rec, ok
}

func (s *exp01Stream) apply(e exp01Event) exp01Step {
	step := exp01Step{EventID: e.EventID, Kind: e.Kind, Status: "applied", DroppedFields: exp01Dropped(e)}
	switch e.Kind {
	case "material_available":
		return s.applyMaterial(e, step)
	case "correction_proposed":
		return s.applyCorrectionProposed(e, step)
	case "correction_committed":
		return s.applyCorrectionCommitted(e, step)
	case "material_deleted":
		return s.applyDeleted(e, step)
	default: // task_started, exposure_observed, outcome_observed: assumed to have no input path into the baseline search (not verified per event)
		step.ProductAction = "none"
		step.Status = "no_product_input"
		return step
	}
}

func (s *exp01Stream) applyMaterial(e exp01Event, step exp01Step) exp01Step {
	f := s.f
	kind, ok := exp01Kinds[e.SourcePayload.Kind]
	if !ok {
		step.Status = "not_materializable"
		step.ErrorCode = "no_product_kind"
		return step
	}
	s.text[exp01Key(*e.Target)] = exp01SHA([]byte(e.SourcePayload.Text))
	// The agent proposes and a human reviews, the product's own proposer/reviewer split.
	src, err := f.svc.Create(testCtx, f.agent, "sources", sourceInput(CreateInput{Scope: s.scope, Text: e.SourcePayload.Text}))
	if err != nil {
		return exp01Fail(step, err)
	}
	in := candidateInput(CreateInput{Scope: s.scope, Text: e.SourcePayload.Text, Kind: kind, Sources: refs(src), Generator: &Generator{Model: "exp01-plan", PromptVersion: "none", PipelineVersion: "exp01-native-labelfree-v2"}})
	cand, err := f.svc.Create(testCtx, f.agent, "memory_candidates", in)
	if err != nil {
		return exp01Fail(step, err)
	}
	if s.awaitsCorrection[exp01Key(*e.Target)] {
		step.ProductAction = "create_candidate"
		if err = s.bind(*e.Target, "memory_candidates", cand); err != nil {
			return exp01Cascade(step, "product_id_rebound")
		}
		return step
	}
	step.ProductAction = "create_candidate+review_approve"
	m, err := f.svc.ReviewCandidate(testCtx, f.human, cand.ID, CandidateReviewInput{Scope: s.scope, ExpectedRevision: cand.Revision, Action: "approve"})
	if err != nil {
		return exp01Fail(step, err)
	}
	if err = s.bind(*e.Target, colMemories, m); err != nil {
		return exp01Cascade(step, "product_id_rebound")
	}
	return step
}

func (s *exp01Stream) applyCorrectionProposed(e exp01Event, step exp01Step) exp01Step {
	f := s.f
	step.ProductAction = "conflict_mark_hold_second"
	old, ok1 := s.lookup(*e.Target)
	cand, ok2 := s.lookup(e.RelatedRefs[0])
	if !ok1 || !ok2 {
		return exp01Cascade(step, "missing_prior_ref")
	}
	cur, err := s.current(old.ID)
	if err != nil {
		return exp01Fail(step, err)
	}
	out, err := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: s.scope, Memories: []Ref{{Collection: colMemories, ID: cur.ID, Revision: cur.Revision}, {Collection: "memory_candidates", ID: cand.ID, Revision: cand.Revision}}, Action: "mark", Reason: "EXP-01 plan correction", Hold: "second"})
	if err != nil {
		return exp01Fail(step, err)
	}
	s.conflict[exp01Key(*e.Target)] = out
	return step
}

func (s *exp01Stream) applyCorrectionCommitted(e exp01Event, step exp01Step) exp01Step {
	f := s.f
	step.ProductAction = "review_approve_replace_old"
	out, ok := s.conflict[exp01Key(*e.Target)]
	old, ok2 := s.lookup(*e.Target)
	if !ok || !ok2 {
		return exp01Cascade(step, "no_pending_conflict")
	}
	var cand Record
	oldRef := Ref{Collection: colMemories, ID: old.ID}
	for _, row := range out.Memories {
		if row.ID == old.ID {
			oldRef.Revision = row.Revision
		} else {
			cand = row
		}
	}
	if cand.ID == "" || oldRef.Revision == 0 {
		return exp01Cascade(step, "conflict_without_both_rows")
	}
	m, err := f.svc.ReviewCandidate(testCtx, f.human, cand.ID, CandidateReviewInput{Scope: s.scope, ExpectedRevision: cand.Revision, Action: "approve", OperationID: "exp01-" + e.EventID, Conflict: &CandidateReviewConflict{ID: out.Relation.ID, ExpectedRevision: out.Relation.Revision, Memory: oldRef, Resolution: "replace_old", Reason: "EXP-01 plan commit"}})
	if err != nil {
		return exp01Fail(step, err)
	}
	if err = s.bind(e.RelatedRefs[0], colMemories, m); err != nil {
		return exp01Cascade(step, "product_id_rebound")
	}
	return step
}

func (s *exp01Stream) applyDeleted(e exp01Event, step exp01Step) exp01Step {
	step.ProductAction = "delete"
	old, ok := s.lookup(*e.Target)
	if !ok {
		return exp01Cascade(step, "missing_prior_ref")
	}
	cur, err := s.current(old.ID)
	if err != nil {
		return exp01Fail(step, err)
	}
	if err = s.f.svc.Delete(testCtx, s.f.human, colMemories, cur.ID, ChangeInput{Scope: s.scope, ExpectedRevision: cur.Revision}); err != nil {
		return exp01Fail(step, err)
	}
	return step
}

func (s *exp01Stream) ask(q exp01Query) exp01Answer {
	a := exp01Answer{QueryID: q.QueryID, StreamID: q.StreamID, RequestMode: q.RequestMode, RequestedAsOfNS: q.RequestedAsOfNS, Search: "current", Items: []exp01Hit{}, History: []exp01Hit{}, Degraded: []string{}}
	in := SearchInput{Scope: s.scope, Query: q.QueryText, Modes: []string{"exact", "fts"}, Limit: 10, MaxChars: 4000}
	if q.RequestMode != "current" {
		// The product has no as-of search; the nearest call also returns inactive rows and history.
		a.Search = "include_inactive_no_as_of"
		in.IncludeInactive = true
	}
	out, err := s.f.svc.Search(testCtx, s.f.human, in)
	if err != nil {
		code, _ := errorResponse(err)
		a.Search = "refused:" + code
		return a
	}
	mapHits := func(hits []Hit, into *[]exp01Hit) {
		for _, h := range hits {
			b, ok := s.plan[h.Collection+":"+h.ID]
			got := exp01SHA([]byte(h.Text))
			switch {
			case !ok:
				a.Unmapped++
			case got != b.textSHA:
				a.ContentMismatch++
			default:
				*into = append(*into, exp01Hit{Plan: b.ref, ProductCollection: h.Collection, ProductID: h.ID, ProductRevision: h.Revision, ObservedTextSHA256: got})
			}
		}
	}
	mapHits(out.Items, &a.Items)
	hist := []Hit{}
	for _, h := range out.History {
		hist = append(hist, h.Hit)
	}
	mapHits(hist, &a.History)
	a.Degraded = append(a.Degraded, out.Degraded...)
	return a
}

func exp01Run(t *testing.T, admin *Postgres, events []exp01Event, queries []exp01Query) ([]exp01Step, []exp01Answer) {
	t.Helper()
	streams, order := exp01Streams(t, admin, events)
	steps, answers := []exp01Step{}, []exp01Answer{}
	for _, id := range order {
		st, an := streams[id].run(id, events, queries)
		steps, answers = append(steps, st...), append(answers, an...)
	}
	for _, q := range queries {
		if streams[q.StreamID] == nil {
			t.Fatal("query for a stream without events", q.QueryID)
		}
	}
	return steps, answers
}

// exp01Streams opens one product fixture per stream, in first-event order, and marks targets a correction will replace.
func exp01Streams(t *testing.T, admin *Postgres, events []exp01Event) (map[string]*exp01Stream, []string) {
	t.Helper()
	streams := map[string]*exp01Stream{}
	order := []string{}
	for _, e := range events {
		if streams[e.StreamID] == nil {
			f := newFixture(t)
			f.svc.Repo = admin
			streams[e.StreamID] = &exp01Stream{f: f, scope: Scope{WorkspaceID: "ws-exp01", ProjectID: "p-" + e.StreamID[:16]}, records: map[string]Record{}, text: map[string]string{}, plan: map[string]exp01Bound{}, awaitsCorrection: map[string]bool{}, conflict: map[string]ConflictOutput{}}
			order = append(order, e.StreamID)
		}
		if e.Kind == "correction_proposed" {
			streams[e.StreamID].awaitsCorrection[exp01Key(e.RelatedRefs[0])] = true
		}
	}
	return streams, order
}

// run applies one stream's events in plan order and asks each query once its cutoff is reached.
func (s *exp01Stream) run(id string, events []exp01Event, queries []exp01Query) ([]exp01Step, []exp01Answer) {
	mine := []exp01Event{}
	for _, e := range events {
		if e.StreamID == id {
			mine = append(mine, e)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].PlanSeq < mine[j].PlanSeq })
	qs := []exp01Query{}
	for _, q := range queries {
		if q.StreamID == id {
			qs = append(qs, q)
		}
	}
	sort.SliceStable(qs, func(i, j int) bool { return qs[i].CutoffPlanSeq < qs[j].CutoffPlanSeq })
	steps, answers, next := []exp01Step{}, []exp01Answer{}, 0
	for _, q := range qs {
		for next < len(mine) && mine[next].PlanSeq <= q.CutoffPlanSeq {
			steps = append(steps, s.apply(mine[next]))
			next++
		}
		answers = append(answers, s.ask(q))
	}
	for ; next < len(mine); next++ {
		steps = append(steps, s.apply(mine[next]))
	}
	return steps, answers
}

func exp01Write[T any](t *testing.T, path string, rows []T) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEXP01NativeLabelFreePG(t *testing.T) {
	in, out := os.Getenv("EXP01_NATIVE_INPUT"), os.Getenv("EXP01_NATIVE_OUT")
	if in == "" || out == "" {
		t.Skip("EXP01_NATIVE_INPUT and EXP01_NATIVE_OUT are not set; opt-in run")
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("output directory must not exist yet", out)
	}
	events, queries, sums, err := exp01Inputs(in)
	if err != nil {
		t.Fatal(err)
	}
	steps, answers := exp01Run(t, isolatedPostgres(t), events, queries)
	if len(answers) != len(queries) || len(steps) != len(events) {
		t.Fatalf("denominators: %d/%d answers, %d/%d steps", len(answers), len(queries), len(steps), len(events))
	}
	if err = os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	exp01Write(t, filepath.Join(out, "materialization.jsonl"), steps)
	exp01Write(t, filepath.Join(out, "answers.jsonl"), answers)
	exp01Write(t, filepath.Join(out, "manifest.json"), []any{map[string]any{
		"protocol": "exp01-native-labelfree-run-v2", "input_sha256": sums,
		"actors":      "agent token proposes sources and candidates; human token reviews, marks conflicts and deletes",
		"repository":  "disposable PostgreSQL through the admin connection; row-level security is not exercised",
		"clock":       "product wall clock; plan times are reported as dropped_fields, so as-of state is not reproduced",
		"scope":       "constant workspace, project = p- + first 16 chars of stream_id; plan user/agent scope dropped",
		"no_input":    "task_started, exposure_observed and outcome_observed are assumed to have no baseline search input",
		"waits":       "lookahead: a material revision named by a later correction_proposed in the same stream waits as a candidate until correction_committed",
		"generator":   "the product requires generator metadata on an agent candidate; the harness supplies a fixed exp01-plan placeholder",
		"search":      "exact+fts, limit 10; historical requests use include_inactive because the product has no as-of search",
		"hit_binding": "a hit counts only when its product collection:id is bound and its text sha256 equals the plan text sha256",
	}})
}

func TestEXP01InputsRefuseLabels(t *testing.T) {
	dir := t.TempDir()
	write := func(n, body string) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("events.jsonl", `{"event_id":"e1","stream_id":"s123456789012345678","plan_seq":1,"kind":"task_started"}`+"\n")
	write("queries.jsonl", `{"query_id":"q1","stream_id":"s123456789012345678","cutoff_plan_seq":1,"query_text":"x","request_mode":"current"}`+"\n")
	write("gold.jsonl", "")
	if exp01LabelFree(dir) == nil {
		t.Fatal("a directory with gold.jsonl was accepted")
	}
	if err := os.Remove(filepath.Join(dir, "gold.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, _, sums, err := exp01Inputs(dir); err != nil || len(sums) != 2 {
		t.Fatal("the two label-free files were refused", err)
	}
	write("queries.jsonl", `{"query_id":"q1","stream_id":"s123456789012345678","cutoff_plan_seq":1,"query_text":"x","request_mode":"current","required_refs":[]}`+"\n")
	if _, _, _, err := exp01Inputs(dir); err == nil {
		t.Fatal("a label field inside queries.jsonl was accepted")
	}
	write("events.jsonl", `{"event_id":"e1","stream_id":"s123456789012345678","plan_seq":1,"kind":"material_deleted"}`+"\n")
	write("queries.jsonl", "")
	if _, _, _, err := exp01Inputs(dir); err == nil {
		t.Fatal("a delete without target was accepted")
	}
	if err := os.Remove(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "queries.jsonl"), filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	if exp01LabelFree(dir) == nil {
		t.Fatal("a symlinked input was accepted")
	}
}
