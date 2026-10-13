package memory

import "strings"

// This is a lexical comparison key, not a semantic equivalence or merge rule.
// Version the recipe so later language-specific normalization cannot silently
// reinterpret keys already stored with original proposals.
func candidateNormalizedKey(r Record) string {
	return "v1:" + ContentHash(structuredText(struct {
		Version int    `json:"version"`
		Scope   Scope  `json:"scope"`
		Kind    string `json:"kind"`
		Text    string `json:"text"`
	}{1, r.Scope, r.Kind, strings.ToLower(strings.Join(strings.Fields(r.Text), " "))}))
}
