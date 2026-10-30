package crewrun

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"net/url"
	"reflect"
	"time"
)

// ChainPublication acknowledges separate canonical storage/readback/spooling.
// A trace's required partial outcome finalizes this transport ledger only; it
// is not the original task result. The metadata payload contains no retrieved
// claim and grants no memory-outcome qualification.
type ChainPublication struct {
	Protocol     string         `json:"protocol"`
	CycleID      string         `json:"cycle_id"`
	BundleSHA256 string         `json:"bundle_sha256"`
	Stored       string         `json:"canonical_receipt_stored"`
	Readback     string         `json:"canonical_receipt_readback"`
	Spooled      string         `json:"reference_spooled"`
	Receipts     []TraceReceipt `json:"receipts"`
}

// PublishReceiptChain uses the existing authenticated trace API and reference
// spool. Errors return partial acknowledgements, never a fabricated completion.
func (c *Client) PublishReceiptChain(ctx context.Context, session *receiptchain.Session, spool, agentID string) (ChainPublication, error) {
	out := ChainPublication{Protocol: receiptchain.Protocol, CycleID: session.CycleID(), Stored: "unknown", Readback: "unknown", Spooled: "unknown", Receipts: []TraceReceipt{}}
	if session == nil || c == nil {
		return out, receiptchain.ErrIncomplete
	}
	snap := session.Freeze()
	if !snap.Complete || len(snap.Records) == 0 {
		return out, receiptchain.ErrIncomplete
	}
	scope := receiptchain.Scope(c.Scope)
	if scope != session.Scope() {
		return out, receiptchain.ErrInvalid
	}
	raw, e := json.Marshal(snap.Records)
	if e != nil {
		return out, receiptchain.ErrInvalid
	}
	out.BundleSHA256 = receiptchain.Hash(raw)
	for start := 0; start < len(snap.Records); start += 64 {
		records := snap.Records[start:min(start+64, len(snap.Records))]
		receipt, e := c.publishReceiptChainPage(ctx, session.CycleID(), out.BundleSHA256, start/64, records, agentID, len(snap.Records))
		if e != nil {
			return out, e
		}
		out.Receipts = append(out.Receipts, receipt)
	}
	out.Stored = "observed"
	for i, receipt := range out.Receipts {
		start := i * 64
		records := snap.Records[start:min(start+64, len(snap.Records))]
		if e := c.readReceiptChainPage(ctx, receipt, records, out.BundleSHA256, i, len(snap.Records)); e != nil {
			return out, e
		}
	}
	out.Readback = "observed"
	if spool == "" {
		return out, nil
	}
	for _, receipt := range out.Receipts {
		if e := EnqueueReceipt(spool, receipt); e != nil {
			return out, e
		}
	}
	out.Spooled = "observed"
	return out, nil
}
func (c *Client) publishReceiptChainPage(ctx context.Context, cycle, bundle string, page int, records []receiptchain.Record, agentID string, total int) (TraceReceipt, error) {
	id := "context-chain-" + shortHash(cycle, records[0].ReaderSHA256, fmt.Sprint(page))
	events := []any{}
	var first, last time.Time
	for i, record := range records {
		if e := receiptchain.Validate(record); e != nil {
			return TraceReceipt{}, e
		}
		if record.ObservedAtNS == nil {
			return TraceReceipt{}, receiptchain.ErrIncomplete
		}
		at := time.Unix(0, *record.ObservedAtNS).UTC().Truncate(time.Millisecond)
		if i == 0 || at.Before(first) {
			first = at
		}
		if at.After(last) {
			last = at
		}
		payload, e := json.Marshal(receiptchain.StorageEnvelope{Protocol: receiptchain.StorageProtocol, Record: record, BundleSHA256: bundle, Page: int64(page), PageCount: int64((total + 63) / 64), RecordCount: int64(total)})
		if e != nil || len(payload) > 64<<10 {
			return TraceReceipt{}, receiptchain.ErrInvalid
		}
		events = append(events, map[string]any{"id": "context-chain-event-" + record.RecordSHA256, "kind": "context", "occurred_at": at, "payload": json.RawMessage(payload)})
	}
	if agentID == "" || c.Scope.AgentID != "" && c.Scope.AgentID != agentID {
		return TraceReceipt{}, receiptchain.ErrIncomplete
	}
	body := map[string]any{"scope": scopeMap(c.Scope), "trace_id": id, "thread_id": "context-cycle-" + cycle, "agent_run": map[string]any{"agent_id": agentID, "task_type": "context_receipt_chain", "outcome": "partial", "started_at": first, "finished_at": last, "text": "context receipt transport ledger"}, "events": events}
	var stored struct {
		Trace        Ref   `json:"trace"`
		Run          Ref   `json:"run"`
		SourceEvents []Ref `json:"source_events"`
	}
	if e := c.do(ctx, "POST", "/v1/execution_traces/completed", body, &stored); e != nil {
		return TraceReceipt{}, e
	}
	if e := validReceiptChainAcknowledgement(stored.Trace, stored.Run, stored.SourceEvents, id, len(records)); e != nil {
		return TraceReceipt{}, e
	}
	return TraceReceipt{Scope: scopeMap(c.Scope), SessionID: "context-cycle-" + cycle, Trace: stored.Trace, Run: stored.Run, SourceEvents: stored.SourceEvents, OccurredAt: last.Format(time.RFC3339Nano)}, nil
}
func (c *Client) readReceiptChainPage(ctx context.Context, receipt TraceReceipt, records []receiptchain.Record, bundle string, page, total int) error {
	var trace struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Scope    Scope  `json:"scope"`
		Sources  []struct {
			SourceID string `json:"source_id"`
		} `json:"sources"`
		Status        string `json:"status"`
		ContentErased bool   `json:"content_erased"`
	}
	if e := c.do(ctx, "GET", "/v1/execution_traces/"+url.PathEscape(receipt.Trace.ID)+"?"+c.query(nil), nil, &trace); e != nil {
		return e
	}
	if trace.ID != receipt.Trace.ID || trace.Revision != receipt.Trace.Revision || trace.Scope != c.Scope || trace.Status != "active" || trace.ContentErased || len(trace.Sources) != len(records) {
		return receiptchain.ErrIncomplete
	}
	if e := c.readReceiptChainRun(ctx, receipt); e != nil {
		return e
	}
	for i, ref := range receipt.SourceEvents {
		if trace.Sources[i].SourceID != ref.ID {
			return receiptchain.ErrIncomplete
		}
		if e := c.readReceiptChainSource(ctx, receipt, ref, records[i], bundle, page, total); e != nil {
			return e
		}
	}
	return nil
}

func (c *Client) readReceiptChainRun(ctx context.Context, receipt TraceReceipt) error {
	var run struct {
		ID            string `json:"id"`
		Revision      int64  `json:"revision"`
		Scope         Scope  `json:"scope"`
		Status        string `json:"status"`
		ContentErased bool   `json:"content_erased"`
		Run           *struct {
			TaskType string `json:"task_type"`
			Outcome  string `json:"outcome"`
		} `json:"run"`
		Relations []Ref `json:"relations"`
	}
	if e := c.do(ctx, "GET", "/v1/agent_runs/"+url.PathEscape(receipt.Run.ID)+"?"+c.query(nil), nil, &run); e != nil {
		return e
	}
	if run.ID != receipt.Run.ID || run.Revision != receipt.Run.Revision || run.Scope != c.Scope || run.Status != "active" || run.ContentErased || run.Run == nil || run.Run.TaskType != "context_receipt_chain" || run.Run.Outcome != "partial" {
		return receiptchain.ErrIncomplete
	}
	related := false
	for _, ref := range run.Relations {
		related = related || ref == receipt.Trace
	}
	if !related {
		return receiptchain.ErrIncomplete
	}
	return nil
}

func validReceiptChainAcknowledgement(trace, run Ref, sources []Ref, id string, count int) error {
	if trace.Collection != "execution_traces" || trace.ID != id || trace.Revision < 1 || run.Collection != "agent_runs" || run.ID == "" || run.Revision < 1 || len(sources) != count {
		return receiptchain.ErrIncomplete
	}
	for _, ref := range sources {
		if ref.Collection != "sources" || ref.ID == "" || ref.Revision < 1 {
			return receiptchain.ErrIncomplete
		}
	}
	return nil
}

func (c *Client) readReceiptChainSource(ctx context.Context, receipt TraceReceipt, ref Ref, record receiptchain.Record, bundle string, page, total int) error {
	var source struct {
		ID            string `json:"id"`
		Revision      int64  `json:"revision"`
		Scope         Scope  `json:"scope"`
		TraceID       string `json:"trace_id"`
		Text          string `json:"text"`
		Status        string `json:"status"`
		ContentErased bool   `json:"content_erased"`
	}
	if e := c.do(ctx, "GET", "/v1/sources/"+url.PathEscape(ref.ID)+"?"+c.query(nil), nil, &source); e != nil {
		return e
	}
	if source.ID != ref.ID || source.Revision != ref.Revision || source.Scope != c.Scope || source.TraceID != receipt.Trace.ID || source.Status != "active" || source.ContentErased {
		return receiptchain.ErrIncomplete
	}
	payload, e := receiptchain.DecodeStorageEnvelope([]byte(source.Text))
	if e != nil || payload.BundleSHA256 != bundle || payload.Page != int64(page) || payload.PageCount != int64((total+63)/64) || payload.RecordCount != int64(total) || !reflect.DeepEqual(payload.Record, record) {
		return receiptchain.ErrIncomplete
	}
	return nil
}
