package dynamicsv2

// ResetRevision prepares an explicit pending state for a newer content revision.
// It neither invents activation coordinates nor changes receipt/catalog history.
func ResetRevision(previous StateV2, target RefV2, cfg DynamicsConfigV2) (StateV2, error) {
	if err := ValidateTyped("StateV2", previous); err != nil {
		return StateV2{}, err
	}
	if err := ValidateTyped("RefV2", target); err != nil {
		return StateV2{}, err
	}
	if err := ValidateTyped("DynamicsConfigV2", cfg); err != nil {
		return StateV2{}, err
	}
	if previous.Key.ConfigSHA256 != cfg.ConfigSHA256 {
		return StateV2{}, contractError("config_mismatch", "$.key.config_sha256")
	}
	if err := scalarPendingConfig(previous, cfg); err != nil {
		return StateV2{}, err
	}
	if target == previous.Key.Target {
		return scalarClone(previous), nil
	}
	if !sameMemoryIdentity(previous.Key.Target, target) {
		return StateV2{}, contractError("target_mismatch", "$.target")
	}
	if target.Revision < previous.Key.Target.Revision {
		return StateV2{}, contractError("revision_regression", "$.target")
	}
	if target.Revision == previous.Key.Target.Revision {
		return StateV2{}, contractError("revision_mismatch", "$.target")
	}
	next := scalarClone(previous)
	next.Key.Target = target
	next.Value = cfg.Initial
	next.StateRevision = 0
	next.Status = "pending_activation"
	next.Reason = "none"
	next.ActivatedAtNS = nil
	next.ActivationAcceptedSeq = nil
	next.ActivationEventID = nil
	next.ActivationPayloadSHA256 = nil
	next.LastAppliedAtNS = nil
	next.LastAppliedSeq = nil
	return sealScalarState(next)
}
func sameMemoryIdentity(a, b RefV2) bool {
	return a.Origin == b.Origin && a.Collection == b.Collection && a.ID == b.ID
}
