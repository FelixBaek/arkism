package main

import (
	"testing"
)

func TestDaemonModesAreExplicitAndMutuallyExclusive(t *testing.T) {
	modes := []struct {
		name string
		cfg  daemonOptions
	}{
		{"HTTP", daemonOptions{}}, {"Worker", daemonOptions{Worker: true}}, {"InitDB", daemonOptions{InitDB: true}},
		{"WriteGate", daemonOptions{WriteGate: "status"}}, {"ExportSnapshot", daemonOptions{ExportSnapshot: true}}, {"InspectRestore", daemonOptions{InspectRestore: true}},
		{"RecallBaseline", daemonOptions{RecallBaseline: "self"}}, {"TrainReview", daemonOptions{TrainReview: "model.json"}}, {"SemanticIndex", daemonOptions{SemanticIndex: "create"}}, {"CheckIndexes", daemonOptions{CheckIndexes: true}},
		{"MemoryReach", daemonOptions{MemoryReach: "apply"}}, {"BackupExpiry", daemonOptions{BackupExpiry: "2026-10-21T00:00:00Z"}}, {"AuditExpiry", daemonOptions{AuditExpiry: "preview"}},
		{"CollectBlobs", daemonOptions{CollectBlobs: true}}, {"AdoptBlobs", daemonOptions{AdoptBlobs: true}}, {"Reindex", daemonOptions{Reindex: true}}, {"DynamicsRepair", daemonOptions{DynamicsRepair: true}},
	}
	for _, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			mode, e := daemonMode(tc.cfg)
			if e != nil || mode != tc.name {
				t.Fatal(mode, e)
			}
			cfg := tc.cfg
			cfg.Recovery = true
			_, e = daemonMode(cfg)
			if (e == nil) != (tc.name == "HTTP" || tc.name == "Worker") {
				t.Fatal("recovery mode", e)
			}
			if tc.name != "HTTP" && tc.name != "Worker" {
				cfg = tc.cfg
				cfg.Worker = true
				if _, e = daemonMode(cfg); e == nil {
					t.Fatal("ambiguous worker mode accepted")
				}
			}
		})
	}
	if _, e := daemonMode(daemonOptions{ExportSnapshot: true, InspectRestore: true}); e == nil {
		t.Fatal("ambiguous snapshot modes accepted")
	}
}
func TestDaemonLocalAndRecoveryLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8091", "[::1]:8091"} {
		if e := validateDaemonRuntimeOptions(daemonOptions{Listen: address}, "true"); e != nil {
			t.Fatal(address, e)
		}
	}
	for _, address := range []string{"0.0.0.0:8091", ":8091", "remote:8091", "127.0.0.1"} {
		if validateDaemonRuntimeOptions(daemonOptions{Listen: address, Recovery: true}, "") == nil {
			t.Fatal(address)
		}
	}
	if validateDaemonRuntimeOptions(daemonOptions{Listen: "127.0.0.1:8091"}, "yes") == nil {
		t.Fatal("ambiguous standalone flag")
	}
}
