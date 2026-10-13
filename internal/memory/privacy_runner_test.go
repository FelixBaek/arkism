package memory

import "testing"

func TestNoRuntimeReceiptIsAcceptedOnlyAsVerifiedOrHeld(t *testing.T) {
	pd := &PrivacyDeletion{RequestHash: "h", LegalHoldStatus: privacyHoldNone, Impact: PrivacyImpact{ExpectedRunIDs: []string{"run-1", "run-2"}}}
	action := PrivacyRuntimeAction{RequestHash: "h", HoldStatus: privacyHoldNone, ExpectedRunIDs: []string{"run-1", "run-2"}}
	r := noRuntimeReceipt(action)
	if !validRuntimeReceipt(pd, r) || !r.Verified || len(r.AbsentRunIDs) != 2 || len(r.MatchedRunIDs) != 0 || r.Counts != (PrivacyRuntimeCounts{}) {
		t.Fatalf("receipt %+v", r)
	}
	held := &PrivacyDeletion{RequestHash: "h", LegalHoldStatus: privacyHoldHeld, Impact: PrivacyImpact{}}
	hr := noRuntimeReceipt(PrivacyRuntimeAction{RequestHash: "h", HoldStatus: privacyHoldHeld})
	if !validRuntimeReceipt(held, hr) || hr.Verified || !hr.Held {
		t.Fatalf("held receipt %+v", hr)
	}
}
