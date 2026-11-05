package capture

import (
	"regexp"
	"slices"
	"strings"
)

// Extraction labels beyond the kind: the rule wording, a decision's status, a temporary
// limit, the named entities and the relations between them. The host passes on only
// well-formed ones; the server checks them again, and a person's approval is what acts on them.

const labelProps = `"rule": {"type": "string", "enum": ["always", "never", ""]}, "decision_status": {"type": "string", "enum": ["proposed", "confirmed", "dropped", ""]}, "temporary": {"type": "boolean"}, ` +
	`"entities": {"type": "array", "items": {"type": "object", "additionalProperties": false, "required": ["name", "type"], "properties": {"name": {"type": "string"}, "type": {"type": "string", "enum": ["person", "organization", "project", "product", "tool", "place"]}}}}, ` +
	`"relations": {"type": "array", "items": {"type": "object", "additionalProperties": false, "required": ["source", "relation", "target"], "properties": {"source": {"type": "string"}, "relation": {"type": "string"}, "target": {"type": "string"}}}}`

const claimLabelsMax = 8

var (
	claimEntityTypes  = []string{"person", "organization", "project", "product", "tool", "place"}
	claimRelationName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// claimExtractionLabels copies the model's well-formed labels onto claim; malformed ones are dropped.
func claimExtractionLabels(c, claim map[string]any) {
	if r := str(c["rule"]); r == "always" || r == "never" {
		claim["rule"] = r
	}
	if st := str(c["decision_status"]); claim["kind"] == "decision" && slices.Contains([]string{"proposed", "confirmed", "dropped"}, st) {
		claim["decision_status"] = st
	}
	if t, _ := c["temporary"].(bool); t {
		claim["temporary"] = true
	}
	names := map[string]bool{}
	var entities []any
	list, _ := c["entities"].([]any)
	for _, raw := range list {
		e, _ := raw.(map[string]any)
		name, typ := head(pyStrip(str(e["name"])), 80), str(e["type"])
		if name != "" && slices.Contains(claimEntityTypes, typ) && !names[strings.ToLower(name)] && len(entities) < claimLabelsMax {
			names[strings.ToLower(name)] = true
			entities = append(entities, map[string]any{"name": name, "type": typ})
		}
	}
	if len(entities) > 0 {
		claim["entities"] = entities
	}
	var relations []any
	rels, _ := c["relations"].([]any)
	for _, raw := range rels {
		r, _ := raw.(map[string]any)
		src, rel, dst := head(pyStrip(str(r["source"])), 80), str(r["relation"]), head(pyStrip(str(r["target"])), 80)
		ends := names[strings.ToLower(src)] && names[strings.ToLower(dst)] && !strings.EqualFold(src, dst)
		if ends && claimRelationName.MatchString(rel) && len(relations) < claimLabelsMax {
			relations = append(relations, map[string]any{"source": src, "relation": rel, "target": dst})
		}
	}
	if len(relations) > 0 {
		claim["relations"] = relations
	}
}
