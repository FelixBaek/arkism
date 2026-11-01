//go:build unix

package main

import "testing"

func TestNativeFinalEvidenceConsumerActualPython(t *testing.T) {
	runNativeExperimentFixture(t, "exp_04", "test_final_consumer.py")
}

func TestNativeRetryFinalEvidenceConsumerActualPython(t *testing.T) {
	runNativeExperimentFixture(t, "exp_04", "test_retry_final_consumer.py")
}

func TestNativeAttributionFinalEvidenceConsumerActualPython(t *testing.T) {
	runNativeExperimentFixture(t, "exp_04", "test_native_attribution_consumer.py")
}
