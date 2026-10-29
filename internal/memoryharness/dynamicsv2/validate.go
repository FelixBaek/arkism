package dynamicsv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/memoryharness"
)

const maxBytes = 67108864
const maxArray = 200000
const maxDepth = 32
const maxString = 4096

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var integerPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
var schemaPathPattern = regexp.MustCompile(`^\$(?:\.[a-z][a-z0-9_]*|\[\*\])*$`)

func contractError(code, p string) error { return &ContractErrorV2{Code: V2ErrorCode(code), Path: p} }
func childPath(p, field string) string   { return p + "." + field }

// DecodeStrict decodes one of the compiled concrete record/projection types.
// It never accepts a caller-supplied schema or coerces JSON numeric types.
func DecodeStrict(data []byte, typeName string) (any, error) {
	rule, ok := fixedObjects[typeName]
	if !ok {
		return nil, contractError("unsupported_type", "$")
	}
	if len(data) > maxBytes {
		return nil, contractError("resource_limit", "$")
	}
	if !utf8.Valid(data) {
		return nil, contractError("invalid_unicode", "$")
	}
	if !json.Valid(data) {
		return nil, contractError("invalid_json", "$")
	}
	if !validEscapes(data) {
		return nil, contractError("invalid_unicode", "$")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := readNode(dec, 1)
	if err != nil {
		return nil, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, contractError("invalid_json", "$")
	}
	if hasDuplicate(n) {
		return nil, contractError("duplicate_key", "$")
	}
	for _, unknown := range []bool{true, false} {
		if err = shape(n, typeName, "$", unknown); err != nil {
			return nil, err
		}
	}
	if err = rawObject(n, typeName, "$"); err != nil {
		return nil, err
	}
	target := reflect.New(rule.typ)
	if err = json.Unmarshal(data, target.Interface()); err != nil {
		return nil, contractError("invalid_type", "$")
	}
	value := target.Elem().Interface()
	if err = ValidateTyped(typeName, value); err != nil {
		return nil, err
	}
	return value, nil
}

type rawNode struct {
	kind      byte
	text      string
	members   map[string]*rawNode
	items     []*rawNode
	duplicate bool
}

func readNode(d *json.Decoder, depth int) (*rawNode, error) {
	token, err := d.Token()
	if err != nil {
		return nil, contractError("invalid_json", "$")
	}
	if delim, ok := token.(json.Delim); ok {
		return readContainer(d, delim, depth)
	}
	n := &rawNode{}
	switch v := token.(type) {
	case nil:
		n.kind = 'n'
	case bool:
		n.kind = 'b'
		n.text = strconv.FormatBool(v)
	case string:
		n.kind = 's'
		n.text = v
	case json.Number:
		n.kind = 'd'
		n.text = string(v)
	default:
		return nil, contractError("invalid_json", "$")
	}
	if len(n.text) > maxString && n.kind == 's' {
		return nil, contractError("resource_limit", "$")
	}
	return n, nil
}
func readContainer(d *json.Decoder, delim json.Delim, depth int) (*rawNode, error) {
	if depth > maxDepth {
		return nil, contractError("resource_limit", "$")
	}
	if delim == '{' {
		return readMembers(d, depth)
	}
	if delim != '[' {
		return nil, contractError("invalid_json", "$")
	}
	n := &rawNode{kind: 'a'}
	for d.More() {
		if len(n.items) >= maxArray {
			return nil, contractError("resource_limit", "$")
		}
		v, e := readNode(d, depth+1)
		if e != nil {
			return nil, e
		}
		n.items = append(n.items, v)
	}
	if _, err := d.Token(); err != nil {
		return nil, contractError("invalid_json", "$")
	}
	return n, nil
}
func readMembers(d *json.Decoder, depth int) (*rawNode, error) {
	n := &rawNode{kind: 'o', members: map[string]*rawNode{}}
	for d.More() {
		token, e := d.Token()
		if e != nil {
			return nil, contractError("invalid_json", "$")
		}
		key, ok := token.(string)
		if !ok {
			return nil, contractError("invalid_json", "$")
		}
		if len(key) > maxString {
			return nil, contractError("resource_limit", "$")
		}
		v, e := readNode(d, depth+1)
		if e != nil {
			return nil, e
		}
		if _, found := n.members[key]; found {
			n.duplicate = true
		}
		n.members[key] = v
	}
	if _, e := d.Token(); e != nil {
		return nil, contractError("invalid_json", "$")
	}
	return n, nil
}
func hasDuplicate(n *rawNode) bool {
	if n.duplicate {
		return true
	}
	for _, v := range n.members {
		if hasDuplicate(v) {
			return true
		}
	}
	for _, v := range n.items {
		if hasDuplicate(v) {
			return true
		}
	}
	return false
}
func validEscapes(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for i < len(data) && data[i] != '"' {
			if data[i] != '\\' {
				i++
				continue
			}
			i++
			if data[i] != 'u' {
				i++
				continue
			}
			n, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			i += 5
			if n >= 0xdc00 && n <= 0xdfff {
				return false
			}
			if n < 0xd800 || n > 0xdbff {
				continue
			}
			if i+6 > len(data) || data[i] != '\\' || data[i+1] != 'u' {
				return false
			}
			low, _ := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
func objectName(f fieldRule) string {
	if _, ok := fixedObjects[f.ref]; ok {
		return f.ref
	}
	return ""
}
func shape(n *rawNode, name, p string, unknown bool) error {
	if n.kind != 'o' {
		return nil
	}
	rule := fixedObjects[name]
	if unknown {
		for key := range n.members {
			if !knownField(rule, key) {
				return contractError("unknown_field", p)
			}
		}
	}
	for _, f := range rule.fields {
		v, ok := n.members[f.name]
		if !ok {
			if !unknown {
				return contractError("missing_field", childPath(p, f.name))
			}
			continue
		}
		if err := shapeField(v, f, childPath(p, f.name), unknown); err != nil {
			return err
		}
	}
	return nil
}
func knownField(r objectRule, key string) bool {
	for _, f := range r.fields {
		if f.name == key {
			return true
		}
	}
	return false
}
func shapeField(n *rawNode, f fieldRule, p string, unknown bool) error {
	if f.kind == "array" && n.kind == 'a' {
		for _, v := range n.items {
			if e := shapeField(v, *f.item, p+"[*]", unknown); e != nil {
				return e
			}
		}
		return nil
	}
	if name := objectName(f); name != "" {
		return shape(n, name, p, unknown)
	}
	return nil
}
func rawObject(n *rawNode, name, p string) error {
	if n.kind != 'o' {
		return contractError("invalid_type", p)
	}
	for _, f := range fixedObjects[name].fields {
		if e := rawField(n.members[f.name], f, childPath(p, f.name)); e != nil {
			return e
		}
	}
	return nil
}
func rawField(n *rawNode, f fieldRule, p string) error {
	if n.kind == 'n' {
		if f.nullable || f.kind == "null" {
			return nil
		}
		return contractError("invalid_type", p)
	}
	if f.kind == "null" {
		return contractError("invalid_type", p)
	}
	if f.kind == "array" {
		return rawArray(n, f, p)
	}
	if name := objectName(f); name != "" {
		return rawObject(n, name, p)
	}
	if num, ok := fixedNumbers[f.ref]; ok {
		return rawNumber(n, num, p)
	}
	return rawScalar(n, f, p)
}
func rawArray(n *rawNode, f fieldRule, p string) error {
	if n.kind != 'a' {
		return contractError("invalid_type", p)
	}
	if f.maxItems > 0 && len(n.items) > f.maxItems {
		return contractError("resource_limit", p)
	}
	for _, v := range n.items {
		if e := rawField(v, *f.item, p+"[*]"); e != nil {
			return e
		}
	}
	return nil
}
func rawNumber(n *rawNode, r numberRule, p string) error {
	if n.kind != 'd' {
		return contractError("invalid_type", p)
	}
	if r.kind == "int64" {
		if !integerPattern.MatchString(n.text) {
			return contractError("invalid_type", p)
		}
		if _, e := strconv.ParseInt(n.text, 10, 64); e != nil {
			return contractError("invalid_value", p)
		}
	}
	v, e := strconv.ParseFloat(n.text, 64)
	if e != nil || math.IsInf(v, 0) || math.IsNaN(v) || v < r.minimum || v > r.maximum {
		return contractError("invalid_value", p)
	}
	return nil
}
func rawScalar(n *rawNode, f fieldRule, p string) error {
	if f.ref == "Bool" {
		if n.kind != 'b' {
			return contractError("invalid_type", p)
		}
		return nil
	}
	if f.kind == "int64" {
		if n.kind != 'd' || !integerPattern.MatchString(n.text) {
			return contractError("invalid_type", p)
		}
		v, e := strconv.ParseInt(n.text, 10, 64)
		if e != nil || v != f.integer {
			return contractError("invalid_value", p)
		}
		return nil
	}
	if n.kind != 's' {
		return contractError("invalid_type", p)
	}
	return stringValue(n.text, f, p)
}
func stringValue(s string, f fieldRule, p string) error {
	if !utf8.ValidString(s) {
		return contractError("invalid_unicode", p)
	}
	if len(s) > maxString {
		return contractError("resource_limit", p)
	}
	if f.hasConstant && s != f.constant {
		return contractError(fieldValueCode(f), p)
	}
	choices := f.enums
	if e, ok := fixedEnums[f.ref]; ok {
		choices = e
	}
	if choices != nil && !contains(choices, s) {
		return contractError(fieldValueCode(f), p)
	}
	switch f.ref {
	case "LegacyID":
		if !idPattern.MatchString(s) {
			return contractError("invalid_value", p)
		}
	case "SHA":
		if !shaPattern.MatchString(s) {
			return contractError("invalid_value", p)
		}
	case "SchemaPath":
		if !knownSchemaPath(s) {
			return contractError("invalid_value", p)
		}
	case "SafePath":
		if !safePath(s) {
			return contractError("unsafe_path", p)
		}
	}
	return nil
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func safePath(s string) bool {
	if s == "" || strings.ContainsAny(s, "\\\x00") || strings.HasPrefix(s, "/") || path.Clean(s) != s {
		return false
	}
	for _, x := range strings.Split(s, "/") {
		if x == "." || x == ".." || x == "" {
			return false
		}
	}
	return !strings.Contains(strings.Split(s, "/")[0], ":")
}

// ValidateTyped requires the exact compiled non-pointer concrete type.
// This validates synthetic metadata, not product authority or actual task bodies.
func ValidateTyped(typeName string, value any) error {
	r, ok := fixedObjects[typeName]
	if !ok {
		return contractError("unsupported_type", "$")
	}
	v := reflect.ValueOf(value)
	if !v.IsValid() || v.Type() != r.typ {
		return contractError("invalid_type", "$")
	}
	if e := typedObject(typeName, v, "$", 1); e != nil {
		return e
	}
	return visitSemantics(typeName, v, "$")
}
func typedObject(name string, v reflect.Value, p string, depth int) error {
	if depth > maxDepth {
		return contractError("resource_limit", p)
	}
	r := fixedObjects[name]
	for i, f := range r.fields {
		if e := typedField(v.Field(i), f, childPath(p, f.name), depth+1); e != nil {
			return e
		}
	}
	return nativeAlias(name, v, p)
}
func nativeAlias(name string, v reflect.Value, p string) error {
	aliases := map[string]string{"ScopeV2": "Scope", "RefV2": "Ref", "LegacyContextBudget": "ContextBudget"}
	n, ok := aliases[name]
	if !ok {
		return nil
	}
	e := memoryharness.ValidateTyped(n, v.Interface())
	if e == nil {
		return nil
	}
	var ce *memoryharness.ContractError
	if errors.As(e, &ce) {
		return contractError(ce.Code, p+strings.TrimPrefix(ce.Path, "$"))
	}
	return contractError("invalid_value", p)
}
func typedField(v reflect.Value, f fieldRule, p string, depth int) error {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			if f.nullable || f.kind == "null" {
				return nil
			}
			return contractError("invalid_type", p)
		}
		v = v.Elem()
	}
	if f.kind == "null" {
		return contractError("invalid_type", p)
	}
	if f.kind == "array" {
		return typedArray(v, f, p, depth)
	}
	if name := objectName(f); name != "" {
		return typedObject(name, v, p, depth)
	}
	if r, ok := fixedNumbers[f.ref]; ok {
		return typedNumber(v, r, p)
	}
	if v.Kind() == reflect.Bool {
		return nil
	}
	if v.Kind() == reflect.Int64 {
		if f.hasConstant && v.Int() != f.integer {
			return contractError("invalid_value", p)
		}
		return nil
	}
	return stringValue(v.String(), f, p)
}
func typedArray(v reflect.Value, f fieldRule, p string, depth int) error {
	if v.IsNil() {
		return contractError("invalid_type", p)
	}
	if v.Len() > maxArray || f.maxItems > 0 && v.Len() > f.maxItems {
		return contractError("resource_limit", p)
	}
	for i := 0; i < v.Len(); i++ {
		if e := typedField(v.Index(i), *f.item, p+"[*]", depth+1); e != nil {
			return e
		}
	}
	return nil
}
func typedNumber(v reflect.Value, r numberRule, p string) error {
	if r.kind == "int64" {
		n := v.Int()
		if n < 0 || float64(n) < r.minimum || float64(n) > r.maximum {
			return contractError("invalid_value", p)
		}
		return nil
	}
	n := v.Float()
	if math.IsNaN(n) || math.IsInf(n, 0) || n < r.minimum || n > r.maximum {
		return contractError("invalid_value", p)
	}
	return nil
}
func visitSemantics(name string, v reflect.Value, p string) error {
	for i, f := range fixedObjects[name].fields {
		if e := visitField(v.Field(i), f, childPath(p, f.name)); e != nil {
			return e
		}
	}
	if check := semanticChecks[name]; check != nil {
		if e := check(v, p); e != nil {
			return e
		}
	}
	return checkSelfHash(name, v, p)
}
func visitField(v reflect.Value, f fieldRule, p string) error {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if f.kind == "array" {
		for i := 0; i < v.Len(); i++ {
			if e := visitField(v.Index(i), *f.item, p+"[*]"); e != nil {
				return e
			}
		}
		return nil
	}
	if n := objectName(f); n != "" {
		return visitSemantics(n, v, p)
	}
	return nil
}

// CanonicalBytes uses the frozen primitive grammar on a validated fixed DTO.
// No caller-defined maps, arbitrary schemas, pointers-to-records, or coercions are accepted.
func CanonicalBytes(typeName string, value any) ([]byte, error) {
	if e := ValidateTyped(typeName, value); e != nil {
		return nil, e
	}
	return canonical(reflect.ValueOf(value))
}

// CanonicalSHA256 hashes a concrete DTO/projection with its exact typed domain.
// To calculate a wire self hash before construction, pass its named closed projection.
func CanonicalSHA256(typeName string, value any) (string, error) {
	b, e := CanonicalBytes(typeName, value)
	if e != nil {
		return "", e
	}
	return domainSHA(typeName, b), nil
}
func domainSHA(name string, b []byte) string {
	h := sha256.New()
	h.Write([]byte("dynamics-contract-v2\x00" + name + "\x00"))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func canonical(v reflect.Value) ([]byte, error) {
	var b bytes.Buffer
	if e := appendValue(&b, v); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
func appendValue(b *bytes.Buffer, v reflect.Value) error {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			b.WriteByte('n')
			return nil
		}
		return appendValue(b, v.Elem())
	}
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			b.WriteByte('t')
		} else {
			b.WriteByte('f')
		}
	case reflect.Int64:
		b.WriteByte('i')
		b.WriteString(strconv.FormatInt(v.Int(), 10))
		b.WriteByte(';')
	case reflect.Float64:
		n := v.Float()
		if n == 0 {
			n = 0
		}
		fmt.Fprintf(b, "d%016x;", math.Float64bits(n))
	case reflect.String:
		s := v.String()
		b.WriteByte('s')
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	case reflect.Slice:
		return appendArray(b, v)
	case reflect.Struct:
		return appendObject(b, v)
	default:
		return contractError("invalid_type", "$")
	}
	return nil
}
func appendArray(b *bytes.Buffer, v reflect.Value) error {
	if v.IsNil() {
		return contractError("invalid_type", "$")
	}
	fmt.Fprintf(b, "a%d[", v.Len())
	for i := 0; i < v.Len(); i++ {
		if e := appendValue(b, v.Index(i)); e != nil {
			return e
		}
	}
	b.WriteByte(']')
	return nil
}
func appendObject(b *bytes.Buffer, v reflect.Value) error {
	names := make([]string, v.NumField())
	byName := map[string]int{}
	for i := range names {
		names[i] = v.Type().Field(i).Tag.Get("json")
		byName[names[i]] = i
	}
	sort.Strings(names)
	fmt.Fprintf(b, "o%d{", len(names))
	for _, n := range names {
		if e := appendValue(b, reflect.ValueOf(n)); e != nil {
			return e
		}
		if e := appendValue(b, v.Field(byName[n])); e != nil {
			return e
		}
	}
	b.WriteByte('}')
	return nil
}
func checkSelfHash(name string, v reflect.Value, p string) error {
	dest, ok := fixedProjections[name]
	if !ok {
		return nil
	}
	projected, e := project(v, fixedObjects[dest].typ)
	if e != nil {
		return e
	}
	b, e := canonical(projected)
	if e != nil {
		return e
	}
	f := fixedHashFields[name]
	if v.FieldByName(f).String() != domainSHA(dest, b) {
		return contractError("hash_mismatch", childPath(p, jsonField(v.Type(), f)))
	}
	return nil
}
func jsonField(t reflect.Type, name string) string {
	f, _ := t.FieldByName(name)
	return f.Tag.Get("json")
}
func project(src reflect.Value, dest reflect.Type) (reflect.Value, error) {
	if src.Type() == dest {
		return src, nil
	}
	out := reflect.New(dest).Elem()
	if dest.Kind() == reflect.Struct {
		return projectObject(src, out)
	}
	if dest.Kind() == reflect.Slice {
		out = reflect.MakeSlice(dest, src.Len(), src.Len())
		for i := 0; i < src.Len(); i++ {
			v, e := project(src.Index(i), dest.Elem())
			if e != nil {
				return out, e
			}
			out.Index(i).Set(v)
		}
		return out, nil
	}
	return out, contractError("invalid_type", "$")
}
func projectObject(src, out reflect.Value) (reflect.Value, error) {
	for i := 0; i < out.NumField(); i++ {
		f := out.Type().Field(i)
		in := src.FieldByName(f.Name)
		if !in.IsValid() {
			return out, contractError("invalid_type", "$")
		}
		v, e := project(in, f.Type)
		if e != nil {
			return out, e
		}
		out.Field(i).Set(v)
	}
	return out, nil
}

// Semantic checks are deliberately fixed; adding a caller schema is impossible.
var semanticChecks = map[string]func(reflect.Value, string) error{
	"DynamicsKeyV2": checkKey, "StateV2": checkState, "TaskBinding": checkTask,
	"EvidenceBinding": checkEvidence, "EventV2": checkEvent, "ReceiptV2": checkReceipt,
	"ReceiptIndexV2": checkIndex, "SnapshotV2": checkSnapshot, "SnapshotFileV2": checkSnapshotFile,
	"StateViewV2": checkView, "TransitionV2": checkTransition,
	"ImportanceFeatureV1": checkFeature, "P1FeatureFileV1": checkFeatureFile,
	"RankerConfigV2": checkRanker, "RankerConfigParametersProjectionV2": checkRankerParameters,
	"RegistryBoundRankerProjectionV2": checkBoundRanker,
	"ResourceLimits":                  checkLimits, "PolicyRegistryV2": checkRegistry, "DynamicsRunConfigV2": checkRunConfig,
	"OutputRoleFile": checkOutputRef, "OutputManifest": checkOutputManifest, "CLIStatus": checkCLIStatus,
	"PredictionV2": checkPrediction, "PredictionRunV2": checkPredictionRun, "PolicyDecisionV2": checkDecision,
	"ProbeV2": checkProbe, "ProbeFileV2": checkProbeFile, "EventFileV2": checkEventFile,
	"DynamicInputManifestV2": checkInputManifest, "P1InputManifestV1": checkP1Manifest, "LifecycleFence": checkFence,
}

func checkKey(v reflect.Value, p string) error {
	k := v.Interface().(DynamicsKeyV2)
	if k.ContractSHA256 != ContractSHA256 {
		return contractError("hash_mismatch", p+".contract_sha256")
	}
	return nil
}
func checkState(v reflect.Value, p string) error {
	s := v.Interface().(StateV2)
	anchors := []bool{s.ActivatedAtNS != nil, s.ActivationAcceptedSeq != nil, s.ActivationEventID != nil, s.ActivationPayloadSHA256 != nil, s.LastAppliedAtNS != nil, s.LastAppliedSeq != nil}
	count := 0
	for _, present := range anchors {
		if present {
			count++
		}
	}
	if s.Status == "pending_activation" {
		if count != 0 || s.StateRevision != 0 {
			return contractError("invalid_state", p)
		}
		return nil
	}
	if count != 6 || s.StateRevision < 1 {
		return contractError("invalid_state", p)
	}
	if *s.LastAppliedAtNS < *s.ActivatedAtNS || *s.LastAppliedSeq < *s.ActivationAcceptedSeq {
		return contractError("invalid_state", p)
	}
	return nil
}
func checkTask(v reflect.Value, p string) error {
	t := v.Interface().(TaskBinding)
	if t.TaskRef.Collection != "eval_tasks" || t.RunRef.Collection != "eval_runs" || t.TrialRef.Collection != "eval_trials" {
		return contractError("invalid_value", p)
	}
	if t.TaskSHA256 != t.TaskRef.ContentSHA256 {
		return contractError("hash_mismatch", p+".task_sha256")
	}
	return nil
}
func checkEvidence(v reflect.Value, p string) error {
	e := v.Interface().(EvidenceBinding)
	if e.Independence == "synthetic_independent" && e.SourceStatus == "available" && len(e.Refs) == 0 {
		return contractError("evidence_unqualified", p+".refs")
	}
	return orderedUniqueEvidence(e.Refs, p+".refs")
}
func orderedUniqueEvidence(refs []EvidenceRef, p string) error {
	previous := ""
	for _, r := range refs {
		b, e := canonical(reflect.ValueOf(r))
		if e != nil {
			return e
		}
		key := string(b)
		if previous != "" && key <= previous {
			return contractError("evidence_conflict", p)
		}
		previous = key
	}
	return nil
}
func qualified(e EventV2) bool {
	if e.Task == nil || e.Evidence == nil {
		return false
	}
	t, b := e.Task, e.Evidence
	if !matchedEventRule(e) {
		return false
	}
	if t.Qualification != "synthetic_independent" || b.Independence != "synthetic_independent" || b.SourceStatus != "available" || b.AvailableAcceptedSeq > e.AcceptedSeq {
		return false
	}
	return e.Outcome == "passed" || e.Outcome == "failed" && e.Attribution == "memory"
}
func matchedEventRule(e EventV2) bool {
	if e.Task == nil || e.Evidence == nil {
		return true
	}
	return e.Task.RuleSHA256 == e.Evidence.RuleSHA256
}
func checkEvent(v reflect.Value, p string) error {
	e := v.Interface().(EventV2)
	if !matchedEventRule(e) {
		return contractError("hash_mismatch", p+".evidence.rule_sha256")
	}
	if e.ObservedAtNS != nil && *e.ObservedAtNS > e.AcceptedAtNS {
		return contractError("future_event", p+".observed_at_ns")
	}
	// Arrival belongs to this transport attempt; an exact retry retains the
	// original logical acceptance coordinates. A standalone event cannot know
	// whether it is a retry. First-acceptance ordering is checked by the event
	// stream (and the transition's fresh-event boundary), after ID lookup.
	if e.Kind != "outcome" {
		if e.Outcome != "not_applicable" || e.Attribution != "not_applicable" || e.Strength != nil {
			return contractError("invalid_value", p)
		}
		return nil
	}
	if e.Outcome == "not_applicable" {
		return contractError("invalid_value", p+".outcome")
	}
	if e.Evidence != nil {
		if e.Evidence.Target != e.Key.Target || e.Evidence.Dimension != e.Key.Dimension {
			return contractError("snapshot_binding", p+".evidence")
		}
	}
	if e.Task != nil && e.Evidence != nil && e.Task.TaskSHA256 != e.Evidence.TaskSHA256 {
		return contractError("hash_mismatch", p+".evidence.task_sha256")
	}
	if qualified(e) && e.Strength == nil {
		return contractError("invalid_value", p+".strength")
	}
	return nil
}
func checkReceipt(v reflect.Value, p string) error {
	r := v.Interface().(ReceiptV2)
	if r.Reason == "duplicate_evidence" {
		return duplicateReceipt(r, p)
	}
	if r.DuplicateOfReceiptSHA256 != nil {
		return contractError("evidence_conflict", p+".duplicate_of_receipt_sha256")
	}
	if r.Disposition == "non_learning" {
		if r.BeforeStateRevision != r.AfterStateRevision || r.EvidenceConsumptionSHA256 != nil {
			return contractError("invalid_state", p)
		}
		return nil
	}
	if r.BeforeStateRevision == math.MaxInt64 || r.AfterStateRevision != r.BeforeStateRevision+1 {
		return contractError("invalid_state", p)
	}
	if r.Disposition == "invalidated" && r.EvidenceConsumptionSHA256 != nil {
		return contractError("evidence_conflict", p)
	}
	return receiptQualification(r, p)
}
func duplicateReceipt(r ReceiptV2, p string) error {
	if r.Disposition != "non_learning" || r.DuplicateOfReceiptSHA256 == nil || r.EvidenceConsumptionSHA256 == nil || r.BeforeStateRevision != r.AfterStateRevision {
		return contractError("evidence_conflict", p)
	}
	return nil
}
func receiptQualification(r ReceiptV2, p string) error {
	eligible := r.Reason == "passed_independent" || r.Reason == "failed_attributable"
	if (r.EvidenceConsumptionSHA256 != nil) != eligible {
		return contractError("evidence_conflict", p+".evidence_consumption_sha256")
	}
	return nil
}
func checkIndex(v reflect.Value, p string) error {
	x := v.Interface().(ReceiptIndexV2)
	if x.Cursor.StreamID != x.StreamID || x.Cursor.ClockDomain != x.ClockDomain {
		return contractError("stream_mismatch", p+".cursor")
	}
	if int64(len(x.Receipts)) != x.Cursor.AcceptedSeq {
		return contractError("prefix_mismatch", p+".receipts")
	}
	if len(x.Receipts) > 200000 {
		return contractError("resource_limit", p+".receipts")
	}
	byHash := map[string]ReceiptV2{}
	events := map[string]bool{}
	last := int64(0)
	for i, r := range x.Receipts {
		if r.StreamID != x.StreamID || r.AcceptedSeq != int64(i+1) || r.AcceptedAtNS < last || events[r.EventID] {
			return contractError("sequence_regression", p+".receipts[*]")
		}
		events[r.EventID] = true
		last = r.AcceptedAtNS
		if err := checkReceiptLink(r, byHash, p); err != nil {
			return err
		}
		byHash[r.ReceiptSHA256] = r
	}
	if len(x.Receipts) > 0 && x.Cursor.AcceptedAtNS != last {
		return contractError("prefix_mismatch", p+".cursor")
	}
	if x.Cursor.AcceptedSeq < x.LifecycleFence.MinimumAcceptedSeq {
		return contractError("incomplete_prefix", p)
	}
	return checkConsumptions(x, byHash, p)
}
func checkReceiptLink(r ReceiptV2, previous map[string]ReceiptV2, p string) error {
	if r.DuplicateOfReceiptSHA256 == nil {
		return nil
	}
	first, ok := previous[*r.DuplicateOfReceiptSHA256]
	if !ok || first.Key != r.Key || first.EvidenceConsumptionSHA256 == nil || r.EvidenceConsumptionSHA256 == nil || *first.EvidenceConsumptionSHA256 != *r.EvidenceConsumptionSHA256 || first.DuplicateOfReceiptSHA256 != nil {
		return contractError("evidence_conflict", p+".receipts[*]")
	}
	return nil
}
func checkConsumptions(x ReceiptIndexV2, receipts map[string]ReceiptV2, p string) error {
	indexed := map[string]bool{}
	previous := ""
	for _, c := range x.EvidenceConsumptions {
		if c.IdentitySHA256 <= previous {
			return contractError("evidence_conflict", p+".evidence_consumptions[*]")
		}
		previous = c.IdentitySHA256
		r, ok := receipts[c.FirstReceiptSHA256]
		if !ok || r.EventID != c.FirstEventID || r.EvidenceConsumptionSHA256 == nil || *r.EvidenceConsumptionSHA256 != c.IdentitySHA256 || r.DuplicateOfReceiptSHA256 != nil {
			return contractError("evidence_conflict", p+".evidence_consumptions[*]")
		}
		indexed[c.IdentitySHA256] = true
	}
	for _, r := range x.Receipts {
		if r.EvidenceConsumptionSHA256 != nil && !indexed[*r.EvidenceConsumptionSHA256] {
			return contractError("evidence_conflict", p+".receipts[*]")
		}
	}
	return nil
}
func checkFence(v reflect.Value, p string) error {
	f := v.Interface().(LifecycleFence)
	previous := ""
	for _, e := range f.Entries {
		key := scopeRefKey(e.Scope, e.Target)
		if key <= previous {
			return contractError("snapshot_conflict", p+".entries")
		}
		previous = key
		if err := orderedUniqueEvidence(e.SourceRefs, p+".entries[*].source_refs"); err != nil {
			return err
		}
	}
	return nil
}
func scopeRefKey(s ScopeV2, r RefV2) string {
	a, _ := canonical(reflect.ValueOf(s))
	b, _ := canonical(reflect.ValueOf(r))
	return string(a) + string(b)
}
func checkSnapshot(v reflect.Value, p string) error {
	s := v.Interface().(SnapshotV2)
	if s.StreamID != s.Cursor.StreamID || s.Cursor != s.Receipts.Cursor {
		return contractError("prefix_mismatch", p+".cursor")
	}
	if len(s.States) > 100000 {
		return contractError("resource_limit", p+".states")
	}
	previous := ""
	for _, state := range s.States {
		if state.Status == "pending_activation" {
			return contractError("invalid_state", p+".states[*]")
		}
		if state.Key.Scope != s.Scope || state.Key.Dimension != s.Dimension || state.Key.ConfigSHA256 != s.ConfigSHA256 {
			return contractError("snapshot_binding", p+".states[*].key")
		}
		b, _ := canonical(reflect.ValueOf(state.Key))
		if string(b) <= previous {
			return contractError("snapshot_conflict", p+".states")
		}
		previous = string(b)
		if *state.LastAppliedSeq > s.Cursor.AcceptedSeq || *state.LastAppliedAtNS > s.Cursor.AcceptedAtNS {
			return contractError("prefix_mismatch", p+".states[*]")
		}
		if err := stateFence(state, s.Receipts.LifecycleFence, p); err != nil {
			return err
		}
	}
	for _, r := range s.Receipts.Receipts {
		if r.Key.Scope != s.Scope || r.Key.Dimension != s.Dimension || r.Key.ConfigSHA256 != s.ConfigSHA256 {
			return contractError("snapshot_binding", p+".receipts")
		}
	}
	return checkPrefixHash(s, p)
}
func stateFence(s StateV2, f LifecycleFence, p string) error {
	for _, e := range f.Entries {
		if e.Scope == s.Key.Scope && e.Target == s.Key.Target {
			if s.Status == "active" && e.Status != "current" {
				return contractError("invalidated_target", p+".states[*]")
			}
			return nil
		}
	}
	if s.Status == "active" {
		return contractError("source_unavailable", p+".states[*]")
	}
	return nil
}
func checkPrefixHash(s SnapshotV2, p string) error {
	initial := StreamInitialPrefixProjectionV2{StreamID: s.StreamID, ClockDomain: s.Cursor.ClockDomain, Scope: s.Scope, Dimension: s.Dimension, ConfigSHA256: s.ConfigSHA256}
	b, _ := canonical(reflect.ValueOf(initial))
	prefix := domainSHA("StreamInitialPrefixProjectionV2", b)
	for _, r := range s.Receipts.Receipts {
		step := StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: prefix, StreamID: s.StreamID, ClockDomain: s.Cursor.ClockDomain, AcceptedSeq: r.AcceptedSeq, AcceptedAtNS: r.AcceptedAtNS, PayloadSHA256: r.PayloadSHA256}
		b, _ = canonical(reflect.ValueOf(step))
		prefix = domainSHA("StreamAcceptedStepProjectionV2", b)
	}
	if prefix != s.Cursor.PrefixSHA256 {
		return contractError("prefix_mismatch", p+".cursor.prefix_sha256")
	}
	return nil
}
func checkSnapshotFile(v reflect.Value, p string) error {
	return checkSnapshot(reflect.ValueOf(v.Interface().(SnapshotFileV2).Snapshot), p+".snapshot")
}
func checkView(v reflect.Value, p string) error {
	s := v.Interface().(StateViewV2)
	if s.StreamID != s.SnapshotCursor.StreamID || s.Scope != s.Key.Scope || s.Dimension != s.Key.Dimension || s.ConfigSHA256 != s.Key.ConfigSHA256 || s.ClockDomain != s.SnapshotCursor.ClockDomain {
		return contractError("snapshot_binding", p)
	}
	if s.CutoffAcceptedSeq != s.SnapshotCursor.AcceptedSeq || s.ActivationAcceptedSeq > s.CutoffAcceptedSeq || s.AtNS < s.ActivatedAtNS || s.AtNS < s.SnapshotCursor.AcceptedAtNS {
		return contractError("prefix_mismatch", p)
	}
	return nil
}
func checkTransition(v reflect.Value, p string) error {
	t := v.Interface().(TransitionV2)
	if t.Cursor != t.Receipts.Cursor {
		return contractError("prefix_mismatch", p+".cursor")
	}
	found := false
	for _, r := range t.Receipts.Receipts {
		if r.ReceiptSHA256 == t.Receipt.ReceiptSHA256 && reflect.DeepEqual(r, t.Receipt) {
			found = true
		}
	}
	if !found {
		return contractError("snapshot_binding", p+".receipt")
	}
	if t.AttemptDisposition == "duplicate_event" {
		if t.BeforeStateSHA256 != t.State.StateSHA256 {
			return contractError("invalid_state", p)
		}
		return nil
	}
	if t.State.Key != t.Receipt.Key || t.State.StateRevision != t.Receipt.AfterStateRevision {
		return contractError("snapshot_binding", p)
	}
	if t.AttemptDisposition == "duplicate_evidence" && t.Receipt.Reason != "duplicate_evidence" {
		return contractError("evidence_conflict", p)
	}
	return nil
}
func checkFeature(v reflect.Value, p string) error {
	f := v.Interface().(ImportanceFeatureV1)
	if f.Status == "present" {
		if f.Value == nil || f.MissingReason != nil {
			return contractError("invalid_value", p)
		}
	} else {
		if f.Value != nil || f.MissingReason == nil || *f.MissingReason == "none" {
			return contractError("invalid_value", p)
		}
	}
	if f.ObservedAtNS != nil && *f.ObservedAtNS > f.AvailableAtNS {
		return contractError("future_event", p+".observed_at_ns")
	}
	return orderedUniqueEvidence(f.InputRefs, p+".input_refs")
}
func checkFeatureFile(v reflect.Value, p string) error {
	f := v.Interface().(P1FeatureFileV1)
	if f.FeatureContractSHA256 != P1ContractSHA256 {
		return contractError("hash_mismatch", p+".feature_contract_sha256")
	}
	if len(f.Features) > 100000 {
		return contractError("resource_limit", p+".features")
	}
	previous := ""
	seen := map[string]bool{}
	for _, x := range f.Features {
		k := featureKey(x)
		if k <= previous || seen[x.FeatureID] {
			return contractError("duplicate_feature", p+".features")
		}
		previous = k
		seen[x.FeatureID] = true
	}
	return nil
}
func featureKey(f ImportanceFeatureV1) string {
	source, _ := canonical(reflect.ValueOf(f.Source))
	seq := fmt.Sprintf("%020d", f.AvailableAcceptedSeq)
	return scopeRefKey(f.Scope, f.Target) + string(source) + f.FeatureName + "\x00" + f.FeatureVersion + "\x00" + f.RuleSHA256 + f.GeneratorConfigSHA256 + seq
}
func checkLimits(v reflect.Value, p string) error {
	x := v.Interface().(ResourceLimits)
	if x.MaxArrayItems != 200000 || x.MaxEvents != 100000 || x.MaxFeatures != 100000 || x.MaxFileBytes != maxBytes || x.MaxJSONDepth != maxDepth || x.MaxProbes != 100000 || x.MaxReceipts != 200000 || x.MaxStates != 100000 || x.MaxStringBytes != maxString {
		return contractError("resource_limit", p)
	}
	return nil
}
func checkRanker(v reflect.Value, p string) error           { return rankerShape(v, p) }
func checkRankerParameters(v reflect.Value, p string) error { return rankerShape(v, p) }
func checkBoundRanker(v reflect.Value, p string) error {
	if e := rankerShape(v, p); e != nil {
		return e
	}
	projected, e := project(v, fixedObjects["RankerConfigParametersProjectionV2"].typ)
	if e != nil {
		return e
	}
	b, _ := canonical(projected)
	if v.FieldByName("ConfigSHA256").String() != domainSHA("RankerConfigParametersProjectionV2", b) {
		return contractError("hash_mismatch", p+".config_sha256")
	}
	return nil
}
func laneShape(policy string) []string {
	switch policy {
	case "B0":
		return []string{}
	case "B1":
		return []string{"relevance", "recency"}
	case "V3-P1-IR":
		return []string{"relevance", "importance", "recency"}
	default:
		return []string{"relevance", "state"}
	}
}
func rankerShape(v reflect.Value, p string) error {
	policy := v.FieldByName("PolicyID").String()
	lanes := v.FieldByName("Lanes").Interface().([]RankLane)
	want := laneShape(policy)
	if len(lanes) != len(want) {
		return contractError("config_mismatch", p+".lanes")
	}
	positive := false
	for i, lane := range lanes {
		if lane.Kind != want[i] {
			return contractError("config_mismatch", p+".lanes[*].kind")
		}
		positive = positive || lane.Weight > 0
	}
	if policy != "B0" && !positive {
		return contractError("config_mismatch", p+".lanes")
	}
	state := policy == "B2" || policy == "V3-P1-FD" || strings.HasPrefix(policy, "V3-P1-NO")
	if v.FieldByName("DynamicsConfigSHA256").IsNil() == state {
		return contractError("config_mismatch", p+".dynamics_config_sha256")
	}
	feature := v.FieldByName("FeatureContractSHA256")
	if feature.IsNil() == (policy == "V3-P1-IR") {
		return contractError("config_mismatch", p+".feature_contract_sha256")
	}
	if !feature.IsNil() && feature.Elem().String() != P1ContractSHA256 {
		return contractError("hash_mismatch", p+".feature_contract_sha256")
	}
	return rankerAblation(v, policy, p)
}
func rankerAblation(v reflect.Value, policy, p string) error {
	want := "none"
	if policy == "V3-P1-NOTIME" {
		want = "disable_decay"
	}
	if policy == "V3-P1-NOOUT" {
		want = "disable_outcome_jumps"
	}
	if v.FieldByName("Ablation").String() != want || v.FieldByName("InheritedB2ConfigSHA256").IsNil() != (want == "none") {
		return contractError("config_mismatch", p+".ablation")
	}
	return nil
}
func checkRegistry(v reflect.Value, p string) error {
	r := v.Interface().(PolicyRegistryV2)
	want := fixedEnums["PolicyID"]
	if len(r.Entries) != len(want) {
		return contractError("config_mismatch", p+".entries")
	}
	for i, e := range r.Entries {
		if string(e.PolicyID) != want[i] || e.Ranker.PolicyID != e.PolicyID || e.Ranker.RegistrySHA256 != r.RegistrySHA256 || e.Version != e.Ranker.PolicyVersion {
			return contractError("config_mismatch", p+".entries[*]")
		}
		if err := entryConfig(e, p+".entries[*]"); err != nil {
			return err
		}
	}
	if e := registryAblations(r, p); e != nil {
		return e
	}
	return checkInheritedAblations(r, p)
}
func entryConfig(e PolicyEntry, p string) error {
	state := e.Ranker.DynamicsConfigSHA256 != nil
	if state != (e.Dynamics != nil) {
		return contractError("config_mismatch", p+".dynamics")
	}
	if e.Dynamics != nil && *e.Ranker.DynamicsConfigSHA256 != e.Dynamics.ConfigSHA256 {
		return contractError("hash_mismatch", p+".dynamics")
	}
	if state != (e.ImplementedMode == "scalar") {
		return contractError("unsupported_mode", p+".implemented_mode")
	}
	if !state && e.ImplementedMode != "none" {
		return contractError("unsupported_mode", p)
	}
	if e.Dynamics != nil {
		if e.UsesElapsedDecay != e.Dynamics.DecayEnabled || e.UsesOutcomes != e.Dynamics.OutcomeJumpEnabled {
			return contractError("config_mismatch", p)
		}
	}
	return nil
}
func registryAblations(r PolicyRegistryV2, p string) error {
	// Each registered scalar configuration has the policy's exact enabled components.
	for _, e := range r.Entries {
		if e.Dynamics == nil {
			continue
		}
		decay := e.PolicyID != "V3-P1-NOTIME"
		jump := e.PolicyID != "V3-P1-NOOUT" && e.PolicyID != "V3-P1-FD"
		if e.Dynamics.DecayEnabled != decay || e.Dynamics.OutcomeJumpEnabled != jump {
			return contractError("config_mismatch", p+".entries[*].dynamics")
		}
	}
	return nil
}
func checkRunConfig(v reflect.Value, p string) error {
	r := v.Interface().(DynamicsRunConfigV2)
	if r.RegistrySHA256 != r.Registry.RegistrySHA256 {
		return contractError("hash_mismatch", p+".registry_sha256")
	}
	if len(r.PolicyIDs) != len(r.Configs) || len(r.PolicyIDs) == 0 {
		return contractError("config_mismatch", p+".configs")
	}
	seen := map[PolicyID]bool{}
	for i, c := range r.Configs {
		if c.PolicyID != r.PolicyIDs[i] || seen[c.PolicyID] {
			return contractError("config_mismatch", p+".configs[*]")
		}
		seen[c.PolicyID] = true
		if e := registeredConfig(c, r.Registry, p); e != nil {
			return e
		}
	}
	return nil
}
func registeredConfig(c PolicyConfig, r PolicyRegistryV2, p string) error {
	for _, e := range r.Entries {
		if e.PolicyID == c.PolicyID {
			if !reflect.DeepEqual(c.Ranker, e.Ranker) || !reflect.DeepEqual(c.Dynamics, e.Dynamics) {
				return contractError("config_mismatch", p+".configs[*]")
			}
			return nil
		}
	}
	return contractError("unsupported_policy", p+".configs[*].policy_id")
}
func checkOutputRef(v reflect.Value, p string) error {
	f := v.Interface().(OutputRoleFile)
	if f.Role == "completion_manifest" {
		if f.RelativePath != "manifest.json" {
			return contractError("unsafe_path", p+".relative_path")
		}
		return nil
	}
	for _, id := range fixedEnums["PolicyID"] {
		if f.RelativePath == "predictions/"+id+".json" {
			return nil
		}
	}
	return contractError("unsafe_path", p+".relative_path")
}
func checkOutputManifest(v reflect.Value, p string) error {
	m := v.Interface().(OutputManifest)
	if m.FailedRows > m.PlannedRows || (m.Status == "completed") != (m.FailedRows == 0) {
		return contractError("invalid_state", p+".status")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if f.Role != "prediction_run" || seen[f.RelativePath] {
			return contractError("snapshot_conflict", p+".files")
		}
		seen[f.RelativePath] = true
	}
	return nil
}
func checkCLIStatus(v reflect.Value, p string) error {
	s := v.Interface().(CLIStatus)
	if s.Status == "failed" {
		if s.Error == nil {
			return contractError("invalid_state", p)
		}
		return nil
	}
	if s.Error != nil || s.RunID == nil || len(s.Outputs) == 0 {
		return contractError("invalid_state", p)
	}
	seen := map[string]bool{}
	for i, f := range s.Outputs {
		if seen[f.RelativePath] || (f.Role == "completion_manifest") != (i == len(s.Outputs)-1) {
			return contractError("snapshot_conflict", p+".outputs")
		}
		seen[f.RelativePath] = true
	}
	return nil
}
func checkPrediction(v reflect.Value, p string) error {
	x := v.Interface().(PredictionV2)
	if x.Status == "failed" {
		if x.Decision != nil || x.Error == nil {
			return contractError("invalid_state", p)
		}
		return nil
	}
	if x.Status == "fallback" {
		return contractError("unsupported_fallback", p+".status")
	}
	if x.Decision == nil || x.Error != nil || x.Decision.QueryID != x.QueryID {
		return contractError("snapshot_binding", p)
	}
	return nil
}
func checkPredictionRun(v reflect.Value, p string) error {
	r := v.Interface().(PredictionRunV2)
	seen := map[string]bool{}
	failed := int64(0)
	for _, x := range r.Predictions {
		if seen[x.QueryID] {
			return contractError("schedule_mismatch", p+".predictions")
		}
		seen[x.QueryID] = true
		if x.Status == "failed" {
			failed++
		}
		if x.Decision != nil && (x.Decision.PolicyID != r.PolicyID || x.Decision.PolicyVersion != r.PolicyVersion) {
			return contractError("snapshot_binding", p+".predictions[*]")
		}
	}
	if failed != r.Cost.FailedPredictions {
		return contractError("schedule_mismatch", p+".cost.failed_predictions")
	}
	return nil
}
func checkDecision(v reflect.Value, p string) error {
	d := v.Interface().(PolicyDecisionV2)
	if d.Status == "fallback" {
		return contractError("unsupported_fallback", p+".status")
	}
	if d.ContextCostUsed > d.ContextBudget.Limit {
		return contractError("invalid_value", p+".context_cost_used")
	}
	ranked := map[RefV2]bool{}
	for i, r := range d.Ranks {
		if ranked[r.Ref] || r.FinalOrdinal != int64(i+1) {
			return contractError("snapshot_conflict", p+".ranks")
		}
		ranked[r.Ref] = true
	}
	selected := map[RefV2]bool{}
	for _, r := range d.Selected {
		if !ranked[r] || selected[r] {
			return contractError("snapshot_binding", p+".selected")
		}
		selected[r] = true
	}
	return nil
}
func checkProbe(v reflect.Value, p string) error {
	x := v.Interface().(ProbeV2)
	if x.AtNS < 0 {
		return contractError("invalid_time", p+".at_ns")
	}
	return nil
}
func checkProbeFile(v reflect.Value, p string) error {
	f := v.Interface().(ProbeFileV2)
	if len(f.Probes) > 100000 {
		return contractError("resource_limit", p+".probes")
	}
	seen := map[string]bool{}
	for _, q := range f.Probes {
		if q.StreamID != f.StreamID || q.ClockDomain != f.ClockDomain {
			return contractError("stream_mismatch", p+".probes[*]")
		}
		key := q.QueryID + "\x00" + string(q.PolicyID)
		if seen[key] {
			return contractError("schedule_mismatch", p+".probes")
		}
		seen[key] = true
	}
	return nil
}
func checkEventFile(v reflect.Value, p string) error {
	f := v.Interface().(EventFileV2)
	if len(f.Events) > 100000 {
		return contractError("resource_limit", p+".events")
	}
	seen := map[string]EventV2{}
	lastSeq, lastTime := int64(0), int64(0)
	for _, e := range f.Events {
		if e.StreamID != f.StreamID || e.ClockDomain != f.ClockDomain || e.Key.ConfigSHA256 != f.ConfigSHA256 {
			return contractError("snapshot_binding", p+".events[*]")
		}
		if old, ok := seen[e.EventID]; ok {
			if old.PayloadSHA256 != e.PayloadSHA256 || old.AcceptedAtNS != e.AcceptedAtNS || old.AcceptedSeq != e.AcceptedSeq {
				return contractError("event_conflict", p+".events[*]")
			}
			continue
		}
		if e.ArrivedAtNS != nil && *e.ArrivedAtNS > e.AcceptedAtNS {
			return contractError("future_event", p+".events[*].arrived_at_ns")
		}
		if lastSeq == math.MaxInt64 || e.AcceptedSeq != lastSeq+1 || e.AcceptedAtNS < lastTime {
			return contractError("sequence_regression", p+".events[*]")
		}
		lastSeq, lastTime = e.AcceptedSeq, e.AcceptedAtNS
		seen[e.EventID] = e
	}
	return nil
}
func checkInputManifest(v reflect.Value, p string) error {
	m := v.Interface().(DynamicInputManifestV2)
	if m.BaselineBundleSHA256 == "" || m.PolicyContractSHA256 != ContractSHA256 {
		return contractError("hash_mismatch", p)
	}
	roles := map[InputRole]bool{}
	paths := map[string]bool{}
	for _, f := range m.Files {
		if roles[f.Role] || paths[f.RelativePath] {
			return contractError("snapshot_conflict", p+".files")
		}
		roles[f.Role] = true
		paths[f.RelativePath] = true
	}
	return nil
}
func checkP1Manifest(v reflect.Value, p string) error {
	m := v.Interface().(P1InputManifestV1)
	if m.FeatureContractSHA256 != P1ContractSHA256 || m.FeatureFile.Role != "features" {
		return contractError("hash_mismatch", p)
	}
	return nil
}

func fieldValueCode(f fieldRule) string {
	if f.ref == "PolicyID" {
		return "unsupported_policy"
	}
	switch f.name {
	case "mode", "implemented_mode":
		return "unsupported_mode"
	case "fallback":
		return "unsupported_fallback"
	case "protocol":
		return "unsupported_protocol"
	}
	return "invalid_value"
}
func checkInheritedAblations(r PolicyRegistryV2, p string) error {
	var base PolicyEntry
	for _, e := range r.Entries {
		if e.PolicyID == "B2" {
			base = e
		}
	}
	for _, e := range r.Entries {
		if e.PolicyID != "V3-P1-NOTIME" && e.PolicyID != "V3-P1-NOOUT" {
			continue
		}
		if e.Ranker.InheritedB2ConfigSHA256 == nil || *e.Ranker.InheritedB2ConfigSHA256 != base.Ranker.ConfigSHA256 {
			return contractError("config_mismatch", p+".entries[*].ranker.inherited_b2_config_sha256")
		}
		if err := sameAblation(base, e, p); err != nil {
			return err
		}
	}
	return nil
}
func sameAblation(base, e PolicyEntry, p string) error {
	flag := "DecayEnabled"
	if e.PolicyID == "V3-P1-NOOUT" {
		flag = "OutcomeJumpEnabled"
	}
	if !equalExcept(reflect.ValueOf(*base.Dynamics), reflect.ValueOf(*e.Dynamics), []string{"ConfigSHA256", flag}) {
		return contractError("config_mismatch", p+".entries[*].dynamics")
	}
	omit := []string{"PolicyID", "PolicyVersion", "DynamicsConfigSHA256", "InheritedB2ConfigSHA256", "Ablation", "ConfigSHA256", "RegistrySHA256"}
	if !equalExcept(reflect.ValueOf(base.Ranker), reflect.ValueOf(e.Ranker), omit) {
		return contractError("config_mismatch", p+".entries[*].ranker")
	}
	return nil
}
func equalExcept(a, b reflect.Value, omit []string) bool {
	for i := 0; i < a.NumField(); i++ {
		if contains(omit, a.Type().Field(i).Name) {
			continue
		}
		if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			return false
		}
	}
	return true
}

var fixedPathFields = buildPathFields()

func buildPathFields() map[string]bool {
	out := map[string]bool{}
	for _, r := range fixedObjects {
		for _, f := range r.fields {
			out[f.name] = true
		}
	}
	native := []reflect.Type{reflect.TypeFor[memoryharness.Candidate](), reflect.TypeFor[memoryharness.CandidateSnapshot](), reflect.TypeFor[memoryharness.Probe](), reflect.TypeFor[memoryharness.Policy](), reflect.TypeFor[memoryharness.CandidateFile](), reflect.TypeFor[memoryharness.BaselineSelection](), reflect.TypeFor[memoryharness.RankedCandidate]()}
	for _, t := range native {
		for i := 0; i < t.NumField(); i++ {
			out[t.Field(i).Tag.Get("json")] = true
		}
	}
	return out
}
func knownSchemaPath(s string) bool {
	if !schemaPathPattern.MatchString(s) {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(s, "$"), ".") {
		part = strings.ReplaceAll(part, "[*]", "")
		if part != "" && !fixedPathFields[part] {
			return false
		}
	}
	return true
}
