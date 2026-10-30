package crewrun

import (
	"context"
	"encoding/json"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"sync"
)

// ReceiptChainRuntime is optional in-process evidence collection. It neither
// chooses a model nor writes to a store. Zero/nil runtimes are off.
type ReceiptChainRuntime struct {
	Enabled   bool
	MaxCycles int
	mu        sync.Mutex
	pending   int
	cycles    []*receiptchain.Session
	issues    []string
}
type receiptChainRuntimeKey struct{}

func WithReceiptChainRuntime(ctx context.Context, runtime *ReceiptChainRuntime) context.Context {
	return context.WithValue(ctx, receiptChainRuntimeKey{}, runtime)
}
func (r *ReceiptChainRuntime) Sessions() []*receiptchain.Session {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*receiptchain.Session{}, r.cycles...)
}
func (r *ReceiptChainRuntime) Issues() []string {
	if r == nil {
		return []string{"off"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.issues...)
}
func (r *ReceiptChainRuntime) note(code string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.issues = append(r.issues, code)
}

type receiptChainAICall struct {
	session  *receiptchain.Session
	prior    receiptchain.Record
	render   receiptchain.Record
	inputSHA string
	done     func()
}

func beginReceiptChainAI(ctx context.Context, s *aiSpec) *receiptChainAICall {
	s.ReceiptChain = nil
	r, _ := ctx.Value(receiptChainRuntimeKey{}).(*ReceiptChainRuntime)
	if r == nil || !r.Enabled {
		return nil
	}
	r.mu.Lock()
	bounded := r.MaxCycles >= 1 && r.MaxCycles <= 256 && len(r.cycles)+r.pending < r.MaxCycles
	if bounded {
		r.pending++
	}
	r.mu.Unlock()
	if !bounded {
		r.note("cycle_bound_exceeded")
		return nil
	}
	actual, ok := AttemptContextFrom(ctx)
	reader, _ := ctx.Value(contextReceiptActorKey{}).(string)
	if !AttemptObserverEnabled(ctx) || !ok || validateObservationRun(actual.Run) != nil || reader == "" {
		r.releasePending()
		r.note("runtime_identity_unknown")
		return nil
	}
	readerRaw, _ := json.Marshal([]any{actual.Run.Scope, actual.Run.RunRef, reader})
	run := receiptchain.Ref{Collection: actual.Run.RunRef.Collection, ID: actual.Run.RunRef.ID, Revision: actual.Run.RunRef.Revision}
	session, e := receiptchain.New(receiptchain.Scope(actual.Run.Scope), receiptchain.Hash(readerRaw), &run, nil)
	if e != nil {
		r.releasePending()
		r.note("runtime_identity_unknown")
		return nil
	}
	done := session.BeginBoundary()
	r.mu.Lock()
	r.pending--
	r.cycles = append(r.cycles, session)
	r.mu.Unlock()
	sources := receiptChainPriorSources(ctx, actual)
	status, reason := "unknown", "source_unknown"
	if len(sources) > 0 {
		status, reason = "observed", "prior_path"
	}
	prior, e := session.Record(receiptchain.Record{EventID: "prior", Path: "crew_runner_input", Stage: "prior_path", Sources: sources, Status: status, Reason: reason})
	if e != nil {
		session.Note("invalid_record")
		done()
		return nil
	}
	s.ReceiptChain = &receiptchain.TransportRequest{Protocol: receiptchain.Protocol, CycleID: session.CycleID(), ReaderSHA256: session.ReaderSHA256()}
	return &receiptChainAICall{session: session, prior: prior, done: done}
}
func receiptChainPriorSources(ctx context.Context, actual AttemptContext) []receiptchain.Source {
	records := ContextReceiptSessionFrom(ctx).Snapshot()
	sources := []receiptchain.Source{}
	if !records.Complete {
		return sources
	}
	for i := len(records.Records) - 1; i >= 0; i-- {
		record := records.Records[i]
		if record.Run.Scope != actual.Run.Scope || record.Run.RunRef != actual.Run.RunRef || record.NodeID != actual.NodeID || record.NodeAttempt != actual.NodeAttempt {
			continue
		}
		for _, source := range record.Sources {
			if source.Scope != actual.Run.Scope || source.Revision == nil {
				continue
			}
			provenance := record.RecordSHA256
			sources = append(sources, receiptchain.Source{Scope: receiptchain.Scope(source.Scope), Ref: receiptchain.Ref{Collection: source.Collection, ID: source.ID, Revision: *source.Revision}, ContentSHA256: source.ContentSHA256, ProvenanceSHA256: &provenance, Form: "unknown"})
		}
		break
	}
	return sources
}
func (c *receiptChainAICall) input(raw []byte) {
	if c == nil {
		return
	}
	c.inputSHA = receiptchain.Hash(raw)
	record, e := c.session.Record(receiptchain.Record{EventID: "render", Path: "crew_runner_input", Stage: "final_render", Parents: []string{c.prior.RecordSHA256}, Sources: []receiptchain.Source{}, BytesSHA256: &c.inputSHA, Status: "observed", Reason: "none"})
	if e != nil {
		c.session.Note("invalid_record")
		return
	}
	c.render = record
}
func (c *receiptChainAICall) finish(raw []byte, runErr error) {
	if c == nil {
		return
	}
	defer c.done()
	if c.render.RecordSHA256 == "" {
		c.session.Note("invalid_record")
		return
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil {
		c.session.Note("adapter_output_unknown")
		return
	}
	ack, ackErr := receiptchain.DecodeResponseInputAcknowledgement(raw)
	valid := ackErr == nil && ack.CycleID == c.session.CycleID() && ack.ReaderSHA256 == c.session.ReaderSHA256() && ack.InputSHA256 == c.inputSHA
	if runErr != nil && !valid {
		c.session.Note("adapter_failed")
		return
	}
	accepted, e := c.session.Record(receiptchain.Record{EventID: "transport", Path: "crew_runner_input", Stage: "transport_accepted", Parents: []string{c.render.RecordSHA256}, Sources: []receiptchain.Source{}, BytesSHA256: &c.inputSHA, Status: "observed", Reason: "none"})
	if e != nil {
		c.session.Note("invalid_record")
		return
	}
	if !valid {
		c.session.Note("reader_unknown")
		return
	}
	read, e := c.session.Record(receiptchain.Record{EventID: "reader", Path: "crew_runner_input", Stage: "reader_bytes_acknowledged", Parents: []string{accepted.RecordSHA256}, Sources: []receiptchain.Source{}, BytesSHA256: &c.inputSHA, Status: "observed", Reason: "none"})
	if e != nil {
		c.session.Note("invalid_record")
		return
	}
	var answer string
	if json.Unmarshal(response["output"], &answer) != nil || string(response["output"]) == "null" {
		c.session.Note("answer_unknown")
		return
	}
	hash := receiptchain.Hash([]byte(answer))
	if _, e = c.session.Record(receiptchain.Record{EventID: "answer", Path: "crew_runner_input", Stage: "answer_observed", Parents: []string{read.RecordSHA256}, Sources: []receiptchain.Source{}, BytesSHA256: &hash, Status: "observed", Reason: "none"}); e != nil {
		c.session.Note("invalid_record")
	}
}

func (r *ReceiptChainRuntime) releasePending() { r.mu.Lock(); r.pending--; r.mu.Unlock() }
