package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"io"
	"strings"
	"testing"
	"time"
)

type independentReceiptReader struct {
	bytes.Buffer
	reader string
	wrong  bool
}

func (r *independentReceiptReader) ReceiptAcknowledgement(cycle string) (ReceiptAcknowledgement, error) {
	var envelope map[string]any
	if json.Unmarshal(r.Bytes(), &envelope) != nil {
		return ReceiptAcknowledgement{}, errors.New("invalid envelope")
	}
	if r.wrong {
		cycle = "old-cycle"
	}
	return ReceiptAcknowledgement{CycleID: cycle, ReaderSHA256: r.reader, InputSHA256: receiptchain.Hash(r.Bytes()), Answer: []byte("independently observed local answer")}, nil
}

type partialReceiptWriter struct{}

func (partialReceiptWriter) Write(b []byte) (int, error) { return len(b) / 2, nil }
func receiptHookFixture(t *testing.T) (receiptchain.Scope, []any) {
	t.Helper()
	capFake(t)
	hookEnv(t)
	oldScope := capScope
	t.Cleanup(func() { capScope = oldScope })
	capOrigin = "fixture_host"
	scope := receiptchain.Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u"}
	capScope = func() map[string]any {
		return map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u", "agent_id": ""}
	}
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "0")
	t.Setenv("ARKISM_RECALL_BUDGET", "user=2:1500")
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_REPO", "")
	t.Setenv("ARKISM_MEMORY_OUTCOME_SPOOL", "")
	items := []any{}
	for _, id := range []string{"chain-a", "chain-b", "chain-excluded"} {
		m := memoryHit(id, 1, "")
		m["kind"] = "user"
		m["text"] = "BODY_CANARY " + id
		items = append(items, m)
	}
	capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/search" {
			t.Fatal(path)
		}
		return map[string]any{"context_id": "server-context-one", "items": items}, nil
	}
	return scope, items
}
func receiptHookSession(t *testing.T, scope receiptchain.Scope, sid string) *receiptchain.Session {
	t.Helper()
	s, e := receiptchain.New(scope, ReceiptReaderIdentity(capOrigin, sid), nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestReceiptChainActualHookReader(t *testing.T) {
	scope, _ := receiptHookFixture(t)
	s := receiptHookSession(t, scope, "reader")
	r := &independentReceiptReader{reader: s.ReaderSHA256()}
	if e := HookWithReceiptChain(map[string]any{"session_id": "reader", "prompt": "기억 전달 경계를 검사합니다."}, "UserPromptSubmit", r, s); e != nil {
		t.Fatal(e)
	}
	snap := s.Snapshot()
	if !snap.Complete || len(snap.Records) != 5 {
		t.Fatal(snap.Issues, len(snap.Records))
	}
	for _, record := range snap.Records {
		assertReceiptHookRecord(t, record)
	}
	raw, _ := json.Marshal(snap)
	if strings.Contains(string(raw), "BODY_CANARY") || strings.Contains(string(raw), "기억 전달") || strings.Contains(string(raw), "independently observed local answer") {
		t.Fatal("raw telemetry")
	}
}
func TestReceiptChainActualHookPartialFailure(t *testing.T) {
	scope, _ := receiptHookFixture(t)
	s := receiptHookSession(t, scope, "partial")
	e := HookWithReceiptChain(map[string]any{"session_id": "partial", "prompt": "기억 전달 경계를 검사합니다."}, "UserPromptSubmit", partialReceiptWriter{}, s)
	if !errors.Is(e, io.ErrShortWrite) {
		t.Fatal(e)
	}
	for _, r := range s.Snapshot().Records {
		if r.Stage == "transport_accepted" || r.Stage == "reader_bytes_acknowledged" {
			t.Fatal("partial output acknowledged")
		}
	}
	if len(sessionStore().Delivered("partial")) != 0 || s.Snapshot().Complete {
		t.Fatal("partial output marked shown")
	}
}
func TestReceiptChainActualHookNewCycle(t *testing.T) {
	scope, _ := receiptHookFixture(t)
	first := receiptHookSession(t, scope, "same-reader")
	var out bytes.Buffer
	if e := HookWithReceiptChain(map[string]any{"session_id": "same-reader", "prompt": "기억 전달 경계를 검사합니다."}, "UserPromptSubmit", &out, first); e != nil {
		t.Fatal(e)
	}
	second := receiptHookSession(t, scope, "same-reader")
	out.Reset()
	if e := HookWithReceiptChain(map[string]any{"session_id": "same-reader", "prompt": "기억 전달 경계를 다시 검사합니다."}, "UserPromptSubmit", &out, second); e != nil {
		t.Fatal(e)
	}
	if first.CycleID() == second.CycleID() {
		t.Fatal("cycle reused")
	}
	prior := false
	for _, r := range second.Snapshot().Records {
		if r.Stage == "prior_path" {
			prior = true
		}
		if r.Stage == "transport_accepted" {
			for _, source := range r.Sources {
				if source.Ref.ID == "chain-a" || source.Ref.ID == "chain-b" {
					t.Fatal("old receipt became new delivery")
				}
			}
		}
	}
	if !prior {
		t.Fatal("persistent path not identified")
	}
}
func TestReceiptChainActualHookWrongReader(t *testing.T) {
	scope, _ := receiptHookFixture(t)
	s := receiptHookSession(t, scope, "reader")
	r := &independentReceiptReader{reader: s.ReaderSHA256(), wrong: true}
	if e := HookWithReceiptChain(map[string]any{"session_id": "reader", "prompt": "기억 전달 경계를 검사합니다."}, "UserPromptSubmit", r, s); e != nil {
		t.Fatal(e)
	}
	for _, record := range s.Snapshot().Records {
		if record.Stage == "reader_bytes_acknowledged" || record.Stage == "answer_observed" {
			t.Fatal("old reader ack accepted")
		}
	}
	if s.Snapshot().Complete {
		t.Fatal("missing reader evidence hidden")
	}
}

func assertReceiptHookRecord(t *testing.T, record receiptchain.Record) {
	t.Helper()
	if record.Stage == "server_return" {
		if len(record.Sources) != 3 || record.ContextID == nil {
			t.Fatal("server return lost")
		}
		return
	}
	if len(record.Sources) != 2 {
		t.Fatal(record.Stage, len(record.Sources))
	}
	for _, source := range record.Sources {
		if source.Ref.ID == "chain-excluded" {
			t.Fatal("packing exclusion lost")
		}
	}
	if record.ActualUse != nil || record.TaskSuccess != nil || record.CausalContribution != nil {
		t.Fatal("transport became success")
	}
}
