package main

import (
	"reflect"
	"testing"
)

func TestDynamicsIntegrationDynamicHTTPPG(t *testing.T) {
	r := newDaemonRankingHTTPRig(t, false)
	off := r.search(t, 0)
	shadow := r.search(t, 1)
	dynamic := r.search(t, 2)
	if r.apis[0].NativeSearchShadow != nil || !reflect.DeepEqual(off, shadow) {
		t.Fatal("default/off or shadow altered baseline")
	}
	if off.Items[0].ID != r.other || dynamic.Items[0].ID != r.target || dynamic.ContextData == off.ContextData {
		t.Fatal("general scalar dynamic ranking did not reach HTTP packed memory")
	}
	recs := r.apis[2].NativeSearchShadow.Observations()
	if len(recs) != 1 || recs[0].Protocol != "sys-native-service-dynamic-v1" || recs[0].Mode != "dynamic" || recs[0].FastSlow || !recs[0].Served || recs[0].BaselineReturned || recs[0].SourceCount != 2 {
		t.Fatal("scalar return mislabeled", recs)
	}
	r.removeState(t)
	fallback := r.search(t, 2)
	if !reflect.DeepEqual(off, fallback) {
		t.Fatal("missing scalar source did not fall back to baseline")
	}
	recs = r.apis[2].NativeSearchShadow.Observations()
	last := recs[len(recs)-1]
	if last.Status != "fallback" || last.Served || !last.BaselineReturned || last.Mode != "dynamic" {
		t.Fatal("general fallback fabricated return", last)
	}
}

func TestDynamicsIntegrationDynamicConfigDefaultOff(t *testing.T) {
	api := daemonSearchModeAPI(t)
	got, err := prepareDaemonDynamics(api)
	if err != nil || got.dynamicsSearch != nil {
		t.Fatal("absent setting enabled native serving", err)
	}
	api.DynamicsSearchConfig = daemonSearchModeFiles(t, func(m map[string]any) { m["mode"] = "dynamic" })
	got, err = prepareDaemonDynamics(api)
	if err != nil || got.dynamicsSearch == nil || got.dynamicsSearch.Mode != "dynamic" || got.dynamicsSearch.FastSlow != nil {
		t.Fatal("explicit scalar dynamic mode not carried", err)
	}
}
