package memory

// Missing legacy source classification must never promote new proposals.
func sourceTrustLevel(level string) string {
	if level == "" {
		return "external_untrusted"
	}
	return level
}

func candidateTrustLevel(r Record, sources []Record) string {
	for _, source := range sources {
		if source.TrustLevel != "user_authored" {
			return "external_untrusted"
		}
	}
	if r.CreatedByType != "human" {
		return "agent_proposed"
	}
	if r.Generator != nil {
		return "model_inferred"
	}
	return "user_authored"
}
