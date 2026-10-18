package capture

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

const claimContextMaxBytes = 16 << 10
const claimContextBatchBytes = 64 << 10

// The model supplies descriptions, never evidence authority or observation time.
const claimContextSchema = `"memory_context":{"anyOf":[{"type":"null"},{"type":"object","additionalProperties":false,"required":["domains","applicability","uncertainty","valid_from","semantic_kind"],"properties":{"domains":{"type":"array","minItems":1,"maxItems":2,"uniqueItems":true,"items":{"type":"string","enum":["personal","work"]}},"applicability":{"type":"string","maxLength":2000},"uncertainty":{"type":"string","maxLength":512},"valid_from":{"type":"string","maxLength":256},"semantic_kind":{"type":"string","enum":["fact","preference","correction","lesson","failure_recovery"]}}}]}`

type claimMemoryContext struct {
	Domains       []string
	Applicability string
	Uncertainty   string
	ValidFrom     string
	SemanticKind  string
}

func (c *claimMemoryContext) wire() host.Obj {
	return host.Obj{
		{Key: "domains", Value: append([]string(nil), c.Domains...)},
		{Key: "applicability", Value: c.Applicability},
		{Key: "uncertainty", Value: c.Uncertainty},
		{Key: "valid_from", Value: c.ValidFrom},
		{Key: "semantic_kind", Value: c.SemanticKind},
		// Legacy extraction cannot independently establish canonical authority.
		{Key: "evidence_kind", Value: "inference"},
	}
}

func claimContextObject(v any) (map[string]any, bool) {
	if row, ok := v.(host.Obj); ok {
		seen := map[string]bool{}
		for _, field := range row {
			if seen[field.Key] {
				return nil, false
			}
			seen[field.Key] = true
		}
	}
	row, err := asDict(v)
	if err != nil || len(row) != 5 {
		return nil, false
	}
	for key := range row {
		if !slices.Contains([]string{"domains", "applicability", "uncertainty", "valid_from", "semantic_kind"}, key) {
			return nil, false
		}
	}
	return row, true
}

func claimContextDomains(v any) ([]string, bool) {
	var values []any
	switch list := v.(type) {
	case []any:
		values = list
	case []string:
		for _, value := range list {
			values = append(values, value)
		}
	default:
		return nil, false
	}
	if len(values) < 1 || len(values) > 2 {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		name, ok := value.(string)
		if !ok || name != "personal" && name != "work" || slices.Contains(result, name) {
			return nil, false
		}
		result = append(result, name)
	}
	slices.Sort(result)
	return result, true
}

func claimContextText(v any, limit int) (string, bool) {
	text, ok := v.(string)
	return text, ok && utf8.ValidString(text) && !strings.ContainsRune(text, 0) && utf8.RuneCountInString(text) <= limit
}

func parseClaimContext(v any) (*claimMemoryContext, int, string) {
	if v == nil {
		return nil, 0, ""
	}
	row, ok := claimContextObject(v)
	if !ok {
		return nil, 0, "context_fields_invalid"
	}
	domains, ok := claimContextDomains(row["domains"])
	if !ok {
		return nil, 0, "context_domains_invalid"
	}
	applicability, appOK := claimContextText(row["applicability"], 2000)
	uncertainty, uncertainOK := claimContextText(row["uncertainty"], 512)
	validFrom, fromOK := claimContextText(row["valid_from"], 256)
	if !appOK || !uncertainOK || !fromOK {
		return nil, 0, "context_text_invalid"
	}
	kind, ok := row["semantic_kind"].(string)
	if !ok || !slices.Contains([]string{"fact", "preference", "correction", "lesson", "failure_recovery"}, kind) {
		return nil, 0, "context_kind_invalid"
	}
	// Classification labels alone add no condition. Domain restrictions belong
	// in applicability as well, so they survive the human-readable memory body.
	if strings.TrimSpace(applicability+uncertainty+validFrom) == "" {
		return nil, 0, ""
	}
	context := &claimMemoryContext{domains, applicability, uncertainty, validFrom, kind}
	// Match the capture spool's UTF-8 JSON representation, including escaping.
	encoded, err := host.Dumps(context.wire(), host.DumpOpts{NoASCII: true})
	if err != nil || len(encoded) > claimContextMaxBytes {
		return nil, 0, "context_bytes_exceeded"
	}
	return context, len(encoded), ""
}

// Literal support prevents invented metadata wording, not selective quotation or
// false semantic implication. It never promotes inference to source authority.
func claimContextSupported(context *claimMemoryContext, row map[string]any, users []any) bool {
	quote, ok := row["quote"].(string)
	if !ok || users == nil {
		return false // An aggregate cannot establish a user-message boundary.
	}
	matches, err := claimQuoteMatches(quote, users)
	if err != nil || len(matches) != 1 {
		return false // Never guess which user's source or archive ref to use.
	}
	span := claimEvidenceSpan(quote)
	for _, phrase := range []string{context.Applicability, context.Uncertainty, context.ValidFrom} {
		if phrase != "" && !strings.Contains(span, phrase) {
			return false
		}
	}
	return true
}

// ClaimContextRejection names an omitted model item without storing its text.
type ClaimContextRejection struct {
	Index       int    `json:"index"`
	Reason      string `json:"reason"`
	TextSHA256  string `json:"text_sha256"`
	QuoteSHA256 string `json:"quote_sha256"`
}

// ClaimContextReceipt binds rejected items to the prepared extraction inputs.
// Scope and event are deliberately absent until a delivery caller knows them.
type ClaimContextReceipt struct {
	Version          int                     `json:"version"`
	BindingStatus    string                  `json:"binding_status"`
	UserInputSHA256  string                  `json:"user_input_sha256"`
	SourceRowsSHA256 string                  `json:"source_rows_sha256"`
	Rejected         []ClaimContextRejection `json:"rejected"`
}

func claimContextSourceDigest(users []any, userText string) (string, error) {
	if users == nil {
		return sdigest(host.Obj{{Key: "aggregate_only", Value: userText}}), nil
	}
	rows := []any{}
	for _, user := range users {
		row, err := asDict(user)
		if err != nil {
			return "", err
		}
		text, textOK := row["text"].(string)
		role, roleOK := row["role"].(string)
		_, rolePresent := row["role"]
		at, _ := row["at"].(string)
		rows = append(rows, host.Obj{{Key: "text", Value: text}, {Key: "text_is_string", Value: textOK},
			{Key: "role", Value: role}, {Key: "role_is_string", Value: roleOK}, {Key: "role_present", Value: rolePresent}, {Key: "at", Value: at}})
	}
	return sdigest(rows), nil
}

func newClaimContextReceipt(users []any, userText string) (*ClaimContextReceipt, error) {
	source, err := claimContextSourceDigest(users, userText)
	if err != nil {
		return nil, err
	}
	return &ClaimContextReceipt{1, "unbound", sha256Hex([]byte(userText)), source, []ClaimContextRejection{}}, nil
}

func (r *ClaimContextReceipt) reject(index int, reason, text, quote string) {
	r.Rejected = append(r.Rejected, ClaimContextRejection{index, reason, sha256Hex([]byte(text)), sha256Hex([]byte(quote))})
	metric(kv("event", "capture_claim_context_withheld"), kv("reason", reason), kv("claim_index", index))
}

func buildClaimsWithReceipt(raw []any, userText string, related []map[string]any, users []any) ([]any, *ClaimContextReceipt, error) {
	claims := []any{}
	var receipt *ClaimContextReceipt
	used := 0
	for index, value := range raw[:min(len(raw), 16)] {
		row, err := asDict(value)
		if err != nil {
			return nil, nil, err
		}
		text := pyStrip(str(or(row["text"], "")))
		if text == "" {
			continue
		}
		context, size, reason := parseClaimContext(row["memory_context"])
		if reason == "" && context != nil && !claimContextSupported(context, row, users) {
			reason = "context_evidence_unbound"
		}
		if reason == "" && used+size > claimContextBatchBytes {
			reason = "context_batch_bytes_exceeded"
		}
		if reason != "" {
			if receipt == nil {
				receipt, err = newClaimContextReceipt(users, userText)
				if err != nil {
					return nil, nil, err
				}
			}
			receipt.reject(index, reason, head(text, 4000), str(or(row["quote"], "")))
			continue
		}
		claim, err := buildClaim(row, text, userText, related, users)
		if err != nil {
			return nil, nil, err
		}
		if context != nil {
			claim["memory_context"] = context.wire()
		}
		used += size
		claims = append(claims, claim)
	}
	return claims, receipt, nil
}

// Extraction version is fixed in prepared requests; backfill and old prepared
// requests retain their original v2 generator.
func extractionGenerator(pipeline string) host.Obj {
	gen := generator(pipeline)
	gen.Set("prompt_version", "capture-extract-v4")
	return gen
}

func preparedClaimGenerator(prepared map[string]any, pipeline string) (host.Obj, error) {
	value, exists := prepared["generator"]
	if !exists {
		return generator(pipeline), nil
	}
	row, err := asDict(value)
	if err != nil || len(row) != 3 {
		return nil, runtimeErr("capture_generator_invalid")
	}
	gen := host.Obj{}
	for _, key := range []string{"model", "prompt_version", "pipeline_version"} {
		text, ok := row[key].(string)
		if !ok || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text) > 512 {
			return nil, runtimeErr("capture_generator_invalid")
		}
		gen = append(gen, host.KV{Key: key, Value: text})
	}
	return gen, nil
}
