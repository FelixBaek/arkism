package capture

import "regexp"

var sessionDiagnosticCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,95}$`)

// Diagnostics are counters and local window positions, not a semantic grade.
// In particular a literal citation check is never labelled 100% complete.
func sessionValidationDiagnostic(reason string, start, end int64, candidates int) map[string]any {
	if reason != "" && !sessionDiagnosticCode.MatchString(reason) {
		reason = "validation_unavailable"
	}
	status := "validated"
	if reason != "" {
		status = "withheld"
	}
	return map[string]any{"version": 1, "prompt_version": sessionPromptVersion, "status": status, "reason": reason,
		"window_start": start, "window_end": end, "candidate_count": candidates, "citation_check": "literal_source_support", "semantic_completeness": "not_measured"}
}
