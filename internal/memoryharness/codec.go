package memoryharness

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// codecField is one entry of this closed, compiled contract. It is not an
// extensible schema API and no schema file is loaded at runtime.
type codecField struct {
	name, kind, object, constantString string
	enums                              []string
	minimum, maximum                   float64
	hasMinimum, hasMaximum             bool
	constantInt                        int64
	constantFloat                      float64
	minItems, maxItems, outerItems     int
	unique                             bool
	itemMin, itemMax                   int64
	hasItemBounds                      bool
}

var codecFields = map[string][]codecField{
	"Scope": {
		{name: "workspace", kind: "id"},
		{name: "project", kind: "id"},
		{name: "user", kind: "id"},
		{name: "agent", kind: "id"},
	},
	"Ref": {
		{name: "origin", kind: "enum", enums: []string{"memory", "fixture_alt"}},
		{name: "collection", kind: "enum", enums: []string{"memories"}},
		{name: "id", kind: "id"},
		{name: "revision", kind: "int64", minimum: 1, hasMinimum: true},
		{name: "content_sha256", kind: "sha256"},
	},
	"Candidate": {
		{name: "scope", kind: "object", object: "Scope"},
		{name: "ref", kind: "object", object: "Ref"},
		{name: "reasons", kind: "array", enums: []string{"exact_keyword", "text_search", "vector"}, minItems: 0, unique: true},
		{name: "trust", kind: "float64", minimum: 0, hasMinimum: true, maximum: 1, hasMaximum: true},
		{name: "updated_at_ns", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "observed_seq", kind: "int64", minimum: 1, hasMinimum: true},
		{name: "effective_at_ns", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "context_cost_units", kind: "int64", minimum: 1, hasMinimum: true, maximum: 1000000, hasMaximum: true},
		{name: "approved", kind: "bool"},
		{name: "active", kind: "bool"},
		{name: "deleted", kind: "bool"},
		{name: "source_verified", kind: "bool"},
		{name: "authority", kind: "const_string", constantString: "data_only_untrusted"},
	},
	"CandidateSnapshot": {
		{name: "snapshot_id", kind: "id"},
		{name: "stream_id", kind: "id"},
		{name: "scope", kind: "object", object: "Scope"},
		{name: "as_of_ns", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "as_of_seq", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "lanes", kind: "array_of_arrays", object: "Candidate", outerItems: 3},
	},
	"ContextBudget": {
		{name: "unit", kind: "const_string", constantString: "fixture_units_v0"},
		{name: "limit", kind: "int64", minimum: 0, hasMinimum: true, maximum: 1000000, hasMaximum: true},
	},
	"Probe": {
		{name: "query_id", kind: "id"},
		{name: "stream_id", kind: "id"},
		{name: "scope", kind: "object", object: "Scope"},
		{name: "at_ns", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "cutoff_accepted_seq", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "candidate_snapshot_id", kind: "id"},
		{name: "final_k", kind: "int64", minimum: 0, hasMinimum: true, maximum: 64, hasMaximum: true},
		{name: "context_budget", kind: "object", object: "ContextBudget"},
		{name: "policy_id", kind: "const_string", constantString: "b0-rrf-synthetic-v1"},
	},
	"Gold": {
		{name: "query_id", kind: "id"},
		{name: "stream_id", kind: "id"},
		{name: "scope", kind: "object", object: "Scope"},
		{name: "at_ns", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "cutoff_accepted_seq", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "temporal_kind", kind: "enum", enums: []string{"current", "historical", "order", "none"}},
		{name: "label_status", kind: "enum", enums: []string{"relevant", "clear_negative", "ambiguous"}},
		{name: "relevant_refs", kind: "array", object: "Ref", minItems: 0, unique: true},
	},
	"Prediction": {
		{name: "query_id", kind: "id"},
		{name: "stream_id", kind: "id"},
		{name: "scope", kind: "object", object: "Scope"},
		{name: "status", kind: "enum", enums: []string{"success", "timeout", "error"}},
		{name: "selected_refs", kind: "array", object: "Ref", minItems: 0},
		{name: "error_code", kind: "enum", enums: []string{"none", "timeout", "execution_error"}},
	},
	"Policy": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "policy_id", kind: "const_string", constantString: "b0-rrf-synthetic-v1"},
		{name: "rank_offset", kind: "const_int", constantInt: 60},
		{name: "exact_reason_weight", kind: "const_float", constantFloat: 2.0},
		{name: "other_weight", kind: "const_float", constantFloat: 1.0},
		{name: "context_unit", kind: "const_string", constantString: "fixture_units_v0"},
		{name: "eligibility_order", kind: "const_string", constantString: "validate-filter-preserve-ordinal-fuse-pack"},
	},
	"ProbeFile": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "probes", kind: "array", object: "Probe", minItems: 0},
	},
	"CandidateFile": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "snapshots", kind: "array", object: "CandidateSnapshot", minItems: 0},
	},
	"GoldFile": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "labels", kind: "array", object: "Gold", minItems: 0},
	},
	"ArtifactEntry": {
		{name: "logical_path", kind: "enum", enums: []string{"implementation_contract.json", "policy.json", "probes.json", "candidates.json", "input_manifest.json", "gold.json", "split_map.json"}},
		{name: "sha256", kind: "sha256"},
		{name: "byte_length", kind: "int64", minimum: 0, hasMinimum: true},
	},
	"InputManifest": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "manifest_kind", kind: "const_string", constantString: "baseline_input"},
		{name: "dataset_role", kind: "const_string", constantString: "unit_fixture"},
		{name: "seed", kind: "uint64", minimum: 0, hasMinimum: true},
		{name: "source_commit", kind: "hex40"},
		{name: "files", kind: "array", object: "ArtifactEntry", minItems: 0},
	},
	"EvaluationManifest": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "manifest_kind", kind: "const_string", constantString: "baseline_evaluation"},
		{name: "dataset_role", kind: "const_string", constantString: "unit_fixture"},
		{name: "files", kind: "array", object: "ArtifactEntry", minItems: 0},
	},
	"SplitEntry": {
		{name: "stream_id", kind: "id"},
		{name: "root_stream_id", kind: "id"},
		{name: "split_role", kind: "const_string", constantString: "unit_fixture"},
	},
	"SplitMap": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "streams", kind: "array", object: "SplitEntry", minItems: 0},
	},
	"RankedCandidate": {
		{name: "candidate", kind: "object", object: "Candidate"},
		{name: "score", kind: "float64", minimum: 0, hasMinimum: true},
		{name: "lanes", kind: "array", object: "int64", minItems: 1, maxItems: 3, unique: true, itemMin: 0, itemMax: 2, hasItemBounds: true},
	},
	"BaselineSelection": {
		{name: "selected", kind: "array", object: "RankedCandidate", minItems: 0},
		{name: "used_context_units", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "eligible_unique_count", kind: "int64", minimum: 0, hasMinimum: true},
	},
	"PredictionRun": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "run_id", kind: "id"},
		{name: "policy_id", kind: "const_string", constantString: "b0-rrf-synthetic-v1"},
		{name: "input_manifest_sha256", kind: "sha256"},
		{name: "policy_sha256", kind: "sha256"},
		{name: "predictions", kind: "array", object: "Prediction", minItems: 0},
	},
	"Statistic": {
		{name: "numerator", kind: "float64", minimum: 0, hasMinimum: true},
		{name: "denominator", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "value", kind: "nullable_float64", minimum: 0, hasMinimum: true, maximum: 1, hasMaximum: true},
		{name: "null_reason", kind: "enum", enums: []string{"none", "no_denominator"}},
	},
	"QueryMetric": {
		{name: "query_id", kind: "id"},
		{name: "stream_id", kind: "id"},
		{name: "label_status", kind: "enum", enums: []string{"relevant", "clear_negative", "ambiguous"}},
		{name: "status", kind: "enum", enums: []string{"success", "timeout", "error"}},
		{name: "raw_selected_count", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "unique_selected_count", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "relevant_count", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "matched_count", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "recall_at_k", kind: "nullable_float64", minimum: 0, hasMinimum: true, maximum: 1, hasMaximum: true},
		{name: "hit_any", kind: "nullable_int64", minimum: 0, hasMinimum: true, maximum: 1, hasMaximum: true},
	},
	"StreamMetric": {
		{name: "stream_id", kind: "id"},
		{name: "scheduled_queries", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "relevant_queries", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "recall_at_k", kind: "object", object: "Statistic"},
	},
	"MetricCounts": {
		{name: "scheduled_queries", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "streams", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "relevant_queries", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "clear_negative_queries", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "ambiguous_queries", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "successful_queries", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "failed_queries", kind: "int64", minimum: 0, hasMinimum: true},
		{name: "streams_without_relevant", kind: "int64", minimum: 0, hasMinimum: true},
	},
	"MetricSummary": {
		{name: "stream_macro_recall_at_k", kind: "object", object: "Statistic"},
		{name: "query_weighted_recall_at_k", kind: "object", object: "Statistic"},
		{name: "query_hit_any_rate", kind: "object", object: "Statistic"},
		{name: "all_query_failure_rate", kind: "object", object: "Statistic"},
		{name: "observed_negative_false_positive_rate", kind: "object", object: "Statistic"},
		{name: "negative_service_failure_rate", kind: "object", object: "Statistic"},
	},
	"MetricRun": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "run_id", kind: "id"},
		{name: "policy_id", kind: "const_string", constantString: "b0-rrf-synthetic-v1"},
		{name: "input_manifest_sha256", kind: "sha256"},
		{name: "policy_sha256", kind: "sha256"},
		{name: "predictions_sha256", kind: "sha256"},
		{name: "evaluation_manifest_sha256", kind: "sha256"},
		{name: "counts", kind: "object", object: "MetricCounts"},
		{name: "queries", kind: "array", object: "QueryMetric", minItems: 0},
		{name: "streams", kind: "array", object: "StreamMetric", minItems: 0},
		{name: "summary", kind: "object", object: "MetricSummary"},
	},
	"ReportSummary": {
		{name: "contract_id", kind: "const_string", constantString: "arkism-offline-b0-v1"},
		{name: "run_id", kind: "id"},
		{name: "policy_id", kind: "const_string", constantString: "b0-rrf-synthetic-v1"},
		{name: "input_manifest_sha256", kind: "sha256"},
		{name: "predictions_sha256", kind: "sha256"},
		{name: "evaluation_manifest_sha256", kind: "sha256"},
		{name: "metrics_sha256", kind: "sha256"},
		{name: "dataset_role", kind: "const_string", constantString: "unit_fixture"},
		{name: "status", kind: "const_string", constantString: "offline_contract_measurement"},
		{name: "counts", kind: "object", object: "MetricCounts"},
		{name: "summary", kind: "object", object: "MetricSummary"},
	},
}

// ContractFileSHA256 pins the exact, immutable baseline contract bytes.
const ContractFileSHA256 = "04463d838f397772743c5740a346230eb8e9db60db8dddc824d52e1f083790ef"

const initialSourceCommit = "27bb41807ff48d47559ba2cfc30feb300caacecf"

var codecID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var codecHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var codecCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var codecInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

type codecMember struct {
	name  string
	value *codecNode
}

type codecNode struct {
	kind    byte
	value   any
	members []codecMember
	items   []*codecNode
}

// DecodeStrict accepts exactly one JSON object of one of the 27 compiled types.
// All syntax/Unicode and shape passes precede field and semantic validation.
func DecodeStrict(data []byte, typeName string) (any, error) {
	typ, ok := contractTypes[typeName]
	if !ok {
		return nil, contractError("unsupported_type", "$")
	}
	if !utf8.Valid(data) {
		return nil, contractError("invalid_unicode", "$")
	}
	if !json.Valid(data) {
		return nil, contractError("invalid_json", "$")
	}
	if !codecUnicodeEscapes(data) {
		return nil, contractError("invalid_unicode", "$")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	node, err := codecReadNode(decoder)
	if err != nil {
		return nil, contractError("invalid_json", "$")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, contractError("invalid_json", "$")
	}
	if codecDuplicate(node) {
		return nil, contractError("duplicate_key", "$")
	}
	for _, unknown := range []bool{true, false} {
		if err = codecShape(node, typeName, "$", unknown); err != nil {
			return nil, err
		}
	}
	if err = codecRawObject(node, typeName, "$"); err != nil {
		return nil, err
	}
	target := codecConcrete(node, typeName)
	if target.Type() != typ {
		return nil, contractError("invalid_type", "$")
	}
	value := target.Interface()
	if err = ValidateTyped(typeName, value); err != nil {
		return nil, err
	}
	return value, nil
}

// Conversion happens only after validation and uses the declared numeric type.
// In particular uint64 -0 is the valid integer zero, not a float intermediate.
func codecConcrete(node *codecNode, name string) reflect.Value {
	value := reflect.New(contractTypes[name]).Elem()
	for i, field := range codecFields[name] {
		codecAssign(value.Field(i), codecChild(node, field.name), field)
	}
	return value
}

func codecAssign(value reflect.Value, node *codecNode, field codecField) {
	if strings.HasPrefix(field.kind, "nullable_") {
		if node.kind == 'n' {
			return
		}
		value.Set(reflect.New(value.Type().Elem()))
		value = value.Elem()
		field.kind = strings.TrimPrefix(field.kind, "nullable_")
	}
	switch field.kind {
	case "object":
		value.Set(codecConcrete(node, field.object))
	case "array", "array_of_arrays":
		value.Set(reflect.MakeSlice(value.Type(), len(node.items), len(node.items)))
		for i, item := range node.items {
			if field.kind == "array_of_arrays" {
				codecAssign(value.Index(i), item, codecField{kind: "array", object: field.object})
			} else if field.object != "" && field.object != "int64" {
				value.Index(i).Set(codecConcrete(item, field.object))
			} else if field.object == "int64" {
				codecAssign(value.Index(i), item, codecField{kind: "int64"})
			} else {
				value.Index(i).SetString(item.value.(string))
			}
		}
	case "bool":
		value.SetBool(node.value.(bool))
	case "int64", "const_int":
		n, _ := strconv.ParseInt(string(node.value.(json.Number)), 10, 64)
		value.SetInt(n)
	case "uint64":
		raw := string(node.value.(json.Number))
		if raw == "-0" {
			raw = "0"
		}
		n, _ := strconv.ParseUint(raw, 10, 64)
		value.SetUint(n)
	case "float64", "const_float":
		n, _ := strconv.ParseFloat(string(node.value.(json.Number)), 64)
		if n == 0 {
			n = 0
		} // Normalize IEEE754 negative zero on decoding.
		value.SetFloat(n)
	default:
		value.SetString(node.value.(string))
	}
}

func codecUnicodeEscapes(data []byte) bool {
	inString := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		n, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n < 0xd800 || n > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

func codecReadNode(decoder *json.Decoder) (*codecNode, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	n := &codecNode{value: token}
	switch value := token.(type) {
	case json.Delim:
		if value == '{' {
			n.kind = 'o'
			for decoder.More() {
				key, e := decoder.Token()
				if e != nil {
					return nil, e
				}
				child, e := codecReadNode(decoder)
				if e != nil {
					return nil, e
				}
				n.members = append(n.members, codecMember{name: key.(string), value: child})
			}
		} else {
			n.kind = 'a'
			for decoder.More() {
				child, e := codecReadNode(decoder)
				if e != nil {
					return nil, e
				}
				n.items = append(n.items, child)
			}
		}
		_, err = decoder.Token()
	case string:
		n.kind = 's'
	case json.Number:
		n.kind = 'd'
	case bool:
		n.kind = 'b'
	case nil:
		n.kind = 'n'
	}
	return n, err
}

func codecDuplicate(n *codecNode) bool {
	seen := map[string]bool{}
	for _, member := range n.members {
		if seen[member.name] || codecDuplicate(member.value) {
			return true
		}
		seen[member.name] = true
	}
	for _, item := range n.items {
		if codecDuplicate(item) {
			return true
		}
	}
	return false
}

func codecChild(n *codecNode, name string) *codecNode {
	for _, member := range n.members {
		if member.name == name {
			return member.value
		}
	}
	return nil
}

func codecShape(n *codecNode, name, path string, unknown bool) error {
	if n.kind != 'o' {
		return nil
	}
	fields := codecFields[name]
	if unknown {
		allowed := map[string]bool{}
		for _, field := range fields {
			allowed[field.name] = true
		}
		for _, member := range n.members {
			if !allowed[member.name] {
				return contractError("unknown_field", path)
			}
		}
	} else {
		for _, field := range fields {
			if codecChild(n, field.name) == nil {
				return contractError("missing_field", path+"."+field.name)
			}
		}
	}
	for _, field := range fields {
		child := codecChild(n, field.name)
		if child == nil {
			continue
		}
		childPath := path + "." + field.name
		if err := codecChildShape(child, field, childPath, unknown); err != nil {
			return err
		}
	}
	return nil
}

func codecChildShape(child *codecNode, field codecField, path string, unknown bool) error {
	if field.kind == "object" {
		if err := codecShape(child, field.object, path, unknown); err != nil {
			return err
		}
	} else if child.kind == 'a' && field.object != "" && field.object != "int64" {
		for _, item := range child.items {
			if field.kind == "array_of_arrays" && item.kind == 'a' {
				for _, inner := range item.items {
					if err := codecShape(inner, field.object, path+"[*][*]", unknown); err != nil {
						return err
					}
				}
			} else if field.kind == "array" {
				if err := codecShape(item, field.object, path+"[*]", unknown); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func codecRawObject(n *codecNode, name, path string) error {
	if n.kind != 'o' {
		return contractError("invalid_type", path)
	}
	for _, field := range codecFields[name] {
		child := codecChild(n, field.name)
		if child == nil {
			return contractError("missing_field", path+"."+field.name)
		}
		if err := codecRawField(child, field, path+"."+field.name); err != nil {
			return err
		}
	}
	return nil
}

func codecRawField(n *codecNode, field codecField, path string) error {
	if strings.HasPrefix(field.kind, "nullable_") {
		if n.kind == 'n' {
			return nil
		}
		field.kind = strings.TrimPrefix(field.kind, "nullable_")
	}
	switch field.kind {
	case "object":
		return codecRawObject(n, field.object, path)
	case "array", "array_of_arrays":
		return codecRawArray(n, field, path)
	case "bool":
		if n.kind != 'b' {
			return contractError("invalid_type", path)
		}
		return nil
	case "int64", "uint64", "const_int":
		return codecRawInteger(n, field, path)
	case "float64", "const_float":
		if n.kind != 'd' {
			return contractError("invalid_type", path)
		}
		value, err := strconv.ParseFloat(string(n.value.(json.Number)), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return contractError("invalid_value", path)
		}
		if field.kind == "const_float" && value != field.constantFloat {
			return contractError("invalid_value", path)
		}
		return codecBounds(value, field, path)
	default:
		if n.kind != 's' {
			return contractError("invalid_type", path)
		}
		return codecString(n.value.(string), field, path)
	}
}

func codecRawInteger(n *codecNode, field codecField, path string) error {
	if n.kind != 'd' {
		return contractError("invalid_type", path)
	}
	raw := string(n.value.(json.Number))
	if !codecInteger.MatchString(raw) {
		return contractError("invalid_type", path)
	}
	if field.kind == "uint64" {
		if raw == "-0" {
			raw = "0"
		}
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return contractError("invalid_value", path)
		}
		return codecBounds(float64(value), field, path)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return contractError("invalid_value", path)
	}
	if field.kind == "const_int" && value != field.constantInt {
		return contractError("invalid_value", path)
	}
	return codecBounds(float64(value), field, path)
}

func codecRawArray(n *codecNode, field codecField, path string) error {
	if n.kind != 'a' {
		return contractError("invalid_type", path)
	}
	if err := codecArrayLength(len(n.items), field, path); err != nil {
		return err
	}
	for _, item := range n.items {
		if field.kind == "array_of_arrays" {
			if item.kind != 'a' {
				return contractError("invalid_type", path+"[*]")
			}
			for _, inner := range item.items {
				if err := codecRawObject(inner, field.object, path+"[*][*]"); err != nil {
					return err
				}
			}
		} else if field.object != "" && field.object != "int64" {
			if err := codecRawObject(item, field.object, path+"[*]"); err != nil {
				return err
			}
		} else {
			rule := codecField{kind: "enum", enums: field.enums}
			if field.object == "int64" {
				rule = codecField{kind: "int64", minimum: float64(field.itemMin), maximum: float64(field.itemMax), hasMinimum: field.hasItemBounds, hasMaximum: field.hasItemBounds}
			}
			if err := codecRawField(item, rule, path+"[*]"); err != nil {
				return err
			}
		}
	}
	return nil // Concrete uniqueness is checked after exact decoding.
}

func codecString(value string, field codecField, path string) error {
	if !utf8.ValidString(value) {
		return contractError("invalid_unicode", path)
	}
	var valid bool
	switch field.kind {
	case "id":
		valid = codecID.MatchString(value)
	case "sha256":
		valid = codecHash.MatchString(value)
	case "hex40":
		valid = codecCommit.MatchString(value)
	case "enum":
		valid = false
		for _, allowed := range field.enums {
			if value == allowed {
				valid = true
				break
			}
		}
	case "const_string":
		valid = value == field.constantString
	default:
		valid = false
	}
	if !valid {
		if field.name == "contract_id" {
			return contractError("unsupported_version", path)
		}
		return contractError("invalid_value", path)
	}
	return nil
}

func codecBounds(value float64, field codecField, path string) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || field.hasMinimum && value < field.minimum || field.hasMaximum && value > field.maximum {
		return contractError("invalid_value", path)
	}
	return nil
}

func codecArrayLength(length int, field codecField, path string) error {
	if length < field.minItems || field.maxItems > 0 && length > field.maxItems || field.outerItems > 0 && length != field.outerItems {
		return contractError("invalid_value", path)
	}
	return nil
}

// ValidateTyped accepts only the exact named concrete value. Pointers, maps and
// structurally similar user types are not implicit conversions.
func ValidateTyped(typeName string, value any) error {
	typ, ok := contractTypes[typeName]
	if !ok {
		return contractError("unsupported_type", "$")
	}
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || rv.Type() != typ {
		return contractError("invalid_type", "$")
	}
	if err := codecTypedUnicode(rv, "$"); err != nil {
		return err
	}
	if err := codecTypedObject(rv, typeName, "$"); err != nil {
		return err
	}
	return codecSemanticTree(rv, typeName, "$")
}

func codecTypedUnicode(value reflect.Value, path string) error {
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return contractError("invalid_unicode", path)
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if err := codecTypedUnicode(value.Field(i), path+"."+value.Type().Field(i).Tag.Get("json")); err != nil {
				return err
			}
		}
	case reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			if err := codecTypedUnicode(value.Index(i), path+"[*]"); err != nil {
				return err
			}
		}
	}
	return nil
}

func codecTypedObject(value reflect.Value, name, path string) error {
	if value.Type() != contractTypes[name] {
		return contractError("invalid_type", path)
	}
	for i, field := range codecFields[name] {
		if err := codecTypedField(value.Field(i), field, path+"."+field.name); err != nil {
			return err
		}
	}
	return nil
}

func codecTypedField(value reflect.Value, field codecField, path string) error {
	if strings.HasPrefix(field.kind, "nullable_") {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
		field.kind = strings.TrimPrefix(field.kind, "nullable_")
	}
	switch field.kind {
	case "object":
		return codecTypedObject(value, field.object, path)
	case "array", "array_of_arrays":
		return codecTypedArray(value, field, path)
	case "bool":
		return nil
	case "uint64":
		return codecBounds(float64(value.Uint()), field, path)
	case "int64", "const_int":
		if field.kind == "const_int" && value.Int() != field.constantInt {
			return contractError("invalid_value", path)
		}
		return codecBounds(float64(value.Int()), field, path)
	case "float64", "const_float":
		if field.kind == "const_float" && value.Float() != field.constantFloat {
			return contractError("invalid_value", path)
		}
		return codecBounds(value.Float(), field, path)
	default:
		return codecString(value.String(), field, path)
	}
}

func codecTypedArray(value reflect.Value, field codecField, path string) error {
	if value.IsNil() {
		return contractError("invalid_type", path)
	}
	if err := codecArrayLength(value.Len(), field, path); err != nil {
		return err
	}
	seen := map[any]bool{}
	for i := 0; i < value.Len(); i++ {
		item := value.Index(i)
		if field.kind == "array_of_arrays" {
			if item.IsNil() {
				return contractError("invalid_type", path+"[*]")
			}
			for j := 0; j < item.Len(); j++ {
				if err := codecTypedObject(item.Index(j), field.object, path+"[*][*]"); err != nil {
					return err
				}
			}
		} else if field.object != "" && field.object != "int64" {
			if err := codecTypedObject(item, field.object, path+"[*]"); err != nil {
				return err
			}
		} else if field.object == "int64" {
			if err := codecBounds(float64(item.Int()), codecField{minimum: float64(field.itemMin), maximum: float64(field.itemMax), hasMinimum: field.hasItemBounds, hasMaximum: field.hasItemBounds}, path+"[*]"); err != nil {
				return err
			}
		} else if err := codecString(item.String(), codecField{kind: "enum", enums: field.enums}, path+"[*]"); err != nil {
			return err
		}
		if field.unique {
			key := item.Interface()
			if seen[key] {
				return contractError("invalid_value", path)
			}
			seen[key] = true
		}
	}
	return nil
}

func codecSemanticTree(value reflect.Value, name, path string) error {
	for i, field := range codecFields[name] {
		child := value.Field(i)
		at := path + "." + field.name
		if field.kind == "object" {
			if err := codecSemanticTree(child, field.object, at); err != nil {
				return err
			}
		}
		if (field.kind == "array" || field.kind == "array_of_arrays") && field.object != "" && field.object != "int64" {
			for j := 0; j < child.Len(); j++ {
				item := child.Index(j)
				if field.kind == "array_of_arrays" {
					for k := 0; k < item.Len(); k++ {
						if err := codecSemanticTree(item.Index(k), field.object, at+"[*][*]"); err != nil {
							return err
						}
					}
				} else if err := codecSemanticTree(item, field.object, at+"[*]"); err != nil {
					return err
				}
			}
		}
	}
	return codecLocal(value.Interface(), path)
}

func codecLocal(value any, path string) error {
	switch v := value.(type) {
	case CandidateSnapshot:
		return codecSnapshot(v, path)
	case ProbeFile:
		return codecProbeSchedule(v, path)
	case CandidateFile:
		seen := map[string]bool{}
		for _, s := range v.Snapshots {
			if seen[s.SnapshotID] {
				return contractError("schedule_mismatch", path+".snapshots")
			}
			seen[s.SnapshotID] = true
		}
	case Gold:
		if (v.LabelStatus == "relevant") != (len(v.RelevantRefs) > 0) {
			return contractError("invalid_value", path+".relevant_refs")
		}
	case Prediction:
		want := map[string]string{"success": "none", "timeout": "timeout", "error": "execution_error"}
		if v.ErrorCode != want[v.Status] {
			return contractError("invalid_value", path+".error_code")
		}
	case InputManifest:
		if v.SourceCommit != initialSourceCommit {
			return contractError("invalid_value", path+".source_commit")
		}
		return codecManifest(v.Files, []string{"candidates.json", "implementation_contract.json", "policy.json", "probes.json"}, path)
	case EvaluationManifest:
		return codecManifest(v.Files, []string{"gold.json", "input_manifest.json", "split_map.json"}, path)
	case SplitMap:
		prev := ""
		for _, s := range v.Streams {
			if s.StreamID <= prev {
				return contractError("schedule_mismatch", path+".streams")
			}
			prev = s.StreamID
		}
	case Statistic:
		return codecStatistic(v, path)
	case QueryMetric:
		return codecQueryMetric(v, path)

	}
	return nil
}

func codecProbeSchedule(v ProbeFile, path string) error {
	ids, scopes := map[string]bool{}, map[string]Scope{}
	for _, p := range v.Probes {
		prior, seen := scopes[p.StreamID]
		if ids[p.QueryID] {
			return contractError("schedule_mismatch", path+".probes")
		}
		if seen && prior != p.Scope {
			return contractError("snapshot_binding", path+".probes[*].scope")
		}
		ids[p.QueryID], scopes[p.StreamID] = true, p.Scope
	}

	return nil
}

func codecQueryMetric(v QueryMetric, path string) error {
	if v.LabelStatus != "relevant" {
		if v.RecallAtK != nil || v.HitAny != nil || v.MatchedCount != 0 {
			return contractError("invalid_value", path)
		}
	} else if v.Status != "success" && (v.MatchedCount != 0 || v.RecallAtK == nil || *v.RecallAtK != 0 || v.HitAny == nil || *v.HitAny != 0) {
		return contractError("invalid_value", path)
	}
	return nil
}

func codecSnapshot(s CandidateSnapshot, path string) error {
	seen := map[string]Candidate{}
	for _, lane := range s.Lanes {
		for _, c := range lane {
			if c.Scope != s.Scope || c.ObservedSeq > s.AsOfSeq || c.EffectiveAtNS > s.AsOfNS || c.UpdatedAtNS > s.AsOfNS {
				return contractError("snapshot_conflict", path)
			}
			key := c.Ref.Origin + ":" + c.Ref.Collection + ":" + c.Ref.ID
			if prior, ok := seen[key]; ok {
				if prior.Scope != c.Scope || prior.Ref != c.Ref || prior.Approved != c.Approved || prior.Active != c.Active || prior.Deleted != c.Deleted || prior.SourceVerified != c.SourceVerified || prior.ContextCostUnits != c.ContextCostUnits || prior.ObservedSeq != c.ObservedSeq || prior.EffectiveAtNS != c.EffectiveAtNS {
					return contractError("snapshot_conflict", path)
				}
			} else {
				seen[key] = c
			}
		}
	}
	return nil
}

func codecManifest(files []ArtifactEntry, want []string, path string) error {
	if len(files) != len(want) {
		return contractError("invalid_value", path+".files")
	}
	for i, f := range files {
		if f.LogicalPath != want[i] {
			return contractError("invalid_value", path+".files")
		}
	}
	return nil
}

func codecStatistic(v Statistic, path string) error {
	if v.Denominator == 0 {
		if v.Numerator != 0 || v.Value != nil || v.NullReason != "no_denominator" {
			return contractError("invalid_value", path)
		}
		return nil
	}
	if v.Value == nil || v.NullReason != "none" {
		return contractError("invalid_value", path)
	}
	expected := v.Numerator / float64(v.Denominator)
	if math.Abs(*v.Value-expected) > 1e-12+1e-10*math.Abs(expected) {
		return contractError("invalid_value", path+".value")
	}
	return nil
}

// ValidateBaselineInputs checks the complete schedule and candidate binding.
// It never filters ineligible candidates, loads files, or reads evaluation gold.
func ValidateBaselineInputs(probes ProbeFile, candidates CandidateFile, policy Policy) error {
	for _, pair := range []struct {
		name  string
		value any
	}{{"ProbeFile", probes}, {"CandidateFile", candidates}, {"Policy", policy}} {
		if err := ValidateTyped(pair.name, pair.value); err != nil {
			return err
		}
	}
	snapshots := map[string]CandidateSnapshot{}
	for _, s := range candidates.Snapshots {
		snapshots[s.SnapshotID] = s
	}
	referenced := map[string]bool{}
	for _, p := range probes.Probes {
		s, ok := snapshots[p.CandidateSnapshotID]
		if !ok {
			return contractError("schedule_mismatch", "$.probes[*].candidate_snapshot_id")
		}
		if s.StreamID != p.StreamID || s.Scope != p.Scope || s.AsOfNS != p.AtNS || s.AsOfSeq != p.CutoffAcceptedSeq {
			return contractError("snapshot_binding", "$.probes[*]")
		}
		referenced[s.SnapshotID] = true
	}
	if len(referenced) != len(snapshots) {
		return contractError("schedule_mismatch", "$.snapshots")
	}
	return nil
}
