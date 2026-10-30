package receiptchain

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixtureSession(t *testing.T) *Session {
	t.Helper()
	s, e := NewCycle(Scope{WorkspaceID: "w", ProjectID: "p"}, Hash([]byte("reader")), nil, "cycle", nil)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestReceiptChainCurrentCycleAndExclusion(t *testing.T) {
	s := fixtureSession(t)
	a := Source{Scope: s.Scope(), Ref: Ref{"memories", "included", 1}, Form: "full"}
	b := a
	b.Ref.ID = "excluded"
	ret, e := s.Record(Record{EventID: "return", Path: "prompt", Stage: "server_return", Sources: []Source{a, b}, Status: "observed", Reason: "none"})
	if e != nil {
		t.Fatal(e)
	}
	hash := Hash([]byte("actual rendered bytes"))
	render, e := s.Record(Record{EventID: "render", Path: "prompt", Stage: "final_render", Parents: []string{ret.RecordSHA256}, Sources: []Source{a}, BytesSHA256: &hash, Status: "observed", Reason: "none"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Record(Record{EventID: "smuggled", Path: "prompt", Stage: "transport_accepted", Parents: []string{render.RecordSHA256}, Sources: []Source{a, b}, BytesSHA256: &hash, Status: "observed", Reason: "none"})
	if e == nil {
		t.Fatal("packing excluded source acquired current-reader inclusion")
	}
	other := fixtureSession(t)
	if _, e = other.Record(Record{EventID: "old", Path: "prompt", Stage: "transport_accepted", Parents: []string{render.RecordSHA256}, Sources: []Source{a}, BytesSHA256: &hash, Status: "observed", Reason: "none"}); e == nil {
		t.Fatal("prior-cycle completion inherited")
	}
}
func TestReceiptChainStrict(t *testing.T) {
	s := fixtureSession(t)
	r, e := s.Record(Record{EventID: "one", Path: "prompt", Stage: "server_return", Status: "unknown", Reason: "unknown"})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(r)
	if _, e = Decode(raw); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"sequence":1`, `"sequence":1.0`, 1), strings.Replace(string(raw), `"sequence":1`, `"sequence":true`, 1), strings.Replace(string(raw), `"protocol":`, `"extra":1,"protocol":`, 1), strings.Replace(string(raw), `"protocol":`, `"sequence":1,"protocol":`, 1), strings.Replace(string(raw), `"actual_use":null,`, "", 1), strings.Replace(string(raw), `"actual_use":null`, `"actual_use":false`, 1)} {
		if _, e := Decode([]byte(bad)); e == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
func TestReceiptChainDuplicateAndImmutability(t *testing.T) {
	s := fixtureSession(t)
	r := Record{EventID: "one", Path: "prompt", Stage: "server_return", Status: "unknown", Reason: "unknown"}
	first, e := s.Record(r)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Record(r)
	if e != nil || first.RecordSHA256 != again.RecordSHA256 || len(s.Snapshot().Records) != 1 {
		t.Fatal("duplicate")
	}
	r.Status = "failed"
	if _, e = s.Record(r); e != ErrConflict {
		t.Fatal(e)
	}
	snap := s.Snapshot()
	snap.Records[0].EventID = "changed"
	if s.Snapshot().Records[0].EventID == "changed" {
		t.Fatal("aliased")
	}
}

func TestReceiptChainInvalidUTF8DoesNotNormalize(t *testing.T) {
	s := fixtureSession(t)
	_, e := s.Record(Record{EventID: "bad", Path: "prompt", Stage: "server_return", Sources: []Source{{Scope: s.Scope(), Ref: Ref{"memories", string([]byte{255}), 1}, Form: "full"}}, Status: "observed", Reason: "none"})
	if e == nil {
		t.Fatal("invalid UTF8 source normalized into accepted identity")
	}
}
func TestReceiptChainLoneSurrogateDoesNotNormalize(t *testing.T) {
	s := fixtureSession(t)
	r, e := s.Record(Record{EventID: "unicode", Path: "prompt", Stage: "server_return", Sources: []Source{{Scope: s.Scope(), Ref: Ref{"memories", "�", 1}, Form: "full"}}, Status: "observed", Reason: "none"})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(r)
	raw = []byte(strings.Replace(string(raw), `"id":"�"`, `"id":"\ud800"`, 1))
	if _, e = Decode(raw); e == nil {
		t.Fatal("unpaired surrogate normalized while retaining a valid hash")
	}
}
func TestReceiptChainControlFieldsAreClosed(t *testing.T) {
	for _, r := range []Record{{EventID: "bad", Path: "BODY_CANARY", Stage: "server_return", Status: "observed", Reason: "none"}, {EventID: "bad", Path: "prompt", Stage: "server_return", Status: "observed", Reason: "BODY_CANARY"}} {
		s := fixtureSession(t)
		if _, e := s.Record(r); e == nil {
			t.Fatal("raw control text accepted")
		}
	}
}

func TestReceiptChainScopeNullDoesNotBecomeEmpty(t *testing.T) {
	s := fixtureSession(t)
	r, e := s.Record(Record{EventID: "scope", Path: "prompt", Stage: "server_return", Status: "unknown", Reason: "unknown"})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(r)
	bad := strings.Replace(string(raw), `"user_id":""`, `"user_id":null`, 1)
	if _, e = Decode([]byte(bad)); e == nil {
		t.Fatal("null scope coordinate became known empty")
	}
}

func TestReceiptChainPendingFreezeAndClosedRetry(t *testing.T) {
	s := fixtureSession(t)
	done := s.BeginBoundary()
	r := Record{EventID: "one", Path: "prompt", Stage: "server_return", Status: "unknown", Reason: "unknown"}
	first, e := s.Record(r)
	if e != nil {
		t.Fatal(e)
	}
	if s.Freeze().Complete || s.IsFrozen() {
		t.Fatal("in-flight boundary prematurely frozen")
	}
	done()
	if !s.Freeze().Complete || !s.IsFrozen() {
		t.Fatal("finished boundary not frozen")
	}
	again, e := s.Record(r)
	if e != nil || again.RecordSHA256 != first.RecordSHA256 {
		t.Fatal("closed exact retry failed", e)
	}
	r.EventID = "two"
	if _, e = s.Record(r); e != ErrConflict {
		t.Fatal("new event after freeze accepted", e)
	}
}
func TestReceiptChainRetryClockConflict(t *testing.T) {
	s := fixtureSession(t)
	at := int64(100)
	r := Record{EventID: "one", Path: "prompt", Stage: "server_return", ObservedAtNS: &at, Status: "unknown", Reason: "unknown"}
	if _, e := s.Record(r); e != nil {
		t.Fatal(e)
	}
	later := int64(101)
	r.ObservedAtNS = &later
	if _, e := s.Record(r); e != ErrConflict {
		t.Fatal("explicit original clock changed on retry", e)
	}
}

func TestReceiptChainMissingIsUnknown(t *testing.T) {
	s := fixtureSession(t)
	if _, e := s.Record(Record{EventID: "one", Path: "prompt", Stage: "server_return", Status: "observed", Reason: "none"}); e != nil {
		t.Fatal(e)
	}
	coverage := Assess(s.Snapshot())
	if coverage.ActualUse != nil || coverage.TaskSuccess != nil || coverage.CausalContribution != nil || len(coverage.Paths) != 1 {
		t.Fatal("missing evidence became verdict")
	}
	for _, stage := range coverage.Paths[0].Stages {
		if stage.Stage != "server_return" && stage.State != "unknown" {
			t.Fatal("missing stage became negative", stage)
		}
	}
}
