package memoryharness

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Fixtures are handwritten independently from the compiled validation table.
func codecFixture(t *testing.T, name string) any {
	t.Helper()
	data, err := json.Marshal(codecFixtureValues()[name])
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeStrict(data, name)
	if err != nil {
		t.Fatalf("%s fixture: %v", name, err)
	}
	return value
}
func codecFixtureValues() map[string]any {
	const id = "arkism-offline-b0-v1"
	const policyID = "b0-rrf-synthetic-v1"
	hash := strings.Repeat("0", 64)
	scope := Scope{"workspace", "project", "user", "agent"}
	ref := Ref{"memory", "memories", "item", 1, hash}
	candidate := Candidate{scope, ref, []string{"vector"}, .5, 10, 1, 10, 1, true, true, false, true, "data_only_untrusted"}
	snapshot := CandidateSnapshot{"snapshot", "stream", scope, 100, 3, [][]Candidate{{}, {}, {}}}
	budget := ContextBudget{"fixture_units_v0", 3}
	probe := Probe{"query", "stream", scope, 100, 3, "snapshot", 3, budget, policyID}
	statistic := Statistic{0, 0, nil, "no_denominator"}
	summary := MetricSummary{statistic, statistic, statistic, statistic, statistic, statistic}
	counts := MetricCounts{}
	zeroFloat, zeroInt := float64(0), int64(0)
	files := func(paths ...string) []ArtifactEntry {
		out := make([]ArtifactEntry, 0, len(paths))
		for _, path := range paths {
			out = append(out, ArtifactEntry{path, hash, 0})
		}
		return out
	}
	return map[string]any{
		"Scope": scope, "Ref": ref, "Candidate": candidate, "CandidateSnapshot": snapshot, "ContextBudget": budget, "Probe": probe,
		"Gold":       Gold{"query", "stream", scope, 100, 3, "current", "relevant", []Ref{ref}},
		"Prediction": Prediction{"query", "stream", scope, "success", []Ref{}, "none"},
		"Policy":     Policy{id, policyID, 60, 2, 1, "fixture_units_v0", "validate-filter-preserve-ordinal-fuse-pack"},
		"ProbeFile":  ProbeFile{id, []Probe{}}, "CandidateFile": CandidateFile{id, []CandidateSnapshot{}}, "GoldFile": GoldFile{id, []Gold{}},
		"ArtifactEntry":      ArtifactEntry{"policy.json", hash, 0},
		"InputManifest":      InputManifest{id, "baseline_input", "unit_fixture", 0, "27bb41807ff48d47559ba2cfc30feb300caacecf", files("candidates.json", "implementation_contract.json", "policy.json", "probes.json")},
		"EvaluationManifest": EvaluationManifest{id, "baseline_evaluation", "unit_fixture", files("gold.json", "input_manifest.json", "split_map.json")},
		"SplitEntry":         SplitEntry{"stream", "root", "unit_fixture"}, "SplitMap": SplitMap{id, []SplitEntry{}},
		"RankedCandidate": RankedCandidate{candidate, 1, []int64{0}}, "BaselineSelection": BaselineSelection{[]RankedCandidate{}, 0, 0},
		"PredictionRun": PredictionRun{id, "run", policyID, hash, hash, []Prediction{}}, "Statistic": statistic,
		"QueryMetric":  QueryMetric{"query", "stream", "relevant", "success", 0, 0, 1, 0, &zeroFloat, &zeroInt},
		"StreamMetric": StreamMetric{"stream", 0, 0, statistic}, "MetricCounts": counts, "MetricSummary": summary,
		"MetricRun":     MetricRun{id, "run", policyID, hash, hash, hash, hash, counts, []QueryMetric{}, []StreamMetric{}, summary},
		"ReportSummary": ReportSummary{id, "run", policyID, hash, hash, hash, hash, "unit_fixture", "offline_contract_measurement", counts, summary},
	}
}
func codecError(t *testing.T, err error, code, path string) {
	t.Helper()
	var ce *ContractError
	if !errors.As(err, &ce) {
		t.Fatalf("want %s %s, got %v", code, path, err)
	}
	if ce.Code != code || path != "" && ce.Path != path {
		t.Fatalf("want %s %s, got %v", code, path, err)
	}
}
func codecJSON(t *testing.T, name string) string {
	t.Helper()
	data, err := json.Marshal(codecFixtureValues()[name])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCodecEveryConcreteDTO(t *testing.T) {
	if len(codecFixtureValues()) != 27 {
		t.Fatal("27 independent fixtures required")
	}
	for name, typ := range contractTypes {
		t.Run(name, func(t *testing.T) {
			value := codecFixture(t, name)
			if reflect.TypeOf(value) != typ {
				t.Fatalf("wrong concrete type %T", value)
			}
			if err := ValidateTyped(name, value); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestCodecStrictRejections(t *testing.T) {
	scope := codecJSON(t, "Scope")
	ref := codecJSON(t, "Ref")
	candidate := codecJSON(t, "Candidate")
	manifest := codecJSON(t, "InputManifest")
	tests := []struct{ name, typ, data, code, path string }{
		{"unsupported_before_json", "Unknown", "{", "unsupported_type", "$"},
		{"malformed", "Scope", "{", "invalid_json", "$"},
		{"trailing", "Scope", scope + " {}", "invalid_json", "$"},
		{"nan_literal", "Candidate", strings.Replace(candidate, `"trust":0.5`, `"trust":NaN`, 1), "invalid_json", "$"},
		{"float_overflow", "Candidate", strings.Replace(candidate, `"trust":0.5`, `"trust":1e400`, 1), "invalid_value", "$.trust"},
		{"invalid_utf8", "Scope", strings.Replace(scope, "workspace", string([]byte{255}), 1), "invalid_unicode", "$"},
		{"high_surrogate", "Scope", strings.Replace(scope, `"workspace"`, `"\ud800"`, 1), "invalid_unicode", "$"},
		{"low_surrogate", "Scope", strings.Replace(scope, `"workspace"`, `"\udc00"`, 1), "invalid_unicode", "$"},
		{"duplicate", "Scope", `{"workspace":"a","workspace":"b","project":"p","user":"u","agent":"a"}`, "duplicate_key", "$"},
		{"escaped_duplicate", "Scope", `{"workspace":"a","\u0077orkspace":"b"}`, "duplicate_key", "$"},
		{"unknown", "Scope", strings.TrimSuffix(scope, "}") + `,"private_secret":"hidden"}`, "unknown_field", "$"},
		{"missing", "Scope", `{"workspace":"w"}`, "missing_field", "$.project"},
		{"root_array", "Scope", "[]", "invalid_type", "$"},
		{"int_fraction", "Ref", strings.Replace(ref, `"revision":1`, `"revision":1.0`, 1), "invalid_type", "$.revision"},
		{"int_exponent", "Ref", strings.Replace(ref, `"revision":1`, `"revision":1e0`, 1), "invalid_type", "$.revision"},
		{"int_overflow", "Ref", strings.Replace(ref, `"revision":1`, `"revision":9223372036854775808`, 1), "invalid_value", "$.revision"},
		{"int_minimum", "Ref", strings.Replace(ref, `"revision":1`, `"revision":0`, 1), "invalid_value", "$.revision"},
		{"uint_overflow", "InputManifest", strings.Replace(manifest, `"seed":0`, `"seed":18446744073709551616`, 1), "invalid_value", "$.seed"},
		{"uint_negative", "InputManifest", strings.Replace(manifest, `"seed":0`, `"seed":-1`, 1), "invalid_value", "$.seed"},
		{"null_array", "Candidate", strings.Replace(candidate, `["vector"]`, `null`, 1), "invalid_type", "$.reasons"},
		{"duplicate_reason", "Candidate", strings.Replace(candidate, `["vector"]`, `["vector","vector"]`, 1), "invalid_value", "$.reasons"},
		{"wrong_bool", "Candidate", strings.Replace(candidate, `"approved":true`, `"approved":1`, 1), "invalid_type", "$.approved"},
		{"wrong_version", "ProbeFile", `{"contract_id":"future","probes":[]}`, "unsupported_version", "$.contract_id"},
		{"unknown_before_type", "Scope", `{"workspace":3,"project":"p","user":"u","agent":"a","secret":1}`, "unknown_field", "$"},
		{"missing_before_type", "Scope", `{"workspace":3}`, "missing_field", "$.project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeStrict([]byte(tt.data), tt.typ)
			codecError(t, err, tt.code, tt.path)
			if err != nil && (strings.Contains(err.Error(), "private_secret") || strings.Contains(err.Error(), "hidden")) {
				t.Fatal("input leaked")
			}
		})
	}
}
func TestCodecIntegerPrecision(t *testing.T) {
	tests := []struct {
		name, old, new string
		want           any
	}{
		{"Ref", `"revision":1`, `"revision":9223372036854775807`, int64(math.MaxInt64)},
		{"InputManifest", `"seed":0`, `"seed":18446744073709551615`, uint64(math.MaxUint64)},
		{"InputManifest", `"seed":0`, `"seed":-0`, uint64(0)},
	}
	for _, tt := range tests {
		t.Run(tt.new, func(t *testing.T) {
			data := []byte(strings.Replace(codecJSON(t, tt.name), tt.old, tt.new, 1))
			before := bytes.Clone(data)
			value, err := DecodeStrict(data, tt.name)
			if err != nil {
				t.Fatal(err)
			}
			var actual any
			switch v := value.(type) {
			case Ref:
				actual = v.Revision
			case InputManifest:
				actual = v.Seed
			}
			if actual != tt.want {
				t.Fatalf("lost precision: %v", actual)
			}
			if !bytes.Equal(data, before) {
				t.Fatal("mutated input")
			}
		})
	}
}
func TestCodecTypedBoundary(t *testing.T) {
	scope := codecFixture(t, "Scope").(Scope)
	for _, value := range []any{nil, &scope, map[string]string{"workspace": "w"}, Ref{}} {
		codecError(t, ValidateTyped("Scope", value), "invalid_type", "$")
	}
	c := codecFixture(t, "Candidate").(Candidate)
	c.Trust = math.NaN()
	codecError(t, ValidateTyped("Candidate", c), "invalid_value", "$.trust")
	c = codecFixture(t, "Candidate").(Candidate)
	c.Reasons = nil
	codecError(t, ValidateTyped("Candidate", c), "invalid_type", "$.reasons")
	c = codecFixture(t, "Candidate").(Candidate)
	c.Reasons = []string{}
	if err := ValidateTyped("Candidate", c); err != nil {
		t.Fatal(err)
	}
	scope.Workspace = string([]byte{255})
	codecError(t, ValidateTyped("Scope", scope), "invalid_unicode", "$.workspace")
}
func TestCodecLocalSemantics(t *testing.T) {
	tests := []struct {
		name  string
		value any
		code  string
	}{
		{"Gold", func() Gold { v := codecFixture(t, "Gold").(Gold); v.RelevantRefs = []Ref{}; return v }(), "invalid_value"},
		{"Prediction", func() Prediction { v := codecFixture(t, "Prediction").(Prediction); v.ErrorCode = "timeout"; return v }(), "invalid_value"},
		{"InputManifest", func() InputManifest {
			v := codecFixture(t, "InputManifest").(InputManifest)
			v.Files[0], v.Files[1] = v.Files[1], v.Files[0]
			return v
		}(), "invalid_value"},
		{"InputManifest", func() InputManifest {
			v := codecFixture(t, "InputManifest").(InputManifest)
			v.SourceCommit = strings.Repeat("a", 40)
			return v
		}(), "invalid_value"},
		{"SplitMap", func() SplitMap {
			v := codecFixture(t, "SplitMap").(SplitMap)
			v.Streams = []SplitEntry{{"z", "root", "unit_fixture"}, {"a", "root", "unit_fixture"}}
			return v
		}(), "schedule_mismatch"},
		{"Statistic", Statistic{1, 0, nil, "no_denominator"}, "invalid_value"},
		{"QueryMetric", func() QueryMetric {
			v := codecFixture(t, "QueryMetric").(QueryMetric)
			v.LabelStatus = "ambiguous"
			return v
		}(), "invalid_value"},
	}
	for i, tt := range tests {
		t.Run(tt.name+string(rune('a'+i)), func(t *testing.T) { codecError(t, ValidateTyped(tt.name, tt.value), tt.code, "") })
	}
	half := .5
	if err := ValidateTyped("Statistic", Statistic{1, 2, &half, "none"}); err != nil {
		t.Fatal(err)
	}
	half = .4
	codecError(t, ValidateTyped("Statistic", Statistic{1, 2, &half, "none"}), "invalid_value", "$.value")
}
func TestCodecSnapshotBindings(t *testing.T) {
	c := codecFixture(t, "Candidate").(Candidate)
	mutate := []struct {
		name  string
		apply func(*Candidate)
	}{
		{"scope", func(c *Candidate) { c.Scope.Agent = "other" }},
		{"future_observation", func(c *Candidate) { c.ObservedSeq = 4 }},
		{"future_effective", func(c *Candidate) { c.EffectiveAtNS = 101 }},
		{"future_updated", func(c *Candidate) { c.UpdatedAtNS = 101 }},
		{"revision", func(c *Candidate) { c.Ref.Revision++ }},
		{"hash", func(c *Candidate) { c.Ref.ContentSHA256 = strings.Repeat("a", 64) }},
		{"approved", func(c *Candidate) { c.Approved = false }},
		{"active", func(c *Candidate) { c.Active = false }},
		{"deleted", func(c *Candidate) { c.Deleted = true }},
		{"verified", func(c *Candidate) { c.SourceVerified = false }},
		{"cost", func(c *Candidate) { c.ContextCostUnits++ }},
		{"observed", func(c *Candidate) { c.ObservedSeq++ }},
		{"effective", func(c *Candidate) { c.EffectiveAtNS++ }},
	}
	for _, tt := range mutate {
		t.Run(tt.name, func(t *testing.T) {
			s := codecFixture(t, "CandidateSnapshot").(CandidateSnapshot)
			changed := c
			tt.apply(&changed)
			s.Lanes = [][]Candidate{{c}, {changed}, {}}
			codecError(t, ValidateTyped("CandidateSnapshot", s), "snapshot_conflict", "$")
		})
	}
	s := codecFixture(t, "CandidateSnapshot").(CandidateSnapshot)
	other := c
	other.Trust = .7
	other.UpdatedAtNS = 20
	other.Reasons = []string{"text_search"}
	s.Lanes = [][]Candidate{{c}, {other}, {}}
	if err := ValidateTyped("CandidateSnapshot", s); err != nil {
		t.Fatal(err)
	}
	s.Lanes[2] = nil
	codecError(t, ValidateTyped("CandidateSnapshot", s), "invalid_type", "$.lanes[*]")
}
func TestBaselineInputsBindings(t *testing.T) {
	policy := codecFixture(t, "Policy").(Policy)
	fixture := func() (ProbeFile, CandidateFile) {
		p := codecFixture(t, "ProbeFile").(ProbeFile)
		p.Probes = []Probe{codecFixture(t, "Probe").(Probe)}
		c := codecFixture(t, "CandidateFile").(CandidateFile)
		c.Snapshots = []CandidateSnapshot{codecFixture(t, "CandidateSnapshot").(CandidateSnapshot)}
		return p, c
	}
	p, c := fixture()
	if err := ValidateBaselineInputs(p, c, policy); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, code string
		mutate     func(*ProbeFile, *CandidateFile)
	}{
		{"missing", "schedule_mismatch", func(p *ProbeFile, c *CandidateFile) { c.Snapshots = []CandidateSnapshot{} }},
		{"extra", "schedule_mismatch", func(p *ProbeFile, c *CandidateFile) {
			s := c.Snapshots[0]
			s.SnapshotID = "extra"
			c.Snapshots = append(c.Snapshots, s)
		}},
		{"stream", "snapshot_binding", func(p *ProbeFile, c *CandidateFile) { c.Snapshots[0].StreamID = "other" }},
		{"scope", "snapshot_binding", func(p *ProbeFile, c *CandidateFile) { c.Snapshots[0].Scope.Agent = "other" }},
		{"time", "snapshot_binding", func(p *ProbeFile, c *CandidateFile) { c.Snapshots[0].AsOfNS++ }},
		{"seq", "snapshot_binding", func(p *ProbeFile, c *CandidateFile) { c.Snapshots[0].AsOfSeq++ }},
		{"duplicate_query", "schedule_mismatch", func(p *ProbeFile, c *CandidateFile) { p.Probes = append(p.Probes, p.Probes[0]) }},
		{"duplicate_snapshot", "schedule_mismatch", func(p *ProbeFile, c *CandidateFile) { c.Snapshots = append(c.Snapshots, c.Snapshots[0]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, c := fixture()
			tt.mutate(&p, &c)
			codecError(t, ValidateBaselineInputs(p, c, policy), tt.code, "")
		})
	}
	p, c = fixture()
	ineligible := codecFixture(t, "Candidate").(Candidate)
	ineligible.Approved = false
	c.Snapshots[0].Lanes[0] = []Candidate{ineligible}
	if err := ValidateBaselineInputs(p, c, policy); err != nil {
		t.Fatal(err)
	}
	if len(c.Snapshots[0].Lanes[0]) != 1 {
		t.Fatal("filtered input")
	}
}

func TestCodecAdditionalRawBoundaries(t *testing.T) {
	tests := []struct{ name, typ, old, new, code, path string }{
		{"nested_unknown", "Candidate", `"workspace":"workspace"`, `"workspace":"workspace","private_key":42`, "unknown_field", "$.scope"},
		{"nested_missing", "Candidate", `"workspace":"workspace",`, ``, "missing_field", "$.scope.workspace"},
		{"null_scope", "Candidate", `"scope":{"workspace":"workspace","project":"project","user":"user","agent":"agent"}`, `"scope":null`, "invalid_type", "$.scope"},
		{"surrogate_pair_valid_unicode_invalid_id", "Scope", `"workspace":"workspace"`, `"workspace":"\ud83d\ude00"`, "invalid_value", "$.workspace"},
		{"escaped_backslash_not_surrogate", "Scope", `"workspace":"workspace"`, `"workspace":"\\ud800"`, "invalid_value", "$.workspace"},
		{"no_trim", "Scope", `"workspace":"workspace"`, `"workspace":" workspace"`, "invalid_value", "$.workspace"},
		{"no_casefold", "Scope", `"workspace":"workspace"`, `"workspace":"Workspace"`, "invalid_value", "$.workspace"},
		{"outer_lane_count", "CandidateSnapshot", `"lanes":[[],[],[]]`, `"lanes":[[],[]]`, "invalid_value", "$.lanes"},
		{"null_inner_lane", "CandidateSnapshot", `"lanes":[[],[],[]]`, `"lanes":[[],null,[]]`, "invalid_type", "$.lanes[*]"},
		{"null_root", "Scope", `{"workspace":"workspace","project":"project","user":"user","agent":"agent"}`, `null`, "invalid_type", "$"},
		{"bool_integer", "ContextBudget", `"limit":3`, `"limit":true`, "invalid_type", "$.limit"},
		{"int_lower_bound", "ArtifactEntry", `"byte_length":0`, `"byte_length":-1`, "invalid_value", "$.byte_length"},
		{"const_int_lexical", "Policy", `"rank_offset":60`, `"rank_offset":60.0`, "invalid_type", "$.rank_offset"},
		{"const_float_wrong", "Policy", `"other_weight":1`, `"other_weight":1.5`, "invalid_value", "$.other_weight"},
		{"enum_case", "Ref", `"origin":"memory"`, `"origin":"MEMORY"`, "invalid_value", "$.origin"},
		{"hash_case", "Ref", strings.Repeat("0", 64), strings.Repeat("A", 64), "invalid_value", "$.content_sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := codecJSON(t, tt.typ)
			if !strings.Contains(data, tt.old) {
				t.Fatalf("fixture mutation missing %s", tt.old)
			}
			_, err := DecodeStrict([]byte(strings.Replace(data, tt.old, tt.new, 1)), tt.typ)
			codecError(t, err, tt.code, tt.path)
		})
	}
}
func TestCodecPhasePrecedence(t *testing.T) {
	candidate := codecJSON(t, "Candidate")
	t.Run("unicode_before_shape", func(t *testing.T) {
		data := strings.TrimSuffix(candidate, "}") + `,"unknown":"\ud800"}`
		_, err := DecodeStrict([]byte(data), "Candidate")
		codecError(t, err, "invalid_unicode", "$")
	})
	t.Run("duplicate_before_unknown", func(t *testing.T) {
		data := strings.TrimSuffix(candidate, "}") + `,"unknown":{},"unknown":{}}`
		_, err := DecodeStrict([]byte(data), "Candidate")
		codecError(t, err, "duplicate_key", "$")
	})
	t.Run("nested_unknown_before_outer_missing", func(t *testing.T) {
		data := strings.Replace(candidate, `"trust":0.5,`, "", 1)
		data = strings.Replace(data, `"workspace":"workspace"`, `"workspace":"workspace","unknown":0`, 1)
		_, err := DecodeStrict([]byte(data), "Candidate")
		codecError(t, err, "unknown_field", "$.scope")
	})
	t.Run("types_before_semantics", func(t *testing.T) {
		v := codecFixture(t, "CandidateSnapshot").(CandidateSnapshot)
		c := codecFixture(t, "Candidate").(Candidate)
		c.Scope.Agent = "other"
		v.Lanes[0] = []Candidate{c}
		c.Trust = 2
		v.Lanes[2] = []Candidate{c}
		codecError(t, ValidateTyped("CandidateSnapshot", v), "invalid_value", "$.lanes[*][*].trust")
	})
}
func TestCodecDecodeAllocationAndZero(t *testing.T) {
	data := []byte(codecJSON(t, "Candidate"))
	first, err := DecodeStrict(data, "Candidate")
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeStrict(data, "Candidate")
	if err != nil {
		t.Fatal(err)
	}
	a := first.(Candidate)
	b := second.(Candidate)
	a.Reasons[0] = "text_search"
	if b.Reasons[0] != "vector" {
		t.Fatal("decodes alias slices")
	}
	data = []byte(strings.Replace(codecJSON(t, "Candidate"), `"trust":0.5`, `"trust":-0.0`, 1))
	value, err := DecodeStrict(data, "Candidate")
	if err != nil {
		t.Fatal(err)
	}
	if math.Signbit(value.(Candidate).Trust) {
		t.Fatal("negative float zero not normalized")
	}
}
func TestCodecUniqueReferences(t *testing.T) {
	g := codecFixture(t, "Gold").(Gold)
	g.RelevantRefs = append(g.RelevantRefs, g.RelevantRefs[0])
	codecError(t, ValidateTyped("Gold", g), "invalid_value", "$.relevant_refs")
	g.RelevantRefs[1].Revision++
	if err := ValidateTyped("Gold", g); err != nil {
		t.Fatal("uniqueness must use full Ref", err)
	}
	r := codecFixture(t, "RankedCandidate").(RankedCandidate)
	r.Lanes = []int64{0, 0}
	codecError(t, ValidateTyped("RankedCandidate", r), "invalid_value", "$.lanes")
	r.Lanes = []int64{3}
	codecError(t, ValidateTyped("RankedCandidate", r), "invalid_value", "$.lanes[*]")
}
