package capture

import (
	"encoding/json"
	"fmt"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"io"
	"strings"
)

// HookWithReceiptChain runs the actual hook producer with an explicit local
// evidence collector. It never enables a collector through wire JSON or env.
func HookWithReceiptChain(data map[string]any, name string, out io.Writer, chain *receiptchain.Session) error {
	return HookWithReceiptChainView(data, name, out, chain, nil)
}

// HookWithReceiptChainView applies an explicit invocation-only selector even
// when collection is off or its identity is unavailable. A selector error
// never retries through an unmasked search path.
func HookWithReceiptChainView(data map[string]any, name string, out io.Writer, chain *receiptchain.Session, view *ContextViewRecall) error {
	frozen, e := freezeRecallView(view, name)
	if e != nil {
		return e
	}
	view = frozen

	accepted := false
	if chain != nil && chain.IsFrozen() {
		chain.Note("cycle_closed")
		chain = nil
	}
	if chain != nil {
		raw, _ := json.Marshal(capScope())
		var scope receiptchain.Scope
		_ = json.Unmarshal(raw, &scope)
		sid, _ := data["session_id"].(string)
		if scope != chain.Scope() || chain.ReaderSHA256() != ReceiptReaderIdentity(capOrigin, sid) || chain.RunRef() != nil {
			chain.Note("identity_mismatch")
			chain = nil
		} else if name != "UserPromptSubmit" {
			chain.Note("unsupported_path")
			chain = nil
		}
	}
	done := chain.BeginBoundary()
	defer done()
	return hookInjectWithReceiptView(data, name, out, &accepted, chain, view)
}

type receiptPromptFacts struct {
	chain     *receiptchain.Session
	returns   []receiptchain.Record
	selected  *receiptchain.Record
	responses map[string]receiptchain.Record
	items     map[string]receiptchain.Source
	delivery  *promptDelivery
}

// ReceiptAcknowledgement is explicit transport evidence from the reader.
// It cannot assert semantic memory use, task success or causal contribution.
type ReceiptAcknowledgement struct {
	CycleID, ReaderSHA256, InputSHA256 string
	Answer                             []byte
	Reads                              []ReceiptSourceRead
}
type ReceiptReader interface {
	ReceiptAcknowledgement(string) (ReceiptAcknowledgement, error)
}

func hookPromptPartObserved(data map[string]any, sid string, chain *receiptchain.Session) (host.Part, *promptDelivery, error) {
	return hookPromptPartWithView(data, sid, chain, nil)
}
func hookPromptPartWithView(data map[string]any, sid string, chain *receiptchain.Session, view *ContextViewRecall) (host.Part, *promptDelivery, error) {
	if chain == nil && view == nil {
		return hookPromptPart(data, sid)
	}
	prompt, ok := or(data["prompt"], "").(string)
	if !ok {
		return host.Part{}, nil, fmt.Errorf("%w: expected string", errType)
	}
	facts := &receiptPromptFacts{chain: chain, responses: map[string]receiptchain.Record{}, items: map[string]receiptchain.Source{}}
	recall := func(query, project string, limit int, timeout float64) ([]any, error) {
		var observe searchResponseObserver
		if chain != nil {
			observe = facts.observeReturn
		}
		items, vector, e := recallWithView(query, project, limit, timeout, observe, view)
		if e != nil {
			return nil, e
		}
		kept, low := host.LoadRecallBudget().Floor(items, vector)
		budgetMetric("below_score", low)
		return kept, nil
	}
	part, delivery, e := promptPartsUsing(prompt, sid, str(or(data["cwd"], "")), recall)
	if delivery != nil && chain != nil {
		delivery.receipt = facts
		facts.delivery = delivery
		facts.prior()
	}
	return part, delivery, e
}
func chainSource(item map[string]any, scope receiptchain.Scope) (receiptchain.Source, bool) {
	raw, e := json.Marshal(item["scope"])
	if e != nil {
		return receiptchain.Source{}, false
	}
	var actual receiptchain.Scope
	if json.Unmarshal(raw, &actual) != nil || actual != scope {
		return receiptchain.Source{}, false
	}
	revision, ok := host.Int(item["revision"])
	id, _ := item["id"].(string)
	if !ok || revision < 1 || id == "" {
		return receiptchain.Source{}, false
	}
	var content *string
	if text, exists := item["text"].(string); exists {
		hash := receiptchain.Hash([]byte(text))
		content = &hash
	}
	body, e := json.Marshal(item)
	if e != nil {
		return receiptchain.Source{}, false
	}
	provenance := receiptchain.Hash(body)
	return receiptchain.Source{Scope: scope, Ref: receiptchain.Ref{Collection: "memories", ID: id, Revision: revision}, ContentSHA256: content, ProvenanceSHA256: &provenance, Form: "unknown"}, true
}
func (f *receiptPromptFacts) observeReturn(response map[string]any, selected bool) {
	raw, e := json.Marshal(response)
	if e != nil {
		f.chain.Note("invalid_record")
		return
	}
	key := receiptchain.Hash(raw)
	record, exists := f.responses[key]
	if !exists {
		sources := []receiptchain.Source{}
		for _, item := range dicts(listOf(response["items"])) {
			source, ok := chainSource(item, f.chain.Scope())
			if !ok {
				f.chain.Note("invalid_source")
				continue
			}
			sources = append(sources, source)
		}
		contextID, _ := response["context_id"].(string)
		var context *string
		if contextID != "" {
			context = &contextID
		}
		record, e = f.chain.Record(receiptchain.Record{EventID: "return_" + key, Path: "prompt", Stage: "server_return", SourceRelation: "returned", ContextID: context, Sources: sources, BytesSHA256: &key, Status: "observed", Reason: "none"})
		if e != nil {
			f.chain.Note("invalid_record")
			return
		}
		f.responses[key] = record
		f.returns = append(f.returns, record)
	}
	if selected {
		selectedRecord := record
		f.selected = &selectedRecord
		f.items = map[string]receiptchain.Source{}
		for _, source := range record.Sources {
			f.items[source.Ref.ID] = source
		}
	}
}
func writeHookReceiptOutput(out io.Writer, name any, text string, delivery *promptDelivery, chain *receiptchain.Session) error {
	if text == "" {
		return nil
	}
	wire := host.MustDumps(host.Obj{{Key: "hookSpecificOutput", Value: host.Obj{{Key: "hookEventName", Value: name}, {Key: "additionalContext", Value: text}}}}, host.DumpOpts{NoASCII: true}) + "\n"
	var render *receiptchain.Record
	if chain != nil && delivery != nil && delivery.receipt != nil {
		record, e := delivery.receipt.render(text, []byte(wire))
		if e != nil {
			chain.Note("invalid_record")
		} else {
			render = &record
		}
	}
	n, e := io.WriteString(out, wire)
	if e != nil {
		return e
	}
	if n != len(wire) {
		if chain != nil {
			chain.Note("partial_write")
		}
		return io.ErrShortWrite
	}
	if render != nil {
		accepted, e := chain.Record(receiptchain.Record{EventID: "transport", Path: "prompt", Stage: "transport_accepted", SourceRelation: "included", Parents: []string{render.RecordSHA256}, Sources: render.Sources, BytesSHA256: render.BytesSHA256, Status: "observed", Reason: "none"})
		if e != nil {
			chain.Note("invalid_record")
		} else {
			observeReceiptReader(out, chain, accepted)
		}
	}
	return nil
}
func (f *receiptPromptFacts) render(text string, wire []byte) (receiptchain.Record, error) {
	if f.selected == nil {
		return receiptchain.Record{}, receiptchain.ErrIncomplete
	}
	sources := []receiptchain.Source{}
	form := ""
	for i, p := range []string{f.delivery.Part.Full, f.delivery.Part.Meta} {
		if p != "" && (text == p || strings.HasPrefix(text, p+"\n\n")) {
			form = []string{"full", "meta"}[i]
			break
		}
	}
	if form != "" {
		for _, item := range f.delivery.Items {
			id, _ := item["id"].(string)
			source, ok := f.items[id]
			revision, valid := host.Int(item["revision"])
			if !ok || !valid || source.Ref.Revision != revision {
				f.chain.Note("invalid_source")
				return receiptchain.Record{}, receiptchain.ErrIncomplete
			}
			source.Form = form
			sources = append(sources, source)
		}
	}
	hash := receiptchain.Hash(wire)
	return f.chain.Record(receiptchain.Record{EventID: "render", Path: "prompt", Stage: "final_render", SourceRelation: "included", Parents: []string{f.selected.RecordSHA256}, Sources: sources, BytesSHA256: &hash, Status: "observed", Reason: "none"})
}
func observeReceiptReader(out io.Writer, chain *receiptchain.Session, accepted receiptchain.Record) {
	reader, ok := out.(ReceiptReader)
	if !ok {
		return
	}
	ack, e := callReceiptReader(reader, chain.CycleID())
	if e != nil {
		chain.Note("reader_failed")
		return
	}
	if ack.CycleID != chain.CycleID() || ack.ReaderSHA256 != chain.ReaderSHA256() || accepted.BytesSHA256 == nil || ack.InputSHA256 != *accepted.BytesSHA256 {
		chain.Note("reader_mismatch")
		return
	}
	read, e := chain.Record(receiptchain.Record{EventID: "reader", Path: "prompt", Stage: "reader_bytes_acknowledged", SourceRelation: "included", Parents: []string{accepted.RecordSHA256}, Sources: accepted.Sources, BytesSHA256: &ack.InputSHA256, Status: "observed", Reason: "none"})
	if e != nil {
		chain.Note("invalid_record")
		return
	}
	answerParent := observeReceiptSourceReads(chain, read, ack.Reads)
	if ack.Answer != nil {
		hash := receiptchain.Hash(ack.Answer)
		if _, e = chain.Record(receiptchain.Record{EventID: "answer", Path: "prompt", Stage: "answer_observed", SourceRelation: "lineage_only", Parents: []string{answerParent.RecordSHA256}, Sources: read.Sources, BytesSHA256: &hash, Status: "observed", Reason: "none"}); e != nil {
			chain.Note("invalid_record")
		}
	}
}

func (f *receiptPromptFacts) prior() {
	sources := []receiptchain.Source{}
	for _, old := range f.delivery.Previous {
		scope := receiptchain.Scope{WorkspaceID: old.Scope["workspace_id"], ProjectID: old.Scope["project_id"], UserID: old.Scope["user_id"], AgentID: old.Scope["agent_id"]}
		if scope != f.chain.Scope() {
			f.chain.Note("invalid_source")
			continue
		}
		sources = append(sources, receiptchain.Source{Scope: scope, Ref: receiptchain.Ref{Collection: old.Collection, ID: old.ID, Revision: old.Revision}, Form: old.Form})
	}
	if len(sources) > 0 {
		if _, e := f.chain.Record(receiptchain.Record{EventID: "prior", Path: "prompt", Stage: "prior_path", SourceRelation: "lineage_only", Sources: sources, Status: "observed", Reason: "prior_path"}); e != nil {
			f.chain.Note("invalid_record")
		}
	}
}

func ReceiptReaderIdentity(origin, sid string) string {
	raw, _ := json.Marshal([]string{origin, sid})
	return receiptchain.Hash(raw)
}

// ReceiptSourceRead is an observed reader HTTP-body read, not semantic use.
// Path prompt binds only refs in that rendered input; direct is a separate path.
type ReceiptSourceRead struct {
	Path           string
	Source         receiptchain.Source
	ResponseSHA256 string
	ObservedAtNS   *int64
}

func observeReceiptSourceReads(chain *receiptchain.Session, reader receiptchain.Record, reads []ReceiptSourceRead) receiptchain.Record {
	parent := reader
	for i, read := range reads {
		if read.Source.Scope != chain.Scope() {
			chain.Note("reader_source_changed")
			continue
		}
		path := "prompt"
		proof := reader
		if read.Path == "direct" {
			path = "reader_direct"
			record, e := chain.Record(receiptchain.Record{EventID: fmt.Sprintf("direct_return_%d", i), Path: path, Stage: "server_return", SourceRelation: "returned", Sources: []receiptchain.Source{read.Source}, BytesSHA256: &read.ResponseSHA256, ObservedAtNS: read.ObservedAtNS, Status: "observed", Reason: "none"})
			if e != nil {
				chain.Note("invalid_record")
				continue
			}
			proof = record
		} else if read.Path != "prompt" {
			chain.Note("unsupported_path")
			continue
		}
		record, e := chain.Record(receiptchain.Record{EventID: fmt.Sprintf("source_read_%d", i), Path: path, Stage: "source_read", SourceRelation: "read", Parents: []string{proof.RecordSHA256}, Sources: []receiptchain.Source{read.Source}, BytesSHA256: &read.ResponseSHA256, ObservedAtNS: read.ObservedAtNS, Status: "observed", Reason: "none"})
		if e != nil {
			chain.Note("reader_source_changed")
			continue
		}
		parent = record
	}
	return parent
}

func callReceiptReader(reader ReceiptReader, cycle string) (ack ReceiptAcknowledgement, err error) {
	defer func() {
		if recover() != nil {
			err = receiptchain.ErrIncomplete
		}
	}()
	return reader.ReceiptAcknowledgement(cycle)
}

type searchResponseObserver func(map[string]any, bool)

func (o searchResponseObserver) emit(response map[string]any, selected bool) {
	if o != nil {
		o(response, selected)
	}
}

func hookPromptPartSelected(data map[string]any, sid string, chain *receiptchain.Session, view *ContextViewRecall) (host.Part, *promptDelivery, error) {
	if view == nil {
		return hookPromptPartObserved(data, sid, chain)
	}
	return hookPromptPartWithView(data, sid, chain, view)
}
