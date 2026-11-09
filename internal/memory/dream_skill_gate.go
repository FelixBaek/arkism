package memory

// The content gate on a skill the dream promotes on its own: a draft may only restate what the version
// it replaces or the lessons behind it already carry. Any new address, shell command, tool grant or
// permission wording, or a wider analysis (more tools, APIs, permissions, irreversible actions or
// dependencies, or newly writing or sending data out), leaves the draft for a person. Lessons are agent
// text, so this is the line a stored instruction would have to cross to reach a routed skill.

import (
	"regexp"
	"slices"
	"strings"
)

var riskyDraftPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"url", regexp.MustCompile(`(?i)\b(?:https?|ftp|ssh|file)://[^\s<>()"'` + "`" + `]+`)},
	{"command", regexp.MustCompile(`(?im)^\s*(?:\$\s+|#!|(?:sudo|curl|wget|bash|sh|zsh|rm|chmod|chown|ssh|scp|eval|exec|nc|powershell|iex)\b).*$`)},
	{"command", regexp.MustCompile("`[^`\n]*\\b(?:sudo|curl|wget|bash|rm -|chmod|chown|ssh|eval|nc)\\b[^`\n]*`")},
	{"allowed_tools", regexp.MustCompile(`(?i)allowed[-_ ]?tools\s*:.*`)},
	{"permission", regexp.MustCompile(`(?i)\b(?:sudo|root|admin(?:istrator)?|permissions?|privileges?|grant(?:ed|s)?|bypass(?:es|ed)?|dangerously[-\w]*|no-verify|skip[- ]?(?:checks?|permissions?|review))\b`)},
}

// riskyAddition is the kind of the first thing the draft adds that allowed does not carry ("" when none).
func riskyAddition(draft, allowed string, a, base *SkillAnalysis) string {
	known := strings.ToLower(allowed)
	for _, p := range riskyDraftPatterns {
		for _, m := range p.re.FindAllString(draft, -1) {
			if !strings.Contains(known, strings.ToLower(strings.Trim(m, " \t`"))) {
				return p.kind
			}
		}
	}
	if a != nil && widerAnalysis(*a, base, known) {
		return "analysis"
	}
	return ""
}

// widerAnalysis: the draft's analysis claims a capability the base analysis does not have and the
// lessons do not name.
func widerAnalysis(a SkillAnalysis, base *SkillAnalysis, known string) bool {
	var b SkillAnalysis
	if base != nil {
		b = *base
	}
	if a.Writes && !b.Writes || a.ExternalTransfer && !b.ExternalTransfer {
		return true
	}
	for i, list := range [][]string{a.Tools, a.APIs, a.Permissions, a.IrreversibleActions, a.Dependencies, a.SecretNames} {
		was := [][]string{b.Tools, b.APIs, b.Permissions, b.IrreversibleActions, b.Dependencies, b.SecretNames}[i]
		for _, item := range list {
			if !slices.Contains(was, item) && !strings.Contains(known, strings.ToLower(item)) {
				return true
			}
		}
	}
	return false
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
