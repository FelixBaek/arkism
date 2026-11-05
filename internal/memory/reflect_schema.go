package memory

// A small JSON Schema subset for reflect's response_schema: type, properties,
// required, additionalProperties (boolean), items, enum, minItems/maxItems,
// plus title/description as notes. A schema using any other keyword is refused
// up front, so a model answer is never reported valid against a rule we skipped.

import (
	"encoding/json"
	"math"
	"reflect"
	"slices"
)

const reflectSchemaDepth = 8

var schemaTypes = []string{"object", "array", "string", "number", "integer", "boolean", "null"}

func parseReflectSchema(raw json.RawMessage) (map[string]any, error) {
	var schema map[string]any
	if decodeStrict(raw, &schema) != nil || schema == nil || !validSchemaNode(schema, 0) {
		return nil, ErrInvalid
	}
	return schema, nil
}

func validSchemaNode(node map[string]any, depth int) bool {
	if depth > reflectSchemaDepth {
		return false
	}
	for key, v := range node {
		if !validSchemaKeyword(key, v, depth) {
			return false
		}
	}
	return true
}

func validSchemaKeyword(key string, v any, depth int) bool {
	switch key {
	case "title", "description":
		_, ok := v.(string)
		return ok
	case "type":
		t, ok := v.(string)
		return ok && slices.Contains(schemaTypes, t)
	case "additionalProperties":
		_, ok := v.(bool)
		return ok
	case "enum":
		list, ok := v.([]any)
		return ok && len(list) > 0
	case "minItems", "maxItems":
		n, ok := v.(float64)
		return ok && n >= 0 && n == math.Trunc(n)
	case "required":
		list, ok := v.([]any)
		return ok && !slices.ContainsFunc(list, func(x any) bool { _, s := x.(string); return !s })
	case "items":
		sub, ok := v.(map[string]any)
		return ok && validSchemaNode(sub, depth+1)
	case "properties":
		props, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for _, p := range props {
			sub, ok := p.(map[string]any)
			if !ok || !validSchemaNode(sub, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}

// schemaAccepts reports whether v (decoded JSON) is valid against a schema parseReflectSchema accepted.
func schemaAccepts(schema map[string]any, v any) bool {
	if t, ok := schema["type"].(string); ok && !schemaTypeMatches(t, v) {
		return false
	}
	if enum, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return reflect.DeepEqual(e, v) }) {
		return false
	}
	switch val := v.(type) {
	case map[string]any:
		return schemaObjectAccepts(schema, val)
	case []any:
		if n, ok := schema["minItems"].(float64); ok && float64(len(val)) < n {
			return false
		}
		if n, ok := schema["maxItems"].(float64); ok && float64(len(val)) > n {
			return false
		}
		items, _ := schema["items"].(map[string]any)
		return items == nil || !slices.ContainsFunc(val, func(x any) bool { return !schemaAccepts(items, x) })
	}
	return true
}

func schemaObjectAccepts(schema map[string]any, val map[string]any) bool {
	props, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, name := range required {
		if _, ok := val[name.(string)]; !ok {
			return false
		}
	}
	for name, x := range val {
		sub, known := props[name].(map[string]any)
		if !known {
			if extra, ok := schema["additionalProperties"].(bool); ok && !extra {
				return false
			}
			continue
		}
		if !schemaAccepts(sub, x) {
			return false
		}
	}
	return true
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
