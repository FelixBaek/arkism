package memoryharness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Expectations below are frozen original golden_vectors.json values, rather
// than expectations produced by this implementation or another encoder.
func TestCanonicalFrozenPrimitiveVectors(t *testing.T) {
	tests := []struct {
		id              string
		value           any
		encoded, digest string
	}{
		{"GV-CAN-01", struct {
			A int64 `json:"a"`
		}{1}, "o1{s1:ai1;}", "71428871c812f9ee5411e8332769f24e8a754d45c3d9fccfd5f1b84ff46384df"},
		{"GV-CAN-02", struct {
			A float64 `json:"a"`
		}{1}, "o1{s1:ad3ff0000000000000;}", "13642150bdfe7bfadc88afabe02c4e498936ddfc35de654f2f6bb4304e081846"},
		{"GV-CAN-03", math.Copysign(0, -1), "d0000000000000000;", "6455e75a524d3fbbc2df7f4cfb82c6de51970d4a956a5b74d1519dac92c1b059"},
		{"GV-CAN-04", "가", "s3:가", "55ff464de596e610f27f00f4acbdba8c8b2549e17dcd062eff568691122186f7"},
		{"GV-CAN-05", []int64{1, 2}, "a2[i1;i2;]", "d31afd672411c960d6894110dc76cf06e0a64d12ea42bfd625b3cd4b255c18a6"},
		{"GV-CAN-06", []int64{2, 1}, "a2[i2;i1;]", "1abcbf1e12e0c14b566cb8a204b06cef19787e540ea68e5670839f2064986702"},
		{"GV-CAN-07", struct {
			B string  `json:"b"`
			A float64 `json:"a"`
		}{"가", 1}, "o2{s1:ad3ff0000000000000;s1:bs3:가}", "cf22ab01f1922634d72f73798c82a47d3ab64fb5e11c120c8ed6084e7fa782f4"},
		{"GV-CAN-08", struct {
			A float64 `json:"a"`
			B string  `json:"b"`
		}{1, "가"}, "o2{s1:ad3ff0000000000000;s1:bs3:가}", "cf22ab01f1922634d72f73798c82a47d3ab64fb5e11c120c8ed6084e7fa782f4"},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			var out bytes.Buffer
			if err := appendCanonical(&out, reflect.ValueOf(tc.value), "$"); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.encoded {
				t.Fatalf("bytes: %x want %x", out.Bytes(), tc.encoded)
			}
			sum := sha256.Sum256(out.Bytes())
			if hex.EncodeToString(sum[:]) != tc.digest {
				t.Fatal("frozen digest mismatch")
			}
		})
	}
}

func TestCanonicalPublicScopeGolden(t *testing.T) {
	scope := Scope{Workspace: "ws", Project: "proj", User: "usr", Agent: "agt"}
	encoded, err := CanonicalBytes("Scope", scope)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "o4{s5:agents3:agts7:projects4:projs4:users3:usrs9:workspaces2:ws}"
	if string(encoded) != expected {
		t.Fatalf("bytes: %x", encoded)
	}
	digest, err := CanonicalSHA256("Scope", scope)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "482d75fa9bd7a4686fffa00746432a8b4ad50a3329cb1ffa94948fd0858991c1" {
		t.Fatal("Scope digest mismatch")
	}
	encoded[0] = 'x'
	again, err := CanonicalBytes("Scope", scope)
	if err != nil || string(again) != expected {
		t.Fatal("returned bytes alias later result")
	}
}

func TestCanonicalPublicConcreteBoundary(t *testing.T) {
	scope := Scope{Workspace: "ws", Project: "proj", User: "usr", Agent: "agt"}
	type localScope Scope
	tests := []struct {
		name, typeName string
		value          any
	}{
		{"pointer", "Scope", &scope}, {"nil", "Scope", nil}, {"typed_nil", "Scope", (*Scope)(nil)},
		{"map", "Scope", map[string]string{"workspace": "ws", "project": "proj", "user": "usr", "agent": "agt"}},
		{"other_concrete", "Scope", Ref{}}, {"named_copy", "Scope", localScope(scope)},
		{"primitive", "Scope", "ws"}, {"unknown_type", "Event", scope},
		{"wrong_case", "scope", scope}, {"unicode_identifier", "Scope", Scope{Workspace: "가", Project: "proj", User: "usr", Agent: "agt"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := CanonicalBytes(tc.typeName, tc.value); err == nil || out != nil {
				t.Fatal("invalid public input accepted")
			}
			if out, err := CanonicalSHA256(tc.typeName, tc.value); err == nil || out != "" {
				t.Fatal("invalid public hash accepted")
			}
		})
	}
}

func TestCanonicalPrimitiveEdges(t *testing.T) {
	var nullableInt *int64
	var nullableFloat *float64
	negativeZero := math.Copysign(0, -1)
	tests := []struct {
		name     string
		value    any
		expected string
	}{
		{"null", nil, "n"}, {"nullable_int", nullableInt, "n"}, {"nullable_float", nullableFloat, "n"},
		{"true", true, "t"}, {"false", false, "f"}, {"int64_max", int64(math.MaxInt64), "i9223372036854775807;"},
		{"int64_min", int64(math.MinInt64), "i-9223372036854775808;"}, {"uint64_max", uint64(math.MaxUint64), "i18446744073709551615;"},
		{"empty_array", []int64{}, "a0[]"}, {"nullable_negative_zero", &negativeZero, "d0000000000000000;"},
		{"astral_utf8", "😀", "s4:😀"}, {"decomposed", "e\u0301", "s3:e\u0301"}, {"composed", "é", "s2:é"},
		{"exact_whitespace", " A\n", "s3: A\n"}, {"embedded_nul", "a\x00b", "s3:a\x00b"},
		{"float_subnormal", math.SmallestNonzeroFloat64, "d0000000000000001;"},
		{"float_max", math.MaxFloat64, "d7fefffffffffffff;"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := appendCanonical(&out, reflect.ValueOf(tc.value), "$"); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.expected {
				t.Fatalf("bytes %x want %x", out.Bytes(), tc.expected)
			}
		})
	}
	if !math.Signbit(negativeZero) {
		t.Fatal("encoder mutated negative zero")
	}
}

func TestCanonicalPrimitiveRejects(t *testing.T) {
	// Construct the malformed schema at runtime so vet still checks production
	// struct tags without treating this intentional negative fixture as a defect.
	duplicateType := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeOf(int64(0)), Tag: `json:"a"`},
		{Name: "B", Type: reflect.TypeOf(int64(0)), Tag: `json:"a"`},
	})
	duplicate := reflect.New(duplicateType).Elem()
	duplicate.Field(0).SetInt(1)
	duplicate.Field(1).SetInt(2)
	tests := []struct {
		name  string
		value any
		code  string
	}{
		{"nan", math.NaN(), "invalid_value"}, {"positive_inf", math.Inf(1), "invalid_value"}, {"negative_inf", math.Inf(-1), "invalid_value"},
		{"invalid_utf8", string([]byte{0xff}), "invalid_unicode"}, {"surrogate_utf8", string([]byte{0xed, 0xa0, 0x80}), "invalid_unicode"},
		{"nil_slice", []int64(nil), "invalid_type"}, {"nested_nil_slice", [][]int64{nil}, "invalid_type"},
		{"float32", float32(1), "invalid_type"}, {"native_int", int(1), "invalid_type"},
		{"arbitrary_map", map[string]int64{"a": 1}, "invalid_type"},
		{"unexported", struct{ x int64 }{1}, "invalid_type"}, {"no_tag", struct{ A int64 }{1}, "invalid_type"},
		{"duplicate_tag", duplicate.Interface(), "invalid_type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := appendCanonical(&out, reflect.ValueOf(tc.value), "$")
			var ce *ContractError
			if !errors.As(err, &ce) || ce.Code != tc.code {
				t.Fatalf("error %v want %s", err, tc.code)
			}
			if strings.Contains(err.Error(), "NaN") {
				t.Fatal("input contents leaked")
			}
		})
	}
}

func TestCanonicalPublicNestedNonmutation(t *testing.T) {
	candidate := Candidate{
		Scope:   Scope{Workspace: "ws", Project: "proj", User: "usr", Agent: "agt"},
		Ref:     Ref{Origin: "memory", Collection: "memories", ID: "item", Revision: 1, ContentSHA256: strings.Repeat("a", 64)},
		Reasons: []string{"vector", "exact_keyword"}, Trust: math.Copysign(0, -1),
		ObservedSeq: 1, ContextCostUnits: 1, Authority: "data_only_untrusted",
	}
	beforeReasons := append([]string{}, candidate.Reasons...)
	encoded, err := CanonicalBytes("Candidate", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("s5:trustd0000000000000000;")) {
		t.Fatal("nested negative zero encoding")
	}
	if !bytes.Contains(encoded, []byte("s7:reasonsa2[s6:vectors13:exact_keyword]")) {
		t.Fatal("reasons array order changed")
	}
	if !math.Signbit(candidate.Trust) || !reflect.DeepEqual(candidate.Reasons, beforeReasons) {
		t.Fatal("caller value mutated")
	}
	candidate.Reasons[0] = "text_search"
	if !bytes.Contains(encoded, []byte("s6:vector")) {
		t.Fatal("result aliases caller data")
	}
	candidate.Reasons = nil
	if b, err := CanonicalBytes("Candidate", candidate); err == nil || b != nil {
		t.Fatal("nil array accepted")
	}
	candidate.Reasons = []string{}
	if _, err := CanonicalBytes("Candidate", candidate); err != nil {
		t.Fatalf("empty array: %v", err)
	}
}

// Every public concrete DTO must pass through the same validation and encoding
// boundary. The numeric and byte-level oracle is independently pinned above.
func TestCanonicalEveryConcreteDTO(t *testing.T) {
	names := []string{"Scope", "Ref", "Candidate", "CandidateSnapshot", "ContextBudget", "Probe", "Gold", "Prediction", "Policy", "ProbeFile", "CandidateFile", "GoldFile", "ArtifactEntry", "InputManifest", "EvaluationManifest", "SplitEntry", "SplitMap", "RankedCandidate", "BaselineSelection", "PredictionRun", "Statistic", "QueryMetric", "StreamMetric", "MetricCounts", "MetricSummary", "MetricRun", "ReportSummary"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			value := codecFixture(t, name)
			encoded, err := CanonicalBytes(name, value)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) == 0 || encoded[0] != 'o' {
				t.Fatal("DTO did not encode as an object")
			}
			digest, err := CanonicalSHA256(name, value)
			if err != nil {
				t.Fatal(err)
			}
			expected := sha256.Sum256(encoded)
			if digest != hex.EncodeToString(expected[:]) {
				t.Fatal("hash does not cover exact canonical bytes")
			}
			again, err := CanonicalBytes(name, value)
			if err != nil || !bytes.Equal(encoded, again) {
				t.Fatal("nondeterministic encoding")
			}
		})
	}
}
