package memory

// The content gate on a skill the dream promotes on its own: a draft may only restate what the version
// it replaces or the lessons behind it already carry. A new address, a piped download into a shell, a
// root or sudo step, a permission change, an access grant, a check bypass or a new allowed tool, or a
// wider analysis (more tools, APIs, permissions, irreversible actions or dependencies, or newly writing
// or sending data out), leaves the draft for a person. Lessons are agent text, so this is the line a
// stored instruction would have to cross to reach a routed skill. The rules match instructions, not bare
// words, so "root cause" or "review the grant" in a draft does not stop it.

import (
	"regexp"
	"slices"
	"strings"
)

var riskyDraftRules = []struct {
	id string
	re *regexp.Regexp
}{
	{"url", regexp.MustCompile(`(?i)\b(?:https?|ftp|ssh|file)://[^\s<>()"'` + "`" + `]+`)},
	{"pipe_shell", regexp.MustCompile(`(?i)\b(?:curl|wget|iwr|irm|invoke-webrequest)\b[^\n|]*\|\s*(?:sudo\s+)?(?:ba|z|da)?sh\b|\biex\b`)},
	{"sudo", regexp.MustCompile(`(?i)\bsudo\b`)},
	{"run_as_root", regexp.MustCompile(`(?i)\b(?:run|execute|start|log\s+in)\b[^\n.]{0,30}\bas\s+(?:the\s+)?(?:root|admin(?:istrator)?)\b|\bsu\s+-?\s*root\b`)},
	{"chmod_chown", regexp.MustCompile(`(?i)\b(?:chmod|chown|icacls|setfacl)\b`)},
	{"grant_access", regexp.MustCompile(`(?i)\b(?:grant|give|elevate)\b[^\n.]{0,40}\b(?:access|permissions?|privileges?|rights|admin(?:istrator)?)\b`)},
	{"bypass", regexp.MustCompile(`(?i)--no-verify\b|\bdangerously[-\w]*|\b(?:bypass|skip|disable)\w*\s+(?:the\s+)?(?:review|checks?|permission\s+prompts?|sandbox|approval)\b`)},
	{"allowed_tools", regexp.MustCompile(`(?i)allowed[-_ ]?tools\s*:[^\n]*`)},
	{"destructive", regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f|\bmkfs\b|\bdd\s+if=|\beval\s+\$|\bnc\s+-[a-z]*e\b`)},
}

// riskyAddition is the rule and token of the first thing the draft adds that allowed does not carry
// ("" when none). The token is the rule's own keyword or a cleaned address host, never draft prose.
func riskyAddition(draft, allowed string, a, base *SkillAnalysis) (string, string) {
	known := strings.ToLower(allowed)
	for _, r := range riskyDraftRules {
		for _, m := range r.re.FindAllString(draft, -1) {
			if r.id == "allowed_tools" {
				if tool := newAllowedTool(m, known); tool != "" {
					return r.id, tool
				}
				continue
			}
			if !strings.Contains(known, strings.ToLower(strings.Trim(m, " \t`"))) {
				return r.id, riskyToken(r.id, m)
			}
		}
	}
	if a != nil {
		if field := widerAnalysis(*a, base, known); field != "" {
			return "analysis", field
		}
	}
	return "", ""
}

// newAllowedTool is the first tool an allowed-tools line lists that allowed does not name.
func newAllowedTool(line, known string) string {
	_, list, _ := strings.Cut(line, ":")
	for _, tool := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '[' || r == ']' }) {
		if tool = strings.Trim(tool, "\"'`"); tool != "" && !strings.Contains(known, strings.ToLower(tool)) {
			return cleanToken(tool)
		}
	}
	return ""
}

func riskyToken(rule, match string) string {
	switch rule {
	case "url":
		_, rest, _ := strings.Cut(match, "://")
		host, _, _ := strings.Cut(rest, "/")
		return cleanToken(host)
	case "pipe_shell":
		return "curl|sh"
	case "grant_access":
		words := strings.Fields(strings.ToLower(match))
		return cleanToken(words[0]) + ".." + cleanToken(words[len(words)-1])
	case "run_as_root":
		return "run as root"
	}
	return cleanToken(strings.ToLower(strings.Fields(match)[0]))
}

// cleanToken keeps a short run of plain characters, so a reported token cannot carry an instruction.
func cleanToken(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_|*", r) {
			return r
		}
		return -1
	}, s)
	return s[:min(len(s), 48)]
}

// widerAnalysis names the analysis field where the draft claims a capability the base analysis does not
// have and the lessons do not name ("" when none).
func widerAnalysis(a SkillAnalysis, base *SkillAnalysis, known string) string {
	var b SkillAnalysis
	if base != nil {
		b = *base
	}
	if a.Writes && !b.Writes {
		return "writes"
	}
	if a.ExternalTransfer && !b.ExternalTransfer {
		return "external_transfer"
	}
	fields := []string{"tools", "apis", "permissions", "irreversible_actions", "dependencies", "secret_names"}
	now := [][]string{a.Tools, a.APIs, a.Permissions, a.IrreversibleActions, a.Dependencies, a.SecretNames}
	was := [][]string{b.Tools, b.APIs, b.Permissions, b.IrreversibleActions, b.Dependencies, b.SecretNames}
	for i, list := range now {
		for _, item := range list {
			if !slices.Contains(was[i], item) && !strings.Contains(known, strings.ToLower(item)) {
				return fields[i]
			}
		}
	}
	return ""
}

// analysisProse joins an analysis's text fields (its lists are compared by widerAnalysis).
func analysisProse(a *SkillAnalysis) string {
	if a == nil {
		return ""
	}
	parts := []string{a.Name, a.Description, a.Problem, a.InputFormat, a.OutputFormat}
	for _, list := range [][]string{a.UseWhen, a.AvoidWhen, a.SuccessCriteria, a.FailureTypes, a.Verification} {
		parts = append(parts, list...)
	}
	return strings.Join(parts, "\n")
}
