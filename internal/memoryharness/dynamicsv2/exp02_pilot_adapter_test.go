package dynamicsv2

// EXP-02 early pilot adapter: projects EXP-01 label-free plan rows onto the
// registered seven-policy offline runner. Gold is never read here. Every
// mapping choice the plan rows do not determine is named in exp02Mapping and
// written to the output manifest, so the pilot report can disclose it.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/memoryharness"
)

var exp02Mapping = map[string]string{
	"clock_domain":         "plan clock domain is replaced by the registry dynamics clock domain; ns values are kept",
	"accepted_seq":         "one consecutive counter per stream over projected events in plan_seq order",
	"material_available":   "activate target",
	"correction_committed": "invalidate old target, then activate each related (new) revision not yet available",
	"material_deleted":     "invalidate target",
	"exposure_observed":    "exposure on target",
	"outcome_observed":     "outcome on target; declared_not_authenticated synthetic evidence is projected as synthetic_independent with strength 1 and attribution memory for passed/failed; outcome on an invalidated target is dropped and counted; task and evidence rule_sha256 take the registry evidence_rule_sha256 while the source rule stays as checker_sha256",
	"dropped_kinds":        "task_started, correction_proposed",
	"relevance_lane":       "lane 0 orders every known revision by shared character bigrams between query_text and payload text, then ref id; a positive overlap carries reason text_search; lanes 1-2 empty",
	"eligibility":          "current queries: a revision is active until invalidated at or before the cutoff; historical queries: additionally available at requested_as_of_ns; a revision invalidated by the cutoff stays inactive because the native source fence forbids reviving it, so historical gold is unreachable for every policy alike",
	"importance_feature":   "kind_weight_v1: preference 0.75, other kinds 0.5; a revision activated by a correction inherits the superseded revision's kind; available at the activation",
	"pooling":              "EXP02_POOL=1 merges streams of one split into pools without a repeated entity-topic pair; events ordered by available time, refused unless every stream keeps its plan order; a query cutoff is the merged position of its own last event at or before the original cutoff",
	"pooled_scope":         "every pool member takes the first member's scope, so scope isolation is not exercised in pooled runs",
	"answer_text":          "selected_text carries the exact payload text the relevance lane used for each selected ref",
	"projected_ids":        "event, attempt, feature and evidence ids are a one-letter prefix plus the first 40 hex of sha256(plan id)",
	"packing":              "final_k 2, context cost 1 per candidate, budget 2",
}

type exp02Ref struct {
	Origin        string `json:"origin"`
	Collection    string `json:"collection"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	ContentSHA256 string `json:"content_sha256"`
}

type exp02Event struct {
	EventID       string     `json:"event_id"`
	Kind          string     `json:"kind"`
	PlanSeq       int64      `json:"plan_seq"`
	AvailableAtNS int64      `json:"available_at_ns"`
	ArrivedAtNS   int64      `json:"arrived_at_ns"`
	StreamID      string     `json:"stream_id"`
	Scope         ScopeV2    `json:"scope"`
	Target        *exp02Ref  `json:"target"`
	RelatedRefs   []exp02Ref `json:"related_refs"`
	TaskID        *string    `json:"task_id"`
	Outcome       *struct {
		EvidenceID       string `json:"evidence_id"`
		Judgment         string `json:"judgment"`
		SourceRuleSHA256 string `json:"source_rule_sha256"`
	} `json:"outcome"`
	SourcePayload *struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"source_payload"`
}

type exp02Query struct {
	QueryID         string  `json:"query_id"`
	StreamID        string  `json:"stream_id"`
	Scope           ScopeV2 `json:"scope"`
	AtNS            int64   `json:"at_ns"`
	CutoffPlanSeq   int64   `json:"cutoff_plan_seq"`
	QueryText       string  `json:"query_text"`
	RequestMode     string  `json:"request_mode"`
	RequestedAsOfNS *int64  `json:"requested_as_of_ns"`
}

type exp02Known struct {
	ref         RefV2
	kind, text  string
	seq, atNS   int64
	invalidSeq  int64 // 0 while current
	invalidAtNS int64
}

type exp02Row struct {
	StreamID  string    `json:"stream_id"`
	QueryID   string    `json:"query_id"`
	PolicyID  PolicyID  `json:"policy_id"`
	Status    string    `json:"status"`
	ErrorCode string    `json:"error_code"`
	ErrorPath string    `json:"error_path"`
	Selected  []RefV2   `json:"selected"`
	Ranks     []RankRow `json:"ranks"`
	// SelectedText is the payload text the adapter ranked on, so prompts cannot drift from it.
	SelectedText []string `json:"selected_text"`
}

func exp02Lines[T any](t *testing.T, path string) []T {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := []T{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<24)
	for s.Scan() {
		var v T
		if err := json.Unmarshal(s.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func exp02RefV2(r exp02Ref) RefV2 {
	return RefV2{Origin: r.Origin, Collection: r.Collection, ID: r.ID, Revision: r.Revision, ContentSHA256: r.ContentSHA256}
}

func exp02Bigrams(s string) map[string]bool {
	r, out := []rune(s), map[string]bool{}
	for i := 0; i+1 < len(r); i++ {
		if r[i] != ' ' && r[i+1] != ' ' {
			out[string(r[i:i+2])] = true
		}
	}
	return out
}

func exp02Overlap(a, b string) int {
	n, bb := 0, exp02Bigrams(b)
	for g := range exp02Bigrams(a) {
		if bb[g] {
			n++
		}
	}
	return n
}

func exp02SHA(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// exp02ID maps a plan id onto the contract id pattern (lowercase, 64 bytes, no colon).
func exp02ID(prefix, raw string) string { return prefix + exp02SHA(raw)[:40] }

// exp02Stream projects one stream and runs the seven registered policies.
// It returns one row per (query, policy) and the dropped-outcome count.
func exp02Stream(t *testing.T, registry PolicyRegistryV2, events []exp02Event, queries []exp02Query) ([]exp02Row, int) {
	t.Helper()
	source := *registry.Entries[2].Dynamics
	clock, stream := source.ClockDomain, events[0].StreamID
	sort.SliceStable(events, func(i, j int) bool { return events[i].PlanSeq < events[j].PlanSeq })
	file := EventFileV2{Protocol: "dynamics-event-file-v2", StreamID: stream, ClockDomain: clock, ConfigSHA256: source.ConfigSHA256, Events: []EventV2{}}
	known, order := map[RefV2]*exp02Known{}, []RefV2{}
	features, dropped := []ImportanceFeatureV1{}, 0
	lastSeqAtPlan := map[int64]int64{}
	var seq, atNS int64
	text := map[string]string{} // content sha -> text, for corrected revisions
	for _, e := range events {
		if e.SourcePayload != nil && e.Target != nil {
			text[e.Target.ContentSHA256] = e.SourcePayload.Text
		}
	}
	emit := func(e exp02Event, kind EventKind, target RefV2, suffix string) {
		if e.ArrivedAtNS > e.AvailableAtNS {
			t.Fatalf("event %s arrives after it is available; accepted time would precede arrival", e.EventID)
		}
		seq++
		atNS = e.AvailableAtNS
		key := DynamicsKeyV2{Scope: e.Scope, Target: target, Dimension: "work_task", ContractSHA256: ContractSHA256, ConfigSHA256: source.ConfigSHA256}
		ev := EventV2{Protocol: "dynamics-event-v2", StreamID: stream, EventID: exp02ID("e", e.EventID+suffix), TransportAttemptID: exp02ID("t", e.EventID+suffix), Key: key, Kind: kind, ClockDomain: clock, ArrivedAtNS: ptr(e.ArrivedAtNS), AcceptedAtNS: atNS, AcceptedSeq: seq, Outcome: "not_applicable", Attribution: "not_applicable"}
		switch kind {
		case "activate":
			ev.Reason = "activation"
		case "exposure":
			ev.Reason = "exposure_only"
		case "invalidate":
			ev.Reason = "source_deleted"
		case "outcome":
			o := e.Outcome
			ev.Outcome, ev.Attribution, ev.Reason = Outcome(o.Judgment), "unattributable", "unknown_outcome"
			taskID := "none"
			if e.TaskID != nil {
				taskID = *e.TaskID
			}
			task := TaskBinding{TaskRef: EvidenceRef{Namespace: "fixture", Collection: "eval_tasks", ID: exp02ID("k", taskID), Revision: 1, ContentSHA256: exp02SHA(taskID)}, RunRef: EvidenceRef{Namespace: "fixture", Collection: "eval_runs", ID: exp02ID("r", e.EventID), Revision: 1, ContentSHA256: exp02SHA("run" + e.EventID)}, TrialRef: EvidenceRef{Namespace: "fixture", Collection: "eval_trials", ID: exp02ID("x", e.EventID), Revision: 1, ContentSHA256: exp02SHA("trial" + e.EventID)}, CheckerSHA256: o.SourceRuleSHA256, RuleSHA256: source.EvidenceRuleSHA256, Qualification: "synthetic_independent", TaskSHA256: exp02SHA(taskID)}
			ev.Task = &task
			ev.Evidence = ptr(sealFixture(t, "EvidenceBinding", EvidenceBinding{Refs: []EvidenceRef{{Namespace: "fixture", Collection: "evaluations", ID: exp02ID("v", o.EvidenceID), Revision: 1, ContentSHA256: exp02SHA(o.EvidenceID)}}, Target: target, TaskSHA256: task.TaskSHA256, Dimension: "work_task", RuleSHA256: source.EvidenceRuleSHA256, Independence: "synthetic_independent", AvailableAcceptedSeq: seq, SourceStatus: "available"}))
			if o.Judgment == "passed" || o.Judgment == "failed" {
				ev.Attribution, ev.Strength = "memory", ptr(1.0)
				ev.Reason = "passed_independent"
				if o.Judgment == "failed" {
					ev.Reason = "failed_attributable"
				}
			}
		}
		file.Events = append(file.Events, sealFixture(t, "EventV2", ev))
	}
	activate := func(e exp02Event, r exp02Ref, suffix, kind string) {
		ref := exp02RefV2(r)
		body, ok := text[ref.ContentSHA256]
		if !ok {
			t.Fatalf("no payload text for %s revision %d", ref.ID, ref.Revision)
		}
		emit(e, "activate", ref, suffix)
		k := &exp02Known{ref: ref, kind: kind, text: body, seq: seq, atNS: atNS}
		known[ref], order = k, append(order, ref)
		value := .5
		if k.kind == "preference" {
			value = .75
		}
		src := EvidenceRef{Namespace: "fixture", Collection: "sources", ID: exp02ID("s", e.EventID), Revision: 1, ContentSHA256: exp02SHA(e.EventID)}
		features = append(features, sealFixture(t, "ImportanceFeatureV1", ImportanceFeatureV1{FeatureID: exp02ID("f", fmt.Sprintf("%s:%d", ref.ID, ref.Revision)), Scope: e.Scope, Target: ref, Source: src, ClockDomain: clock, ObservedAtNS: ptr(atNS), AvailableAtNS: atNS, AvailableAcceptedSeq: seq, FeatureName: "importance", FeatureVersion: "kind_weight_v1", RuleSHA256: exp02SHA("kind_weight_v1"), GeneratorConfigSHA256: exp02SHA("exp02-pilot-adapter-v1"), ProducerVersion: "exp02-pilot-adapter-v1", InputRefs: []EvidenceRef{src}, Status: "present", Value: ptr(value), Unit: "dimensionless"}))
	}
	invalidate := func(e exp02Event, r exp02Ref, suffix string) {
		ref := exp02RefV2(r)
		emit(e, "invalidate", ref, suffix)
		if k := known[ref]; k != nil && k.invalidSeq == 0 {
			k.invalidSeq, k.invalidAtNS = seq, atNS
		}
	}
	for _, e := range events {
		switch e.Kind {
		case "material_available":
			if known[exp02RefV2(*e.Target)] != nil {
				t.Fatalf("revision %s %d is already available", e.Target.ID, e.Target.Revision)
			}
			activate(e, *e.Target, "", e.SourcePayload.Kind)
		case "correction_committed":
			inherited := ""
			if old := known[exp02RefV2(*e.Target)]; old != nil {
				inherited = old.kind
			}
			invalidate(e, *e.Target, ":old")
			for i, r := range e.RelatedRefs {
				if known[exp02RefV2(r)] == nil { // a revision already available is not activated twice
					activate(e, r, ":new"+string(rune('0'+i)), inherited)
				}
			}
		case "material_deleted":
			invalidate(e, *e.Target, "")
		case "exposure_observed":
			if k := known[exp02RefV2(*e.Target)]; k != nil && k.invalidSeq == 0 {
				emit(e, "exposure", k.ref, "")
			} else {
				dropped++
			}
		case "outcome_observed":
			if k := known[exp02RefV2(*e.Target)]; k != nil && k.invalidSeq == 0 {
				emit(e, "outcome", k.ref, "")
			} else {
				dropped++
			}
		}
		lastSeqAtPlan[e.PlanSeq] = seq
	}
	cutoffSeq := func(plan int64) int64 {
		best := int64(0)
		for p, s := range lastSeqAtPlan {
			if p <= plan && s > best {
				best = s
			}
		}
		return best
	}
	sort.Slice(features, func(i, j int) bool { return featureKey(features[i]) < featureKey(features[j]) })

	cfg := DynamicsRunConfigV2{Protocol: "dynamics-run-config-v2", RunID: "exp02-pilot-" + exp02SHA(stream)[:16], InputManifestSHA256: exp02SHA("exp02-pilot-" + stream), RegistrySHA256: registry.RegistrySHA256, PolicyIDs: []PolicyID{}, Configs: []PolicyConfig{}, ResourceLimits: registry.ResourceLimits, Registry: registry}
	for _, entry := range registry.Entries {
		cfg.PolicyIDs = append(cfg.PolicyIDs, entry.PolicyID)
		cfg.Configs = append(cfg.Configs, PolicyConfig{PolicyID: entry.PolicyID, Ranker: entry.Ranker, Dynamics: entry.Dynamics})
	}
	cfg = sealFixture(t, "DynamicsRunConfigV2", cfg)
	active := map[string]map[RefV2]bool{} // snapshot -> ref -> active
	probes := ProbeFileV2{Protocol: "dynamics-probe-file-v2", StreamID: stream, ClockDomain: clock, Probes: []ProbeV2{}}
	candidates := memoryharness.CandidateFile{ContractID: "arkism-offline-b0-v1", Snapshots: []memoryharness.CandidateSnapshot{}}
	for _, q := range queries {
		cut := cutoffSeq(q.CutoffPlanSeq)
		// The B0 snapshot holds one candidate per memory id: the active revision,
		// else the latest known one (inactive).
		pick := map[string]memoryharness.Candidate{}
		ids := []string{}
		for _, ref := range order {
			k := known[ref]
			if k.seq > cut {
				continue
			}
			active := k.invalidSeq == 0 || k.invalidSeq > cut
			if q.RequestedAsOfNS != nil {
				// The source fence forbids an active candidate already invalidated by the cutoff,
				// so a historical query can only narrow, never revive, a revision.
				active = active && k.atNS <= *q.RequestedAsOfNS
			}
			reasons := []string{}
			if exp02Overlap(q.QueryText, k.text) > 0 {
				reasons = []string{"text_search"}
			}
			c := memoryharness.Candidate{Scope: q.Scope, Ref: ref, Reasons: reasons, Trust: .5, UpdatedAtNS: k.atNS, ObservedSeq: k.seq, EffectiveAtNS: k.atNS, ContextCostUnits: 1, Approved: true, Active: active, SourceVerified: true, Authority: "data_only_untrusted"}
			prior, seen := pick[ref.ID]
			if !seen {
				ids = append(ids, ref.ID)
			}
			if !seen || active || !prior.Active {
				pick[ref.ID] = c
			}
		}
		lane := []memoryharness.Candidate{}
		for _, id := range ids {
			lane = append(lane, pick[id])
		}
		sort.SliceStable(lane, func(i, j int) bool {
			a, b := exp02Overlap(q.QueryText, known[lane[i].Ref].text), exp02Overlap(q.QueryText, known[lane[j].Ref].text)
			if a != b {
				return a > b
			}
			return lane[i].Ref.ID+string(rune(lane[i].Ref.Revision)) < lane[j].Ref.ID+string(rune(lane[j].Ref.Revision))
		})
		active[q.QueryID] = map[RefV2]bool{}
		for _, c := range lane {
			active[q.QueryID][c.Ref] = c.Active
		}
		snap := memoryharness.CandidateSnapshot{SnapshotID: q.QueryID, StreamID: stream, Scope: q.Scope, AsOfNS: q.AtNS, AsOfSeq: cut, Lanes: [][]memoryharness.Candidate{lane, {}, {}}}
		candidates.Snapshots = append(candidates.Snapshots, snap)
		for _, entry := range registry.Entries {
			p := ProbeV2{Protocol: "dynamics-probe-v2", QueryID: q.QueryID, StreamID: stream, Scope: q.Scope, Dimension: "work_task", AtNS: q.AtNS, ClockDomain: clock, CutoffAcceptedSeq: cut, CandidateSnapshotID: snap.SnapshotID, FinalK: 2, ContextBudget: memoryharness.ContextBudget{Unit: "fixture_units_v0", Limit: 2}, PolicyID: entry.PolicyID, PolicyVersion: entry.Version}
			policySealSnapshot(t, snap, &p)
			configSHA := source.ConfigSHA256
			if entry.Dynamics != nil {
				configSHA = entry.Dynamics.ConfigSHA256
			}
			p.PrefixSHA256 = hashTypedNativeFixture(t, file, p, configSHA)
			probes.Probes = append(probes.Probes, p)
		}
	}
	runs, err := RunPoliciesP1(file, probes, candidates, P1FeatureFileV1{Protocol: "p1-feature-file-v1", FeatureContractSHA256: P1ContractSHA256, Features: features}, cfg)
	if err != nil {
		t.Fatalf("stream %s: %v", stream, err)
	}
	rows := []exp02Row{}
	for _, run := range runs {
		for _, p := range run.Predictions {
			row := exp02Row{StreamID: stream, QueryID: p.QueryID, PolicyID: run.PolicyID, Status: p.Status, Selected: []RefV2{}}
			if p.Error != nil {
				row.ErrorCode, row.ErrorPath = string(p.Error.Code), p.Error.Path
			}
			if p.Decision != nil {
				row.Selected, row.Ranks = p.Decision.Selected, p.Decision.Ranks
			}
			for _, ref := range row.Selected {
				if !active[p.QueryID][ref] {
					t.Fatalf("policy %s selected inactive %s for %s", run.PolicyID, ref.ID, p.QueryID)
				}
				row.SelectedText = append(row.SelectedText, known[ref].text)
			}
			rows = append(rows, row)
		}
	}
	return rows, dropped
}

func exp02Registry(t *testing.T) PolicyRegistryV2 {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/research/memory-harness/policy_registry.json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := DecodeStrict(raw, "PolicyRegistryV2")
	if err != nil {
		t.Fatal(err)
	}
	return v.(PolicyRegistryV2)
}

func exp02Run(t *testing.T, dir string) ([]exp02Row, int) {
	t.Helper()
	events := exp02Lines[exp02Event](t, filepath.Join(dir, "events.jsonl"))
	queries := exp02Lines[exp02Query](t, filepath.Join(dir, "queries.jsonl"))
	byStream, qByStream, streams := map[string][]exp02Event{}, map[string][]exp02Query{}, []string{}
	for _, e := range events {
		if byStream[e.StreamID] == nil {
			streams = append(streams, e.StreamID)
		}
		byStream[e.StreamID] = append(byStream[e.StreamID], e)
	}
	for _, q := range queries {
		qByStream[q.StreamID] = append(qByStream[q.StreamID], q)
	}
	sort.Strings(streams)
	if os.Getenv("EXP02_POOL") == "1" {
		byStream, qByStream, streams = exp02Pool(t, dir, byStream, qByStream, streams)
	}
	registry, rows, dropped := exp02Registry(t), []exp02Row{}, 0
	for _, s := range streams {
		r, d := exp02Stream(t, registry, byStream[s], qByStream[s])
		rows, dropped = append(rows, r...), dropped+d
	}
	return rows, dropped
}

// exp02Pool merges the streams of one split into pooled native streams so each
// query competes against other instances' memories. A pool never holds two
// streams with the same entity-topic pair, keeping the gold refs unambiguous.
// Events are ordered by available time (stable on plan order), plan_seq becomes
// the merged position, and each query's cutoff becomes the merged position of
// its own stream's last event at or before its original cutoff.
func exp02Pool(t *testing.T, dir string, byStream map[string][]exp02Event, qByStream map[string][]exp02Query, streams []string) (map[string][]exp02Event, map[string][]exp02Query, []string) {
	t.Helper()
	var manifest struct {
		Entries []struct {
			StreamID string `json:"stream_id"`
			Split    string `json:"split"`
			Pairs    []struct {
				EntityID string `json:"entity_id"`
				TopicID  string `json:"topic_id"`
			} `json:"entity_topic_pairs"`
		} `json:"entries"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "split_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	type pool struct {
		split   string
		pairs   map[string]bool
		members []string
	}
	pools := []*pool{}
	entries := map[string]int{}
	for i, e := range manifest.Entries {
		entries[e.StreamID] = i
	}
	for _, s := range streams {
		i, ok := entries[s]
		if !ok {
			t.Fatalf("stream %s missing from split_manifest.json", s)
		}
		e := manifest.Entries[i]
		keys := []string{}
		for _, p := range e.Pairs {
			keys = append(keys, p.EntityID+"/"+p.TopicID)
		}
		var into *pool
		for _, p := range pools {
			if p.split == e.Split && !slices.ContainsFunc(keys, func(k string) bool { return p.pairs[k] }) {
				into = p
				break
			}
		}
		if into == nil {
			into = &pool{split: e.Split, pairs: map[string]bool{}}
			pools = append(pools, into)
		}
		for _, k := range keys {
			into.pairs[k] = true
		}
		into.members = append(into.members, s)
	}
	outE, outQ, ids := map[string][]exp02Event{}, map[string][]exp02Query{}, []string{}
	for _, p := range pools {
		id := "s" + exp02SHA("exp02-pool/" + strings.Join(p.members, ","))[:63]
		scope := byStream[p.members[0]][0].Scope
		members := [][]exp02Event{}
		for _, s := range p.members {
			members = append(members, byStream[s])
		}
		merged, last, err := exp02Merge(members)
		if err != nil {
			t.Fatal(err)
		}
		for i := range merged {
			merged[i].Scope = scope
		}
		for _, s := range p.members {
			for _, q := range qByStream[s] {
				cut := int64(0)
				for plan, pos := range last[s] {
					if plan <= q.CutoffPlanSeq && pos > cut {
						cut = pos
					}
				}
				q.CutoffPlanSeq, q.Scope, q.StreamID = cut, scope, id
				outQ[id] = append(outQ[id], q)
			}
		}
		for i := range merged {
			merged[i].StreamID = id
		}
		outE[id], ids = merged, append(ids, id)
	}
	return outE, outQ, ids
}

// exp02Merge orders the member streams' events by available time and renumbers
// plan_seq as the merged position. It refuses a merge that would reorder any
// stream's own events, since a cutoff could then include that stream's future.
// last maps stream -> original plan_seq -> merged position.
func exp02Merge(members [][]exp02Event) ([]exp02Event, map[string]map[int64]int64, error) {
	merged := []exp02Event{}
	for _, m := range members {
		m = append([]exp02Event{}, m...)
		sort.SliceStable(m, func(i, j int) bool { return m[i].PlanSeq < m[j].PlanSeq })
		merged = append(merged, m...)
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].AvailableAtNS < merged[j].AvailableAtNS })
	last, prior := map[string]map[int64]int64{}, map[string]int64{}
	for i := range merged {
		e := &merged[i]
		if p, seen := prior[e.StreamID]; seen && e.PlanSeq < p {
			return nil, nil, fmt.Errorf("stream %s: plan_seq %d would follow %d after the merge", e.StreamID, e.PlanSeq, p)
		}
		prior[e.StreamID] = e.PlanSeq
		if last[e.StreamID] == nil {
			last[e.StreamID] = map[int64]int64{}
		}
		last[e.StreamID][e.PlanSeq] = int64(i + 1)
		e.PlanSeq = int64(i + 1)
	}
	return merged, last, nil
}

func TestEXP02MergeKeepsStreamOrder(t *testing.T) {
	a := []exp02Event{{StreamID: "a", PlanSeq: 1, AvailableAtNS: 10}, {StreamID: "a", PlanSeq: 2, AvailableAtNS: 30}}
	b := []exp02Event{{StreamID: "b", PlanSeq: 1, AvailableAtNS: 20}}
	merged, last, err := exp02Merge([][]exp02Event{a, b})
	if err != nil || merged[1].StreamID != "b" || last["a"][2] != 3 || last["b"][1] != 2 {
		t.Fatalf("merge: %v %+v %v", err, merged, last)
	}
	// A stream whose later plan event is available earlier must be refused, not reordered.
	a[1].AvailableAtNS = 5
	if _, _, err := exp02Merge([][]exp02Event{a, b}); err == nil {
		t.Fatal("merge reordered a stream's own events")
	}
}

// TestEXP02PilotAdapter projects the EXP-01 plan named by EXP02_PLAN_DIR and
// writes rankings.jsonl + manifest.json under EXP02_OUT_DIR (must not exist).
// Without EXP02_PLAN_DIR it checks the projection on a one-stream inline plan.
func TestEXP02PilotAdapter(t *testing.T) {
	dir, out := os.Getenv("EXP02_PLAN_DIR"), os.Getenv("EXP02_OUT_DIR")
	if dir == "" {
		dir = t.TempDir()
		scope := `{"agent":"a","project":"p","user":"u","workspace":"w"}`
		ref := func(id string, rev int, body string) string {
			return `{"collection":"memories","content_sha256":"` + exp02SHA(body) + `","id":"` + id + `","origin":"memory","revision":` + string(rune('0'+rev)) + `}`
		}
		ev := func(id, kind string, seq int, target, related, payload, outcome, task string) string {
			ns := `"available_at_ns":` + string(rune('0'+seq)) + `0,"arrived_at_ns":` + string(rune('0'+seq)) + `0`
			return `{"event_id":"` + id + `","kind":"` + kind + `","plan_seq":` + string(rune('0'+seq)) + `,` + ns + `,"stream_id":"s1","scope":` + scope + `,"target":` + target + `,"related_refs":[` + related + `],"task_id":` + task + `,"outcome":` + outcome + `,"source_payload":` + payload + `}`
		}
		plan := ev("e1", "material_available", 1, ref("m1", 1, "a"), "", `{"kind":"preference","text":"솔의 방문일은 화요일"}`, "null", "null") + "\n" +
			ev("e2", "material_available", 2, ref("m2", 1, "b"), "", `{"kind":"preference","text":"민의 생일은 오월"}`, "null", "null") + "\n" +
			ev("e3", "outcome_observed", 3, ref("m1", 1, "a"), "", "null", `{"evidence_id":"v","judgment":"passed","source_rule_sha256":"`+exp02SHA("r")+`"}`, `"t"`) + "\n" +
			ev("e4", "material_available", 4, ref("m2", 2, "c"), "", `{"kind":"preference","text":"민의 생일은 유월"}`, "null", "null") + "\n" +
			ev("e5", "correction_committed", 5, ref("m2", 1, "b"), ref("m2", 2, "c"), "null", "null", "null") + "\n"
		query := `{"query_id":"q1","stream_id":"s1","scope":` + scope + `,"at_ns":60,"cutoff_plan_seq":5,"query_text":"솔의 방문일은?","request_mode":"current","requested_as_of_ns":null}` + "\n"
		if os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(plan), 0o600) != nil || os.WriteFile(filepath.Join(dir, "queries.jsonl"), []byte(query), 0o600) != nil {
			t.Fatal("inline plan not written")
		}
	}
	rows, dropped := exp02Run(t, dir)
	if os.Getenv("EXP02_PLAN_DIR") == "" {
		if len(rows) != 7 || dropped != 0 {
			t.Fatalf("want 7 rows, got %d (dropped %d)", len(rows), dropped)
		}
		for _, r := range rows {
			// Every policy packs the lexical match and the corrected revision, never the superseded one.
			if r.Status != "ok" || len(r.Selected) != 2 || r.Selected[0].Revision+r.Selected[1].Revision != 3 {
				t.Fatalf("policy %s: %+v", r.PolicyID, r)
			}
			if r.PolicyID == "B0" && r.Selected[0].ID != "m1" {
				t.Fatal("B0 did not rank the lexical match first")
			}
		}
		return
	}
	if out == "" {
		t.Fatal("EXP02_OUT_DIR is required with EXP02_PLAN_DIR")
	}
	for _, r := range rows {
		// A failed policy would otherwise reach the answer step as an empty selection.
		if r.Status != "ok" {
			t.Fatalf("policy %s failed on %s: %s at %s", r.PolicyID, r.QueryID, r.ErrorCode, r.ErrorPath)
		}
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(out, "rankings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	inputs := map[string]string{} // the only plan files this adapter opens; gold.jsonl is not among them
	for _, name := range []string{"events.jsonl", "queries.jsonl", "split_manifest.json"} {
		if raw, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			inputs[name] = exp02SHA(string(raw))
		}
	}
	manifest, _ := json.MarshalIndent(map[string]any{"protocol": "exp02-pilot-rankings-v1", "rows": len(rows), "dropped_events": dropped, "mapping": exp02Mapping, "registry_sha256": exp02Registry(t).RegistrySHA256, "inputs_read": inputs, "pooled": os.Getenv("EXP02_POOL") == "1"}, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), append(manifest, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
