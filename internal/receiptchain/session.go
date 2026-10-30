package receiptchain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"sync"
	"time"
)

// Session is an invocation-local evidence collector, never an authorizer.
// Nil sessions are off; a failed sink marks incompleteness without creating an acknowledgement.
type Session struct {
	pending int
	frozen  bool
	mu      sync.Mutex
	scope   Scope
	reader  string
	cycle   string
	run     *Ref
	records []Record
	events  map[string]Record
	sink    func(Record) error
	issues  map[string]bool
}
type Snapshot struct {
	Records  []Record `json:"records"`
	Complete bool     `json:"collection_complete"`
	Issues   []string `json:"issues"`
}

func New(scope Scope, readerSHA string, run *Ref, sink func(Record) error) (*Session, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return nil, e
	}
	return NewCycle(scope, readerSHA, run, hex.EncodeToString(b[:]), sink)
}
func NewCycle(scope Scope, readerSHA string, run *Ref, cycle string, sink func(Record) error) (*Session, error) {
	if !ValidScope(scope) || !validSHA(readerSHA) || !label(cycle, 128) || run != nil && !validRef(*run) {
		return nil, ErrInvalid
	}
	if run != nil {
		v := *run
		run = &v
	}
	return &Session{scope: scope, reader: readerSHA, cycle: cycle, run: run, records: []Record{}, events: map[string]Record{}, sink: sink, issues: map[string]bool{}}, nil
}
func clone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func (s *Session) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{Records: []Record{}, Complete: false, Issues: []string{"off"}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	issues := []string{}
	for k := range s.issues {
		issues = append(issues, k)
	}
	sort.Strings(issues)
	return Snapshot{clone(s.records), len(issues) == 0 && s.pending == 0, issues}
}

func (s *Session) CycleID() string {
	if s == nil {
		return ""
	}
	return s.cycle
}
func (s *Session) Scope() Scope {
	if s == nil {
		return Scope{}
	}
	return s.scope
}
func (s *Session) Note(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !issueKnown(reason) {
		reason = "invalid_issue"
	}
	s.issues[reason] = true
}

// Record fills only collector identity and local ordinal. Facts supplied by a
// producer remain observations, not a claim of server acceptance or learning.
func (s *Session) Record(r Record) (Record, error) {
	if s == nil {
		return Record{}, nil
	}
	s.mu.Lock()
	sealed, fresh, e := s.recordLocked(r)
	if fresh && s.sink != nil {
		s.pending++
	}
	s.mu.Unlock()
	if e != nil || !fresh || s.sink == nil {
		return sealed, e
	}
	e = receiptSink(s.sink, clone(sealed))
	s.mu.Lock()
	s.pending--
	if e != nil {
		s.issues["sink_failed"] = true
	}
	s.mu.Unlock()
	return sealed, e
}
func receiptSink(sink func(Record) error, r Record) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrIncomplete
		}
	}()
	return sink(r)
}
func (s *Session) recordLocked(r Record) (Record, bool, error) {
	r = s.prepareRecord(r)
	if e := validRecord(r); e != nil {
		return Record{}, false, e
	}
	r = clone(r)
	if old, ok := s.events[r.EventID]; ok {
		r.Sequence = old.Sequence
		if r.ObservedAtNS == nil {
			r.ObservedAtNS = old.ObservedAtNS
		}
		sealed, e := Seal(r)
		if e != nil {
			return Record{}, false, e
		}
		if !reflect.DeepEqual(old, sealed) {
			s.issues["event_conflict"] = true
			return Record{}, false, ErrConflict
		}
		return clone(old), false, nil
	}
	if s.frozen {
		return Record{}, false, ErrConflict
	}
	if r.ObservedAtNS == nil {
		now := time.Now()
		ns := now.UnixNano()
		if time.Unix(0, ns).Equal(now) {
			r.ObservedAtNS = &ns
		}
	}
	if e := s.validateParents(r); e != nil {
		s.issues["missing_parent"] = true
		return Record{}, false, e
	}
	sealed, e := Seal(r)
	if e != nil {
		s.issues["invalid_record"] = true
		return Record{}, false, e
	}
	s.records = append(s.records, sealed)
	s.events[r.EventID] = sealed
	return clone(sealed), true, nil
}

func (s *Session) validateParents(r Record) error {
	parents := []Record{}
	for _, sha := range r.Parents {
		found := false
		for _, p := range s.records {
			if p.RecordSHA256 == sha {
				parents = append(parents, p)
				found = true
				break
			}
		}
		if !found {
			return ErrIncomplete
		}
	}
	if r.Stage == "server_return" || r.Stage == "prior_path" {
		if len(parents) != 0 {
			return ErrIncomplete
		}
		return nil
	}
	if len(parents) == 0 {
		return ErrIncomplete
	}
	switch r.Stage {
	case "final_render":
		return renderParents(r, parents)
	case "transport_accepted":
		return exactInputParent(r, parents, "final_render")
	case "reader_bytes_acknowledged":
		return exactInputParent(r, parents, "transport_accepted")
	case "source_read":
		return sourceReadParent(r, parents)

	}
	return nil
}
func exactInputParent(r Record, p []Record, stage string) error {
	if len(p) != 1 || p[0].Stage != stage || p[0].Path != r.Path || p[0].BytesSHA256 == nil || r.BytesSHA256 == nil || *p[0].BytesSHA256 != *r.BytesSHA256 || p[0].SourceRelation != r.SourceRelation || !reflect.DeepEqual(p[0].Sources, r.Sources) {
		return ErrIncomplete
	}
	return nil
}
func renderParents(r Record, parents []Record) error {
	for _, want := range r.Sources {
		found := false
		for _, p := range parents {
			if p.Stage != "server_return" && p.Stage != "prior_path" {
				return ErrIncomplete
			}
			for _, source := range p.Sources {
				if source.Scope == want.Scope && source.Ref == want.Ref && reflect.DeepEqual(source.ContentSHA256, want.ContentSHA256) && reflect.DeepEqual(source.ProvenanceSHA256, want.ProvenanceSHA256) {
					found = true
					break
				}
			}
		}
		if !found {
			return ErrIncomplete
		}
	}
	return nil
}

func (s *Session) ReaderSHA256() string {
	if s == nil {
		return ""
	}
	return s.reader
}

func (s *Session) Freeze() Snapshot {
	if s == nil {
		return s.Snapshot()
	}
	s.mu.Lock()
	if s.pending == 0 {
		s.frozen = true
	}
	s.mu.Unlock()
	return s.Snapshot()
}

func (s *Session) RunRef() *Ref {
	if s == nil || s.run == nil {
		return nil
	}
	r := *s.run
	return &r
}

func issueKnown(s string) bool {
	switch s {
	case "event_conflict", "sink_failed", "missing_parent", "invalid_record", "invalid_source", "identity_mismatch", "unsupported_path", "reader_failed", "reader_mismatch", "partial_write", "adapter_output_unknown", "adapter_failed", "reader_unknown", "cycle_closed", "reader_source_changed", "answer_unknown", "invalid_issue":
		return true
	}
	return false
}

func (s *Session) BeginBoundary() func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.frozen {
		s.issues["cycle_closed"] = true
		s.mu.Unlock()
		return func() {}
	}
	s.pending++
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); s.pending--; s.mu.Unlock() }) }
}

func (s *Session) IsFrozen() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frozen
}

func sourceReadParent(r Record, p []Record) error {
	if len(p) != 1 || p[0].Path != r.Path || r.SourceRelation != "read" || r.BytesSHA256 == nil {
		return ErrIncomplete
	}
	if r.Path == "prompt" && p[0].Stage != "reader_bytes_acknowledged" {
		return ErrIncomplete
	}
	if r.Path == "reader_direct" && p[0].Stage != "server_return" {
		return ErrIncomplete
	}
	for _, want := range r.Sources {
		found := false
		for _, source := range p[0].Sources {
			if source.Scope == want.Scope && source.Ref == want.Ref {
				found = true
				break
			}
		}
		if !found {
			return ErrIncomplete
		}
	}
	return nil
}

func (s *Session) prepareRecord(r Record) Record {
	r.Protocol = Protocol
	r.CycleID = s.cycle
	r.Scope = s.scope
	r.ReaderSHA256 = s.reader
	r.Run = s.run
	r.Sequence = int64(len(s.records) + 1)
	if r.Parents == nil {
		r.Parents = []string{}
	}
	if r.Sources == nil {
		r.Sources = []Source{}
	}
	if r.SourceRelation == "" {
		r.SourceRelation = "unknown"
	}
	if old, exists := s.events[r.EventID]; exists {
		r.Sequence = old.Sequence
		if r.ObservedAtNS == nil {
			r.ObservedAtNS = old.ObservedAtNS
		}
	}
	return r
}
