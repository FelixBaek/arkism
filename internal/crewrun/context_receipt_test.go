package crewrun

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func contextReceiptSource() SourceObservation {
	return SourceObservation{Scope: ObservationScope{"ws", "proj", "usr", "agt"}, Collection: "memories", ID: "memory", Revision: observationInt(5), ContentSHA256: observationString(strings.Repeat("a", 64)), ReadAtNS: observationInt(100), ProvenanceStatus: "pinned", ReadAuthorization: "confirmed", Sharing: "team", ObservedAtNS: observationInt(100), Authority: "data_only_untrusted"}
}
func contextReceiptRecord(t *testing.T) ContextObservation {
	t.Helper()
	actual, _ := AttemptContextFrom(observerTestContext())
	value := ContextObservation{ContractID: CrewObservationContractID, Run: actual.Run, Task: actual.Task, NodeID: actual.NodeID, NodeAttempt: actual.NodeAttempt, ReadAtNS: 100, Sources: []SourceObservation{contextReceiptSource()}, ContextSHA256: observationString(strings.Repeat("b", 64)), ContextHashState: "recorded", ContextStage: "source_read", FreshnessAction: "continue"}
	sealed, err := SealContextObservation(value)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}
func contextReceiptInput() ContextUseInput {
	source := contextReceiptSource()
	key := sourceKey(source)
	team := ObservationRef{"crew", "definition", 3}
	return ContextUseInput{Mode: ContextReceiptEnforceCurrentUse, Source: key, TeamRef: team, Current: ContextReceiptRead{Observation: source, Facts: ContextUseFacts{Source: key, Ref: ObservationRef{"memories", "memory", 5}, ContentSHA256: *source.ContentSHA256, GuardSHA256: strings.Repeat("c", 64), ReadState: "confirmed", Lifecycle: "active", Sharing: "team", CheckedAtNS: observationInt(100), PolicyRef: &team, PolicyReadAtNS: observationInt(100), PolicyScope: key.Scope, PolicyState: "confirmed"}}, Premise: ContextPremiseUnchanged, DecisionAtNS: 100}
}
func TestContextReceiptCodecRoundTrip(t *testing.T) {
	record := contextReceiptRecord(t)
	for name, value := range map[string]any{"source": contextReceiptSource(), "context": record} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var got any
			if name == "source" {
				got, err = DecodeSourceObservation(raw)
			} else {
				got, err = DecodeContextObservation(raw)
			}
			if err != nil || !reflect.DeepEqual(value, got) {
				t.Fatalf("roundtrip: %v", err)
			}
			var object map[string]json.RawMessage
			if err = json.Unmarshal(raw, &object); err != nil || len(object) != 14 {
				t.Fatalf("closed14: %d %v", len(object), err)
			}
		})
	}
}
func TestContextReceiptStrictDecode(t *testing.T) {
	raw, _ := json.Marshal(contextReceiptSource())
	base := string(raw)
	cases := map[string]string{
		"unknown":       strings.Replace(base, `"id":"memory"`, `"id":"memory","secret_payload":"hidden"`, 1),
		"duplicate":     strings.Replace(base, `"id":"memory"`, `"id":"memory","id":"memory"`, 1),
		"missing":       strings.Replace(base, `"authority":"data_only_untrusted"`, `"removed":"data_only_untrusted"`, 1),
		"nullscope":     strings.Replace(base, `"scope":{`, `"scope":null,"unused":{`, 1),
		"nullid":        strings.Replace(base, `"id":"memory"`, `"id":null`, 1),
		"float":         strings.Replace(base, `"revision":5`, `"revision":5.0`, 1),
		"exponent":      strings.Replace(base, `"revision":5`, `"revision":5e0`, 1),
		"bool":          strings.Replace(base, `"revision":5`, `"revision":true`, 1),
		"overflow":      strings.Replace(base, `"revision":5`, `"revision":9223372036854775808`, 1),
		"zero":          strings.Replace(base, `"revision":5`, `"revision":0`, 1),
		"negativeclock": strings.Replace(base, `"read_at_ns":100`, `"read_at_ns":-1`, 1),
		"unpinned":      strings.Replace(base, `"revision":5`, `"revision":null`, 1),
		"future":        strings.Replace(base, `"available_at_ns":null`, `"available_at_ns":101`, 1),
		"authority":     strings.Replace(base, `data_only_untrusted`, `trusted`, 1),
		"badsha":        strings.Replace(base, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"trailing":      base + ` {}`,
		"surrogate":     strings.Replace(base, `"id":"memory"`, `"id":"\ud800"`, 1),
		"utf8":          strings.Replace(base, "memory", string([]byte{255}), 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeSourceObservation([]byte(input))
			if err == nil {
				t.Fatal("accepted invalid source")
			}
			if strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "secret_payload") {
				t.Fatal("raw leak")
			}
		})
	}
}
func TestContextReceiptSourceNullAndPrecision(t *testing.T) {
	source := contextReceiptSource()
	source.Revision = observationInt(9223372036854775807)
	source.ReadAtNS = observationInt(9007199254740993)
	raw, _ := json.Marshal(source)
	got, err := DecodeSourceObservation(raw)
	if err != nil || *got.ReadAtNS != 9007199254740993 || *got.Revision != 9223372036854775807 {
		t.Fatalf("precision: %v", err)
	}
	source.Revision = nil
	source.ContentSHA256 = nil
	source.ReadAtNS = nil
	source.ObservedAtNS = nil
	source.ProvenanceStatus = "not_observed"
	source.ReadAuthorization = "not_recorded"
	raw, _ = json.Marshal(source)
	got, err = DecodeSourceObservation(raw)
	if err != nil || !reflect.DeepEqual(got, source) {
		t.Fatalf("unknown fabricated: %v", err)
	}
}
func TestContextReceiptContextInvalid(t *testing.T) {
	changes := map[string]func(*ContextObservation){
		"nilsources": func(v *ContextObservation) { v.Sources = nil },
		"too_many": func(v *ContextObservation) {
			for len(v.Sources) <= 100 {
				v.Sources = append(v.Sources, contextReceiptSource())
			}
		},
		"badsource":           func(v *ContextObservation) { v.Sources[0].Authority = "trusted" },
		"hashpair":            func(v *ContextObservation) { v.ContextSHA256 = nil },
		"stage":               func(v *ContextObservation) { v.ContextStage = "complete" },
		"definitionnamespace": func(v *ContextObservation) { v.Run.Definition.Ref.Collection = "memories" },
		"runnamespace":        func(v *ContextObservation) { v.Run.RunRef.Collection = "jobs" },
		"badCAS":              func(v *ContextObservation) { v.Run.RunRef.Revision = 0 },
		"oldcontract":         func(v *ContextObservation) { v.ContractID = "arkism-crew-research-v1" },
		"zeroattempt":         func(v *ContextObservation) { v.NodeAttempt = 0 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			value := contextReceiptRecord(t)
			change(&value)
			if _, err := SealContextObservation(value); err == nil {
				t.Fatal("accepted invalid context")
			}
		})
	}
	value := contextReceiptRecord(t)
	value.Sources[0].ID = "changed"
	if ValidateContextObservation(value) == nil {
		t.Fatal("unsealed mutation accepted")
	}
}
func TestContextReceiptHashSemantics(t *testing.T) {
	value := contextReceiptRecord(t)
	raw, err := CanonicalContextObservation(value)
	if err != nil || len(raw) == 0 {
		t.Fatal(err)
	}
	copy := value
	copy.RecordSHA256 = strings.Repeat("0", 64)
	sealed, err := SealContextObservation(copy)
	if err != nil || sealed.RecordSHA256 != value.RecordSHA256 {
		t.Fatal("selfhash dependency")
	}
	sealed.Sources[0].ID = "mutated"
	if value.Sources[0].ID != "memory" {
		t.Fatal("seal aliased caller")
	}
	for _, text := range []string{"", " a\n", "한글🌳", strings.Repeat("x", 2100)} {
		sha, err := ContextTextSHA256(text)
		if err != nil || sha != observationBytesSHA([]byte(text)) {
			t.Fatal("text bytes changed")
		}
	}
	if _, err = ContextTextSHA256(string([]byte{255})); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
	clipped, _ := ContextTextSHA256(strings.Repeat("x", 2000))
	full, _ := ContextTextSHA256(strings.Repeat("x", 2100))
	if clipped == full {
		t.Fatal("preclip digest lost")
	}
}
func TestContextReceiptCurrentUseRejections(t *testing.T) {
	changes := map[string]func(*ContextUseInput){
		"foreignscope":      func(v *ContextUseInput) { v.Current.Observation.Scope.ProjectID = "other" },
		"foreignid":         func(v *ContextUseInput) { v.Current.Facts.Ref.ID = "other" },
		"foreigncollection": func(v *ContextUseInput) { v.Current.Facts.Ref.Collection = "knowledge_documents" },
		"wrongrev":          func(v *ContextUseInput) { v.Current.Facts.Ref.Revision = 6 },
		"wrongdigest":       func(v *ContextUseInput) { v.Current.Facts.ContentSHA256 = strings.Repeat("d", 64) },
		"unavailable":       func(v *ContextUseInput) { v.Current.Facts.ReadState = "unavailable" },
		"denied":            func(v *ContextUseInput) { v.Current.Facts.ReadState = "denied" },
		"inactive":          func(v *ContextUseInput) { v.Current.Facts.Lifecycle = "inactive" },
		"deleted":           func(v *ContextUseInput) { v.Current.Facts.Lifecycle = "deleted" },
		"unknownlifecycle":  func(v *ContextUseInput) { v.Current.Facts.Lifecycle = "unknown" },
		"unknownshare":      func(v *ContextUseInput) { v.Current.Facts.Sharing = "unknown" },
		"staleshare":        func(v *ContextUseInput) { v.Current.Observation.Sharing = "shared" },
		"discordmemory":     func(v *ContextUseInput) { v.Discord = true },
		"unknownpolicy":     func(v *ContextUseInput) { v.Current.Facts.PolicyRef = nil },
		"foreignpolicy":     func(v *ContextUseInput) { v.Current.Facts.PolicyRef = &ObservationRef{"crew", "foreign", 3} },
		"oldpolicy":         func(v *ContextUseInput) { v.Current.Facts.PolicyRef.Revision = 2 },
		"policyfuture":      func(v *ContextUseInput) { v.Current.Facts.PolicyReadAtNS = observationInt(101) },
		"policynottime":     func(v *ContextUseInput) { v.Current.Facts.PolicyReadAtNS = nil },
		"policyunconfirmed": func(v *ContextUseInput) { v.Current.Facts.PolicyState = "unknown" },
		"noguard":           func(v *ContextUseInput) { v.Current.Facts.GuardSHA256 = "" },
		"unknowncheck":      func(v *ContextUseInput) { v.Current.Facts.CheckedAtNS = nil },
		"futurecheck":       func(v *ContextUseInput) { v.Current.Facts.CheckedAtNS = observationInt(101) },
		"checkbefore_read":  func(v *ContextUseInput) { v.Current.Facts.CheckedAtNS = observationInt(99) },
		"resumemissing":     func(v *ContextUseInput) { v.Resume = true },
		"initialunverified": func(v *ContextUseInput) { v.Premise = ContextPremiseUnverified },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := contextReceiptInput()
			change(&input)
			got := EvaluateContextUse(input)
			if got.Action != "block" || got.EvidenceComplete {
				t.Fatalf("unsafe current use: %+v", got)
			}
			input.Mode = ContextReceiptObserve
			got = EvaluateContextUse(input)
			if got.Action != "continue" || got.ProposedAction != "block" {
				t.Fatalf("observe changed work: %+v", got)
			}
		})
	}
}
func TestContextReceiptChangedPremises(t *testing.T) {
	cases := []struct {
		name    string
		premise ContextPremiseDisposition
		changed bool
		want    string
	}{{"unchanged", ContextPremiseUnchanged, false, "continue"}, {"display", ContextPremiseDisplayOnly, false, "continue"}, {"typedrefresh", ContextPremiseRefresh, true, "refresh"}, {"frozenrestart", ContextPremiseRestart, true, "restart"}, {"unverified", ContextPremiseUnverified, true, "block"}, {"equaltext_newguard", ContextPremiseUnchanged, true, "block"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := contextReceiptInput()
			old := input.Current.Observation
			input.Previous = &ContextSourceState{old, input.Current.Facts.GuardSHA256}
			input.Premise = tc.premise
			input.Current.Observation.Revision = observationInt(6)
			input.Current.Facts.Ref.Revision = 6
			if tc.changed {
				input.Current.Facts.GuardSHA256 = strings.Repeat("d", 64)
			}
			got := EvaluateContextUse(input)
			if got.Action != tc.want {
				t.Fatalf("want %s: %+v", tc.want, got)
			}
		})
	}
	for name, revision := range map[string]int64{"conflict": 5, "regression": 4} {
		t.Run(name, func(t *testing.T) {
			input := contextReceiptInput()
			input.Previous = &ContextSourceState{input.Current.Observation, input.Current.Facts.GuardSHA256}
			input.Current.Observation.Revision = observationInt(revision)
			input.Current.Facts.Ref.Revision = revision
			input.Current.Facts.GuardSHA256 = strings.Repeat("d", 64)
			input.Premise = ContextPremiseRefresh
			if got := EvaluateContextUse(input); got.Action != "block" {
				t.Fatal(got)
			}
		})
	}
}
func TestContextReceiptCollector(t *testing.T) {
	for name, sink := range map[string]func(ContextObservation) error{"memory": nil, "error": func(ContextObservation) error { return errors.New("secret body") }, "panic": func(ContextObservation) error { panic("secret body") }} {
		t.Run(name, func(t *testing.T) {
			session, _ := NewContextReceiptSession(ContextReceiptObserve, sink)
			ctx := WithContextReceiptSession(context.Background(), session)
			if AttemptObserverEnabled(ctx) {
				t.Fatal("hidden observer enabled")
			}
			if _, err := session.Capture(ctx, contextReceiptRecord(t)); err == nil {
				t.Fatal("missing observer accepted")
			}
			actual, _ := AttemptContextFrom(observerTestContext())
			ctx = WithAttemptContext(WithAttemptObserver(ctx, NewAttemptObserver(nil)), actual)
			session, _ = NewContextReceiptSession(ContextReceiptObserve, sink)
			value := contextReceiptRecord(t)
			value.Run.RunRef.ID = "fake"
			value.Task.TaskID = "fake"
			got, err := session.Capture(ctx, value)
			if err != nil || got.Run.RunRef.ID != "run" || got.Task.TaskID != "task" {
				t.Fatalf("actual binding: %v", err)
			}
			if _, err = session.Capture(ctx, got); err != nil {
				t.Fatal(err)
			}
			snapshot := session.Snapshot()
			if len(snapshot.Records) != 1 || snapshot.Complete != (name == "memory") {
				t.Fatalf("bad snapshot: %+v", snapshot)
			}
			for _, issue := range snapshot.Issues {
				if strings.Contains(issue, "secret") {
					t.Fatal("sinkbody leak")
				}
			}
			snapshot.Records[0].Sources[0].ID = "mutated"
			if session.Snapshot().Records[0].Sources[0].ID != "memory" {
				t.Fatal("snapshot aliases records")
			}
		})
	}
}
func TestContextReceiptCollectorOffAndBindings(t *testing.T) {
	ctx := WithAttemptObserver(observerTestContext(), NewAttemptObserver(nil))
	value := contextReceiptRecord(t)
	for _, session := range []*ContextReceiptSession{nil, {}, {mode: ContextReceiptOff}} {
		if _, err := session.Capture(ctx, value); err != nil || session.Snapshot().Complete {
			t.Fatal("off changed behavior")
		}
	}
	if _, err := NewContextReceiptSession("unknown", nil); err == nil {
		t.Fatal("invalid mode")
	}
	session, _ := NewContextReceiptSession(ContextReceiptObserve, nil)
	if session.Snapshot().Complete {
		t.Fatal("empty evidence complete")
	}
	value.Sources[0].Scope.ProjectID = "other"
	if _, err := session.Capture(ctx, value); err == nil {
		t.Fatal("foreignsource captured")
	}
	value = contextReceiptRecord(t)
	value.SummaryRef = &ObservationRef{"jobs", "run", 3}
	if _, err := session.Capture(ctx, value); err == nil {
		t.Fatal("summarynamespace accepted")
	}
	value = contextReceiptRecord(t)
	value.Sources[0].ReadAtNS = observationInt(101)
	if _, err := session.Capture(ctx, value); err == nil {
		t.Fatal("future source consumed")
	}
}
func TestContextReceiptConcurrentReplay(t *testing.T) {
	ctx := WithAttemptObserver(observerTestContext(), NewAttemptObserver(nil))
	session, _ := NewContextReceiptSession(ContextReceiptObserve, nil)
	value := contextReceiptRecord(t)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := session.Capture(ctx, value); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := session.Snapshot(); len(got.Records) != 1 || !got.Complete {
		t.Fatalf("replay: %+v", got)
	}
	raw, _ := json.Marshal(ContextReceiptRead{Text: "raw secret"})
	if strings.Contains(string(raw), "raw secret") {
		t.Fatal("ephemeral body leaked")
	}
}

func TestContextReceiptPendingDelivery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	session, _ := NewContextReceiptSession(ContextReceiptObserve, func(ContextObservation) error { close(entered); <-release; return nil })
	ctx := WithAttemptObserver(observerTestContext(), NewAttemptObserver(nil))
	value := contextReceiptRecord(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := session.Capture(ctx, value); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	pending := session.Snapshot()
	if pending.Complete || len(pending.Issues) != 1 || pending.Issues[0] != "sink_pending" {
		t.Errorf("pending delivery claimed complete: %+v", pending)
	}
	if _, err := session.Capture(ctx, value); err != nil {
		t.Error(err)
	}
	close(release)
	<-done
	if got := session.Snapshot(); !got.Complete || len(got.Records) != 1 {
		t.Fatal(got)
	}
}
func TestContextReceiptResumeLineage(t *testing.T) {
	input := contextReceiptInput()
	input.Resume = true
	input.Previous = &ContextSourceState{input.Current.Observation, input.Current.Facts.GuardSHA256}
	lineage := contextReceiptRecord(t)
	lineage.ContextStage = "briefing"
	lineage, err := SealContextObservation(lineage)
	if err != nil {
		t.Fatal(err)
	}
	input.ProtectedLineage = &lineage
	input.ExecutionRef = lineage.Run.RunRef
	input.CurrentTask = lineage.Task
	if got := EvaluateContextUse(input); got.Action != "continue" {
		t.Fatal(got)
	}
	changes := map[string]func(*ContextUseInput){
		"missing":          func(v *ContextUseInput) { v.ProtectedLineage = nil },
		"foreignrun":       func(v *ContextUseInput) { v.ExecutionRef.ID = "other" },
		"foreignnamespace": func(v *ContextUseInput) { v.ExecutionRef.Collection = "crew_meetings" },
		"corruptedhash":    func(v *ContextUseInput) { v.ProtectedLineage.RecordSHA256 = strings.Repeat("0", 64) },
		"othersource":      func(v *ContextUseInput) { v.Previous.Observation.ID = "other" },
		"otherpolicy":      func(v *ContextUseInput) { v.Current.Facts.PolicyScope.ProjectID = "other" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			test := input
			previous := *input.Previous
			test.Previous = &previous
			record := cloneContextReceipt(lineage)
			test.ProtectedLineage = &record
			change(&test)
			if got := EvaluateContextUse(test); got.Action != "block" {
				t.Fatalf("resume accepted: %+v", got)
			}
		})
	}
}

func TestContextReceiptUnknownHistoricalRead(t *testing.T) {
	for _, name := range []string{"unavailable", "not_observed", "revision_not_recorded", "auth_not_recorded", "read_time_missing"} {
		t.Run(name, func(t *testing.T) {
			input := contextReceiptInput()
			old := input.Current.Observation
			old.ProvenanceStatus = name
			if name == "auth_not_recorded" {
				old.ProvenanceStatus = "revision_not_recorded"
				old.ReadAuthorization = "not_recorded"
			}
			if name == "read_time_missing" {
				old.ProvenanceStatus = "revision_not_recorded"
				old.ReadAtNS = nil
			}
			if err := ValidateSourceObservation(old); err != nil {
				t.Fatalf("expected canonical-valid unknown: %v", err)
			}
			input.Previous = &ContextSourceState{old, input.Current.Facts.GuardSHA256}
			if got := EvaluateContextUse(input); got.Action != "block" || got.EvidenceComplete {
				t.Fatalf("unknown historical promoted: %+v", got)
			}
		})
	}
}

func TestContextReceiptSummarySnapshotCAS(t *testing.T) {
	actual, _ := AttemptContextFrom(observerTestContext())
	actual.Run.RunRef = ObservationRef{"crew_meetings", "meeting", 10}
	actual.Run.RootRunID = observationString("meeting")
	actual.Run.RootRunCollection = observationString("crew_meetings")
	actual.Run.Definition.SourceType = "team"
	actual.Run.ExecutionPath = "crew"
	actual.Run.InvocationMode = "legacy"
	ctx := WithAttemptContext(WithAttemptObserver(context.Background(), NewAttemptObserver(nil)), actual)
	for name, revision := range map[string]int64{"matching": 10, "earlier": 9, "future": 11} {
		t.Run(name, func(t *testing.T) {
			session, _ := NewContextReceiptSession(ContextReceiptObserve, nil)
			value := contextReceiptRecord(t)
			value.ContextStage = "summary_handoff"
			value.SummaryRef = &ObservationRef{"crew_meetings", "meeting", revision}
			got, err := session.Capture(ctx, value)
			if name == "future" {
				if err == nil {
					t.Fatal("future summary snapshot accepted")
				}
				return
			}
			if err != nil || got.SummaryRef.Revision != revision {
				t.Fatalf("actual snapshot: %v", err)
			}
		})
	}
}
