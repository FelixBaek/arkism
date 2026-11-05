package memory

// A bounded JSON Schema validator for response_schema (reflect and mental-model
// refreshes). It checks the 2020-12 vocabulary: type (one or a list), enum, const,
// numbers (minimum, maximum, exclusive bounds, multipleOf), strings (minLength,
// maxLength, pattern as RE2), arrays (items, prefixItems, contains with
// minContains/maxContains, minItems, maxItems, uniqueItems), objects (properties,
// patternProperties, additionalProperties, propertyNames, required,
// dependentRequired, dependentSchemas, minProperties, maxProperties), the
// applicators allOf, anyOf, oneOf, not, if/then/else, and local references
// ($ref "#..." into $defs or definitions). Annotations (title, description,
// format, default, examples, $comment, deprecated, readOnly, writeOnly) are kept
// but not checked. Any other keyword, a remote $ref or an $id is refused up front,
// so an answer is never reported valid against a rule we skipped.
// The schema is bounded (nodes, depth, pattern size) and so is one check (steps).

import (
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	schemaDepthMax   = 16
	schemaNodesMax   = 512
	schemaStepsMax   = 20000
	schemaPatternMax = 512
)

var schemaTypes = []string{"object", "array", "string", "number", "integer", "boolean", "null"}

// schemaKeywordShape says what value a keyword takes.
var schemaKeywordShape = map[string]string{
	"title": "string", "description": "string", "format": "string", "$schema": "string", "$comment": "string",
	"default": "any", "examples": "any", "const": "any",
	"deprecated": "bool", "readOnly": "bool", "writeOnly": "bool", "uniqueItems": "bool",
	"type": "type", "enum": "enum", "required": "strings", "pattern": "pattern", "$ref": "ref",
	"minimum": "number", "maximum": "number", "exclusiveMinimum": "number", "exclusiveMaximum": "number", "multipleOf": "positive",
	"minItems": "count", "maxItems": "count", "minLength": "count", "maxLength": "count",
	"minProperties": "count", "maxProperties": "count", "minContains": "count", "maxContains": "count",
	"items": "schema", "additionalProperties": "schema", "propertyNames": "schema", "contains": "schema",
	"not": "schema", "if": "schema", "then": "schema", "else": "schema",
	"prefixItems": "schemas", "allOf": "schemas", "anyOf": "schemas", "oneOf": "schemas",
	"properties": "schemaMap", "$defs": "schemaMap", "definitions": "schemaMap", "dependentSchemas": "schemaMap",
	"patternProperties": "patternMap", "dependentRequired": "requiredMap",
}

type jsonSchema struct {
	root     map[string]any
	patterns map[string]*regexp.Regexp
}

func parseReflectSchema(raw json.RawMessage) (*jsonSchema, error) {
	var root map[string]any
	if decodeStrict(raw, &root) != nil || root == nil {
		return nil, ErrInvalid
	}
	s := &jsonSchema{root: root, patterns: map[string]*regexp.Regexp{}}
	nodes := 0
	if !s.validNode(root, 0, &nodes) {
		return nil, ErrInvalid
	}
	return s, nil
}

func (s *jsonSchema) validNode(node any, depth int, nodes *int) bool {
	*nodes++
	if depth > schemaDepthMax || *nodes > schemaNodesMax {
		return false
	}
	if _, ok := node.(bool); ok {
		return true
	}
	m, ok := node.(map[string]any)
	if !ok {
		return false
	}
	for key, v := range m {
		if !s.validKeyword(schemaKeywordShape[key], v, depth, nodes) {
			return false
		}
	}
	return true
}

func (s *jsonSchema) validKeyword(shape string, v any, depth int, nodes *int) bool {
	sub := func(x any) bool { return s.validNode(x, depth+1, nodes) }
	switch shape {
	case "any", "string", "bool", "number", "positive", "count", "type", "enum", "strings":
		return validScalarKeyword(shape, v)
	case "pattern":
		p, ok := v.(string)
		return ok && s.compile(p)
	case "ref":
		ref, ok := v.(string)
		return ok && s.resolve(ref) != nil
	case "schema":
		return sub(v)
	case "schemas":
		list, ok := v.([]any)
		return ok && len(list) > 0 && !slices.ContainsFunc(list, func(x any) bool { return !sub(x) })
	case "schemaMap", "patternMap":
		return s.validSchemaMap(v, shape == "patternMap", sub)
	case "requiredMap":
		m, ok := v.(map[string]any)
		return ok && !slices.ContainsFunc(mapValues(m), func(x any) bool { return !schemaStrings(x) })
	}
	return false
}

// validScalarKeyword checks a keyword whose value is not a subschema.
func validScalarKeyword(shape string, v any) bool {
	switch shape {
	case "any":
		return true
	case "positive", "count":
		n, ok := v.(float64)
		return ok && (shape == "positive" && n > 0 || shape == "count" && n >= 0 && n == math.Trunc(n))
	case "type":
		return validSchemaType(v)
	case "enum":
		list, ok := v.([]any)
		return ok && len(list) > 0
	case "strings":
		return schemaStrings(v)
	}
	return schemaShapeOf(v) == shape
}

func (s *jsonSchema) validSchemaMap(v any, patternKeys bool, sub func(any) bool) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for k, x := range m {
		if patternKeys && !s.compile(k) || !sub(x) {
			return false
		}
	}
	return true
}

func schemaShapeOf(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "number"
	}
	return ""
}

func validSchemaType(v any) bool {
	if t, ok := v.(string); ok {
		return slices.Contains(schemaTypes, t)
	}
	list, ok := v.([]any)
	return ok && len(list) > 0 && !slices.ContainsFunc(list, func(x any) bool { t, ok := x.(string); return !ok || !slices.Contains(schemaTypes, t) })
}

func schemaStrings(v any) bool {
	list, ok := v.([]any)
	return ok && !slices.ContainsFunc(list, func(x any) bool { _, s := x.(string); return !s })
}

func mapValues(m map[string]any) []any {
	out := make([]any, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (s *jsonSchema) compile(p string) bool {
	if len(p) > schemaPatternMax {
		return false
	}
	if _, ok := s.patterns[p]; ok {
		return true
	}
	re, err := regexp.Compile(p)
	if err == nil {
		s.patterns[p] = re
	}
	return err == nil
}

// resolve follows a local JSON pointer ("#", "#/$defs/name"); remote references resolve to nil.
func (s *jsonSchema) resolve(ref string) any {
	if !strings.HasPrefix(ref, "#") {
		return nil
	}
	var node any = s.root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#"), "/")[1:] {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch cur := node.(type) {
		case map[string]any:
			node = cur[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(cur) {
				return nil
			}
			node = cur[i]
		default:
			return nil
		}
	}
	switch node.(type) {
	case bool, map[string]any:
		return node
	}
	return nil
}

// schemaAccepts reports whether v (decoded JSON) is valid against a schema parseReflectSchema accepted.
func schemaAccepts(schema *jsonSchema, v any) bool {
	c := &schemaCheck{s: schema}
	return c.accepts(schema.root, v, 0) && c.steps <= schemaStepsMax
}

type schemaCheck struct {
	s     *jsonSchema
	steps int
}

func (c *schemaCheck) accepts(node, v any, depth int) bool {
	c.steps++
	if c.steps > schemaStepsMax || depth > 4*schemaDepthMax { // a $ref cycle ends here
		return false
	}
	if b, ok := node.(bool); ok {
		return b
	}
	n, _ := node.(map[string]any)
	if !c.applicators(n, v, depth) || !schemaValueKeywords(n, v) {
		return false
	}
	switch val := v.(type) {
	case map[string]any:
		return c.object(n, val, depth)
	case []any:
		return c.array(n, val, depth)
	case string:
		return c.str(n, val)
	case float64:
		return schemaNumber(n, val)
	}
	return true
}

func (c *schemaCheck) applicators(n map[string]any, v any, depth int) bool {
	ok := func(x any) bool { return c.accepts(x, v, depth+1) }
	if ref, has := n["$ref"].(string); has && !ok(c.s.resolve(ref)) {
		return false
	}
	if not, has := n["not"]; has && ok(not) {
		return false
	}
	if cond, has := n["if"]; has {
		branch, set := n["else"]
		if ok(cond) {
			branch, set = n["then"]
		}
		if set && !ok(branch) {
			return false
		}
	}
	all, _ := n["allOf"].([]any)
	anyOf, _ := n["anyOf"].([]any)
	oneOf, _ := n["oneOf"].([]any)
	matched := 0
	for _, x := range oneOf {
		if ok(x) {
			matched++
		}
	}
	return !slices.ContainsFunc(all, func(x any) bool { return !ok(x) }) &&
		(anyOf == nil || slices.ContainsFunc(anyOf, ok)) && (oneOf == nil || matched == 1)
}

func schemaValueKeywords(n map[string]any, v any) bool {
	switch t := n["type"].(type) {
	case string:
		if !schemaTypeMatches(t, v) {
			return false
		}
	case []any:
		if !slices.ContainsFunc(t, func(x any) bool { s, _ := x.(string); return schemaTypeMatches(s, v) }) {
			return false
		}
	}
	if enum, ok := n["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return reflect.DeepEqual(e, v) }) {
		return false
	}
	c, has := n["const"]
	return !has || reflect.DeepEqual(c, v)
}

// schemaBound reports whether x keeps the schema's lower (or upper) bound keyword, if set.
func schemaBound(n map[string]any, key string, x float64, upper bool) bool {
	b, ok := n[key].(float64)
	return !ok || upper && x <= b || !upper && x >= b
}

func schemaNumber(n map[string]any, x float64) bool {
	if lo, ok := n["exclusiveMinimum"].(float64); ok && x <= lo {
		return false
	}
	if hi, ok := n["exclusiveMaximum"].(float64); ok && x >= hi {
		return false
	}
	if m, ok := n["multipleOf"].(float64); ok {
		q := x / m
		if math.IsInf(q, 0) || math.Abs(q-math.Round(q)) > 1e-9 {
			return false
		}
	}
	return schemaBound(n, "minimum", x, false) && schemaBound(n, "maximum", x, true)
}

func (c *schemaCheck) str(n map[string]any, val string) bool {
	runes := float64(utf8.RuneCountInString(val))
	if p, ok := n["pattern"].(string); ok && !c.s.patterns[p].MatchString(val) {
		return false
	}
	return schemaBound(n, "minLength", runes, false) && schemaBound(n, "maxLength", runes, true)
}

func (c *schemaCheck) array(n map[string]any, val []any, depth int) bool {
	count := float64(len(val))
	if !schemaBound(n, "minItems", count, false) || !schemaBound(n, "maxItems", count, true) {
		return false
	}
	prefix, _ := n["prefixItems"].([]any)
	items, hasItems := n["items"]
	for i, x := range val {
		switch {
		case i < len(prefix):
			if !c.accepts(prefix[i], x, depth+1) {
				return false
			}
		case hasItems && !c.accepts(items, x, depth+1):
			return false
		}
	}
	if unique, _ := n["uniqueItems"].(bool); unique && !schemaUnique(val) {
		return false
	}
	contains, has := n["contains"]
	if !has {
		return true
	}
	found := 0
	for _, x := range val {
		if c.accepts(contains, x, depth+1) {
			found++
		}
	}
	lo := 1.0
	if m, ok := n["minContains"].(float64); ok {
		lo = m
	}
	return float64(found) >= lo && schemaBound(n, "maxContains", float64(found), true)
}

func schemaUnique(val []any) bool {
	for i := range val {
		for j := i + 1; j < len(val); j++ {
			if reflect.DeepEqual(val[i], val[j]) {
				return false
			}
		}
	}
	return true
}

func (c *schemaCheck) object(n map[string]any, val map[string]any, depth int) bool {
	count := float64(len(val))
	if !schemaBound(n, "minProperties", count, false) || !schemaBound(n, "maxProperties", count, true) {
		return false
	}
	required, _ := n["required"].([]any)
	for _, name := range required {
		if _, ok := val[name.(string)]; !ok {
			return false
		}
	}
	if !c.dependencies(n, val, depth) {
		return false
	}
	for name, x := range val {
		if !c.property(n, name, x, depth) {
			return false
		}
	}
	return true
}

func (c *schemaCheck) dependencies(n map[string]any, val map[string]any, depth int) bool {
	deps, _ := n["dependentRequired"].(map[string]any)
	for name, need := range deps {
		if _, present := val[name]; !present {
			continue
		}
		for _, other := range need.([]any) {
			if _, ok := val[other.(string)]; !ok {
				return false
			}
		}
	}
	schemas, _ := n["dependentSchemas"].(map[string]any)
	for name, sub := range schemas {
		if _, present := val[name]; present && !c.accepts(sub, val, depth+1) {
			return false
		}
	}
	return true
}

// property checks one member: propertyNames, then properties and patternProperties,
// and additionalProperties when neither named it.
func (c *schemaCheck) property(n map[string]any, name string, x any, depth int) bool {
	if names, ok := n["propertyNames"]; ok && !c.accepts(names, name, depth+1) {
		return false
	}
	props, _ := n["properties"].(map[string]any)
	sub, named := props[name]
	if named && !c.accepts(sub, x, depth+1) {
		return false
	}
	patterns, _ := n["patternProperties"].(map[string]any)
	for p, sub := range patterns {
		if c.s.patterns[p].MatchString(name) {
			named = true
			if !c.accepts(sub, x, depth+1) {
				return false
			}
		}
	}
	extra, has := n["additionalProperties"]
	return named || !has || c.accepts(extra, x, depth+1)
}

func schemaTypeMatches(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n == math.Trunc(n)
	case "boolean":
		_, ok := v.(bool)
		return ok
	}
	return v == nil
}
