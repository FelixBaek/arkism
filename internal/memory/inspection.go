package memory

import (
	"encoding/json"
	"strings"

	"github.com/FelixBaek/arkism/internal/secretscan"
)

const inspectionVersion = "101-ingest-v2"

type Inspection struct {
	RulesVersion string `json:"rules_version" bson:"rules_version"`
	Redacted     bool   `json:"redacted" bson:"redacted"`
	PersonalData bool   `json:"personal_data" bson:"personal_data"`
}

// The detector lives in internal/secretscan so host tools judge text the same way.
var (
	secretRules     = secretscan.Rules
	personalPattern = secretscan.Personal
)

func containsSecret(s string) bool { return secretscan.Contains(s) }

func redactSecrets(s string) string { return secretscan.Redact(s, "[REDACTED]") }

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
			clean, e := inspectString(x, in.OnSecret, redactable, &report)
			if e != nil {
				return nil, e
			}
			return clean, nil
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
	if !inspectedCreateLimits(out, file) {
		return CreateInput{}, report, ErrInvalid
	}
	return out, report, nil
}

// inspectString records personal data in report and redacts secrets when allowed.
func inspectString(x, onSecret string, redactable bool, report *Inspection) (string, error) {
	if personalPattern.MatchString(x) {
		report.PersonalData = true
	}
	if containsSecret(x) {
		if onSecret != "redact" || !redactable {
			return "", ErrSecretDetected
		}
		x = redactSecrets(x)
		report.Redacted = true
		if containsSecret(x) {
			return "", ErrSecretDetected
		}
	}
	return x, nil
}
func inspectedCreateLimits(out CreateInput, file bool) bool {
	// Empty original data does not become meaningful merely by passing a scanner.
	if !file && strings.TrimSpace(out.Text) == "" || len(out.Text) > 64000 || len(out.Summary) > 8000 {
		return false
	}
	for _, keyword := range out.Keywords {
		if len(keyword) > 256 {
			return false
		}
	}
	return true
}
