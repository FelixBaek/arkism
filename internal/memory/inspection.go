package memory

import (
	"encoding/json"
	"regexp"
	"strings"
)

const inspectionVersion = "101-ingest-v1"

type Inspection struct {
	RulesVersion string `json:"rules_version" bson:"rules_version"`
	Redacted     bool   `json:"redacted" bson:"redacted"`
	PersonalData bool   `json:"personal_data" bson:"personal_data"`
}

// Deterministic local defence; policy authorization still has to inspect the
// sanitized complete payload. These patterns are not a universal DLP engine.
var secretRules = []*regexp.Regexp{
	regexp.MustCompile(`(?:비밀번호|비밀키|인증토큰)\s*[:=]\s*[^\s,;]+`),
	regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----`),
	regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*`),
	// user:password@host inside one value: never across JSON quotes ("http://h:8888","x":"a@b" is not one).
	regexp.MustCompile(`(?i)\b(?:mongodb(?:\+srv)?|postgres(?:ql)?|mysql|redis|https?)://(?:[^\s/@:"\\]|\\.)+:(?:[^\s@"\\]|\\.)+@[^\s"']+`),
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\bAIza[a-zA-Z0-9_-]{20,}\b`),
	regexp.MustCompile(`\b(?:gh[pousr]_[a-zA-Z0-9]{20,}|github_pat_[a-zA-Z0-9_]{20,})\b`),
	regexp.MustCompile(`\bxox[baprs]-[a-zA-Z0-9-]{10,}\b`),
	// Discord bot tokens: base64 bot id . timestamp . HMAC.
	regexp.MustCompile(`\b[MNO][A-Za-z0-9_-]{23,27}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{27,40}\b`),
	regexp.MustCompile(`\b(?:sk|al|rk)-(?:[a-zA-Z0-9_-]{12,})\b`),
	regexp.MustCompile(`(?i)\b(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|token|password|passwd|secret|비밀번호)\\?["']?\s*[:=]\s*(?:"[^"\r\n]+"|'[^'\r\n]+'|[^\s,;]+)`),
}
var personalPattern = regexp.MustCompile(`(?i)([a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}|\b01[016789][- ]?\d{3,4}[- ]?\d{4}\b|\b\d{6}-[1-4]\d{6}\b)`)

func containsSecret(s string) bool {
	for _, r := range secretRules {
		if r.MatchString(s) {
			return true
		}
	}
	return false
}
func validSensitivity(s string) bool {
	return s == "" || s == "public" || s == "internal" || s == "private" || s == "restricted"
}

func inspectCreate(in CreateInput) (CreateInput, Inspection, error) {
	return inspectCreateInput(in, false)
}

func inspectCreateInput(in CreateInput, file bool) (CreateInput, Inspection, error) {
	report := Inspection{RulesVersion: inspectionVersion}
	if in.OnSecret != "" && in.OnSecret != "block" && in.OnSecret != "redact" || !validSensitivity(in.Sensitivity) {
		return CreateInput{}, report, ErrInvalid
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return CreateInput{}, report, ErrInvalid
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return CreateInput{}, report, ErrInvalid
	}
	var walk func(any, bool) (any, error)
	walk = func(v any, redactable bool) (any, error) {
		switch x := v.(type) {
		case string:
			if personalPattern.MatchString(x) {
				report.PersonalData = true
			}
			if containsSecret(x) {
				if in.OnSecret != "redact" || !redactable {
					return nil, ErrSecretDetected
				}
				for _, rule := range secretRules {
					x = rule.ReplaceAllString(x, "[REDACTED]")
				}
				report.Redacted = true
				if containsSecret(x) {
					return nil, ErrSecretDetected
				}
			}
			return x, nil
		case []any:
			for i, value := range x {
				clean, e := walk(value, redactable)
				if e != nil {
					return nil, e
				}
				x[i] = clean
			}
			return x, nil
		case map[string]any:
			for k, value := range x {
				// References, identity and scope cannot be silently rewritten by redaction.
				allowed := redactable && k != "source_id" && k != "id" && k != "collection" && k != "run_id" && k != "evaluation_id" && k != "skill_id" && k != "skill_version"
				clean, e := walk(value, allowed)
				if e != nil {
					return nil, e
				}
				x[k] = clean
			}
			return x, nil
		default:
			return v, nil
		}
	}
	for k, v := range object {
		redactable := k == "text" || k == "summary" || k == "keywords" || k == "sources" || k == "reflection"
		clean, e := walk(v, redactable)
		if e != nil {
			return CreateInput{}, report, e
		}
		object[k] = clean
	}
	clean, e := json.Marshal(object)
	if e != nil {
		return CreateInput{}, report, ErrInvalid
	}
	var out CreateInput
	if json.Unmarshal(clean, &out) != nil {
		return CreateInput{}, report, ErrInvalid
	}
	if out.Sensitivity == "" {
		out.Sensitivity = "internal"
	}
	if report.PersonalData && out.Sensitivity != "restricted" {
		out.Sensitivity = "private"
	}
	// Empty original data does not become meaningful merely by passing a scanner.
	if !file && strings.TrimSpace(out.Text) == "" || len(out.Text) > 64000 || len(out.Summary) > 8000 {
		return CreateInput{}, report, ErrInvalid
	}
	for _, keyword := range out.Keywords {
		if len(keyword) > 256 {
			return CreateInput{}, report, ErrInvalid
		}
	}
	return out, report, nil
}
