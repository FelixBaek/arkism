package crew

import "testing"

func TestAttributionKeepsHostObservationLeaves(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		t.Run(name, func(t *testing.T) { attributionObservationLeaves(t, enabled) })
	}
}

func attributionObservationLeaves(t *testing.T, enabled bool) {
	t.Helper()
	patch(t, &attributionOn, func() bool { return enabled })
	spec, transport, session := hostObservationSpec(t)
	spec["access"] = "read"
	out, calls, bodies := routeOnce(t, spec, nil)
	closeHostObservation(session)
	read, err := transport.Read()
	if err != nil || len(read.Records) != 2 || len(calls) != 2 || out["ok"] != true {
		t.Fatalf("routing observation lost or duplicated: records=%d calls=%d err=%v", len(read.Records), len(calls), err)
	}
	want := 0
	if enabled {
		want = 1
	}
	if len(bodies) != want {
		t.Fatalf("attribution switch changed: got=%d want=%d", len(bodies), want)
	}
	for _, record := range read.Records {
		if record.Identity.CanonicalAttemptID != nil || record.Identity.AttemptRef != nil {
			t.Fatal("outer reservation copied into a routed leaf")
		}
	}
}
