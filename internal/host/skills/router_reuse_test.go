package skills

import (
	"testing"
	"time"
)

// GET is an authorized history/review endpoint. Current-context MCP recall must
// not republish a row just because an earlier session retained its ID.
func TestRecallByIDWithholdsUnavailableMemory(t *testing.T) {
	for _, tc := range []struct{ name, fields string }{
		{"expired_status", `"status":"expired","approved_by":"human"`},
		{"rejected", `"status":"rejected","approved_by":"human"`},
		{"conflicted", `"status":"conflicted","approved_by":"human"`},
		{"superseded", `"status":"superseded","approved_by":"human"`},
		{"unapproved", `"status":"active"`},
		{"invalid_approver", `"status":"active","approved_by":123`},
		{"quarantined", `"status":"active","approved_by":"human","privacy_quarantined":true`},
		{"expired_clock", `"status":"active","approved_by":"human","expires_at":"2000-01-01T00:00:00Z"`},
		{"future", `"status":"active","approved_by":"human","effective_at":"2999-01-01T00:00:00Z"`},
		{"malformed_expiry", `"status":"active","approved_by":"human","expires_at":"invalid"`},
		{"erased", `"status":"active","approved_by":"human","content_erased":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			swapRouter(t)
			fakeRequest(t, `{"id":"old","text":"retired synthetic instruction",`+tc.fields+`}`)
			out, err := recall("", []any{"old"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(asList(field(out, "memories"))) != 0 {
				t.Fatalf("unavailable memory entered current context: %v", out)
			}
		})
	}
}

func TestRecallByIDAllowsApprovedCurrentMemory(t *testing.T) {
	swapRouter(t)
	fakeRequest(t, `{"id":"current","text":"current synthetic rule","status":"active","approved_by":"human","expires_at":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)+`"}`)
	out, err := recall("", []any{"current"}, nil)
	if err != nil || len(asList(field(out, "memories"))) != 1 {
		t.Fatalf("current approved memory lost: %v %v", out, err)
	}
}
