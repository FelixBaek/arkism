package dynamicsv2

import (
	"encoding/json"
	"testing"
)

func arrivalActivation(t *testing.T) EventV2 {
	t.Helper()
	return sealFixture(t, "EventV2", EventV2{
		Protocol: "dynamics-event-v2", StreamID: "s", EventID: "activate",
		TransportAttemptID: "delivery", Key: fixtureKey(), Kind: "activate",
		ClockDomain: "c", ObservedAtNS: ptr(int64(9)), ArrivedAtNS: ptr(int64(10)),
		AcceptedAtNS: 10, AcceptedSeq: 1, Outcome: "not_applicable",
		Attribution: "not_applicable", Reason: "activation",
	})
}

func arrivalEventFile(events ...EventV2) EventFileV2 {
	return EventFileV2{Protocol: "dynamics-event-file-v2", StreamID: "s",
		ClockDomain: "c", ConfigSHA256: fixtureKey().ConfigSHA256, Events: events}
}

func TestDynamicsContractArrivalHistoricalTransport(t *testing.T) {
	first := arrivalActivation(t)
	retry := first
	retry.TransportAttemptID = "later_delivery"
	retry.ArrivedAtNS = ptr(int64(100))
	// The original logical hash and accepted coordinates are deliberately retained.
	for _, tc := range []struct {
		name, kind string
		value      any
	}{
		{"event", "EventV2", retry},
		{"event_file", "EventFileV2", arrivalEventFile(first, retry)},
	} {
		t.Run(tc.name+"_typed", func(t *testing.T) {
			if err := ValidateTyped(tc.kind, tc.value); err != nil {
				t.Fatalf("late delivery of unchanged accepted event: %v", err)
			}
		})
		t.Run(tc.name+"_strict", func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeStrict(data, tc.kind); err != nil {
				t.Fatalf("file ingress rejected historical delivery: %v", err)
			}
		})
	}
}

func TestDynamicsContractArrivalFirstAcceptanceGuards(t *testing.T) {
	t.Run("first_delivery_after_acceptance", func(t *testing.T) {
		e := arrivalActivation(t)
		e.ArrivedAtNS = ptr(int64(11))
		file := arrivalEventFile(e)
		requireCode(t, ValidateTyped("EventFileV2", file), "future_event")
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		_, err = DecodeStrict(data, "EventFileV2")
		requireCode(t, err, "future_event")
	})
	t.Run("future_observation_remains_logical_conflict", func(t *testing.T) {
		e := arrivalActivation(t)
		e.ObservedAtNS = ptr(int64(11))
		e = sealFixture(t, "EventV2", e)
		requireCode(t, ValidateTyped("EventV2", e), "future_event")
	})
	t.Run("retry_coordinate_conflict_before_freshness", func(t *testing.T) {
		first := arrivalActivation(t)
		retry := first
		retry.TransportAttemptID = "later_delivery"
		retry.ArrivedAtNS = ptr(int64(100))
		retry.AcceptedAtNS = 11
		requireCode(t, ValidateTyped("EventFileV2", arrivalEventFile(first, retry)), "event_conflict")
	})
	t.Run("negative_transport_time_is_invalid", func(t *testing.T) {
		e := arrivalActivation(t)
		e.ArrivedAtNS = ptr(int64(-1))
		requireCode(t, ValidateTyped("EventV2", e), "invalid_value")
	})
	for _, tc := range []struct {
		name string
		at   *int64
	}{{"unknown", nil}, {"earlier", ptr(int64(9))}, {"same_time", ptr(int64(10))}} {
		t.Run(tc.name, func(t *testing.T) {
			e := arrivalActivation(t)
			e.ArrivedAtNS = tc.at
			if err := ValidateTyped("EventFileV2", arrivalEventFile(e)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
