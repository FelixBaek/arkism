package capture

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionEvidenceRepairIsBoundedAndReauthorized(t *testing.T) {
	for _, mode := range []string{"recover", "still-invalid", "authorization-revoked", "transport-error"} {
		t.Run(mode, func(t *testing.T) {
			request, _ := sessionFixture(t)
			capFake(t)
			fakeUsage(t, nil, errors.New("no reading"))
			t.Setenv("ARKISM_HAIKU_QUALITY_GATE", filepath.Join(t.TempDir(), "absent.json"))
			calls, authorizations := 0, 0
			originalContent := ""
			llm = func(system, content, model string, _ time.Duration, _, key string) (any, error) {
				calls++
				if model != sessionModel || key != "" {
					t.Fatal("unexpected model or credential", model)
				}
				if calls == 1 {
					originalContent = content
					if strings.Contains(system, sessionEvidenceRepair) {
						t.Fatal("repair instructions on first generation")
					}
				} else if calls != 2 || content != originalContent || !strings.HasSuffix(system, sessionEvidenceRepair) {
					t.Fatal("repair changed original evidence or exceeded its limit")
				}
				if mode == "transport-error" {
					return nil, sessionWait("model_timeout")
				}
				_, output := sessionFixture(t)
				if calls == 1 || mode == "still-invalid" {
					output["session_state"].(map[string]any)["source_refs"] = []any{map[string]any{"source_id": "", "revision": int64(1), "quote": ""}}
				}
				return output, nil
			}
			authorize := func(string) error {
				authorizations++
				if mode == "authorization-revoked" && authorizations == 2 {
					return sessionWait("external_transfer_denied")
				}
				return nil
			}
			out, err := sessionPrepare(map[string]any{}, "s", request["records"].([]any), 0, 1, 0, nil, nil, "", authorize)
			switch mode {
			case "recover":
				if err != nil || calls != 2 || authorizations != 2 || out["metadata"].(map[string]any)["quality_repair_reason"] != "evidence_mismatch" {
					t.Fatal(calls, authorizations, out, err)
				}
			case "still-invalid":
				if err == nil || calls != 2 || authorizations != 2 || out != nil {
					t.Fatal("invalid repair accepted or retried", calls, authorizations, err)
				}
			case "authorization-revoked":
				if errorCode(err) != "external_transfer_denied" || calls != 1 || authorizations != 2 {
					t.Fatal("repair bypassed transfer authorization", calls, authorizations, err)
				}
			case "transport-error":
				if errorCode(err) != "model_timeout" || calls != 1 {
					t.Fatal("transport failure caused a quality retry", calls, err)
				}
			}
		})
	}
}
