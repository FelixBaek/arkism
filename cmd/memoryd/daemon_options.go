package main

import (
	"flag"
	"net"
	"os"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/memory"
)

type daemonOptions struct {
	DynamicsRuntimeConfig string
	DynamicsLoadConfig    string
	dynamicsPlan          *daemonDynamicsPlan
	dynamicsReaderToken   string
	DynamicsSearchConfig  string
	dynamicsSearch        *memory.NativeSearchShadowConfig
	DynamicsRepair        bool
	ReceiptCauses         bool
	WriteGate             string
	ExpectedStorage       string
	ExpectedGeneration    string
	InspectRestore        bool
	ExportSnapshot        bool
	AdoptBlobs            bool
	BackupExpiry          string
	AuditExpiry           string
	CollectBlobs          bool
	InitDB                bool
	SemanticIndex         string
	CheckIndexes          bool
	Recovery              bool
	Worker                bool
	MemoryReach           string
	Reindex               bool
	TrainReview           string
	TrainReviewExclude    string
	RecallBaseline        string
	BaselineScope         string
	LogLevel              string
	Pprof                 string
	Listen                string
}

func daemonFlags() daemonOptions {
	var cfg daemonOptions
	flag.BoolVar(&cfg.ReceiptCauses, "receipt-causes", false, "include receipt origin evidence in offline v5/v6 recovery snapshots")
	flag.StringVar(&cfg.WriteGate, "recovery-write-gate", "", "offline administrator: status|pause|resume canonical database writes")
	flag.StringVar(&cfg.ExpectedStorage, "expected-storage-identity", "", "independently verified storage identity for write gate administration")
	flag.StringVar(&cfg.ExpectedGeneration, "expected-write-generation", "", "current gate generation required for pause/resume, or paused generation required for snapshot export")
	flag.BoolVar(&cfg.InspectRestore, "inspect-restored-recovery", false, "offline administrator: inspect all-scope requests in a publication-blocked restore")
	flag.BoolVar(&cfg.ExportSnapshot, "export-recovery-snapshot", false, "offline administrator: export all-scope recovery ledger from one read-only database snapshot")
	flag.BoolVar(&cfg.AdoptBlobs, "adopt-restored-blobs", false, "verify quarantined restored files and bind to the restored DB; stopped writers required")
	flag.StringVar(&cfg.BackupExpiry, "privacy-backup-expiry", "", "RFC3339 time of the oldest retained backup; finish privacy deletions parked before it, then exit")
	flag.StringVar(&cfg.AuditExpiry, "audit-expiry", "", "schema owner: preview|execute deleting audit events older than MEMORY_AUDIT_RETENTION_DAYS (default 30), then exit")
	flag.BoolVar(&cfg.CollectBlobs, "collect-blobs", false, "collect unreferenced blob files and abandoned temporary files; service maintenance authorization required")
	flag.BoolVar(&cfg.InitDB, "init-db", false, "create Memory collections and ordinary indexes")
	flag.StringVar(&cfg.SemanticIndex, "semantic-index", "", "create or drop the opt-in pgvector knowledge index: create|drop")
	flag.BoolVar(&cfg.CheckIndexes, "check-indexes", false, "verify the Postgres schema, search indexes and RLS are ready")
	flag.BoolVar(&cfg.Recovery, "recovery", false, "isolated deletion recovery only; loopback API or erasure-only worker")
	flag.BoolVar(&cfg.Worker, "worker", false, "run embedding worker instead of HTTP API")
	flag.StringVar(&cfg.MemoryReach, "memory-reach", "", "offline administrator: apply|revert the default memory reach (user=personal, rest=general), then exit")
	flag.BoolVar(&cfg.Reindex, "reindex", false, "queue embedding jobs for Postgres records not in the configured embedder's space, then exit")
	flag.StringVar(&cfg.TrainReview, "train-review-classifier", "", "offline: train a scope-bound review advice model and write it to this path")
	flag.StringVar(&cfg.TrainReviewExclude, "train-review-exclude", "arkism-memory-reviewer", "comma-separated reviewer principals excluded from training")
	flag.StringVar(&cfg.RecallBaseline, "recall-baseline", "", "read-only measurement: evaluation JSONL path, or self for the known-item set; prints numbers only")
	flag.StringVar(&cfg.BaselineScope, "recall-baseline-scope", "default/default/management:owner/", "workspace/project/user/agent measured by -recall-baseline")
	flag.StringVar(&cfg.LogLevel, "log-level", envLogLevel(), "debug|info|warn|error")
	flag.StringVar(&cfg.Pprof, "pprof", "", "optional loopback diagnostics listener; MEMORY_DIAGNOSTICS_TOKEN required")
	flag.StringVar(&cfg.Listen, "listen", host.DefaultListen, "HTTP listen address")
	flag.StringVar(&cfg.DynamicsRuntimeConfig, "dynamics-runtime-config", "", "explicit bounded dynamics runtime config; requires paired load config")
	flag.StringVar(&cfg.DynamicsLoadConfig, "dynamics-load-config", "", "exact offline load config bound by the dynamics runtime config")
	flag.StringVar(&cfg.DynamicsSearchConfig, "dynamics-search-config", "", "explicit HTTP API search mode config (off|shadow|on); requires both dynamics configs; on requires fast_slow and is development wiring")
	flag.BoolVar(&cfg.DynamicsRepair, "dynamics-repair", false, "verify and release a withheld dynamics publication in every configured scope, then exit; requires both dynamics configs")
	flag.Parse()
	return cfg
}

func validateDaemonOptions(cfg daemonOptions) (string, error) {
	standalone := os.Getenv("MEMORY_LOCAL_STANDALONE")
	checks := []func() error{func() error { return validateDaemonRecoveryFlags(cfg) }, func() error { return validateDaemonSnapshotFlags(cfg) }, func() error { return validateDaemonRuntimeOptions(cfg, standalone) }}
	for _, check := range checks {
		if err := check(); err != nil {
			return "", err
		}
	}
	if _, err := daemonMode(cfg); err != nil {
		return "", err
	}
	return standalone, nil
}
func validateDaemonRecoveryFlags(cfg daemonOptions) error {
	if cfg.ReceiptCauses && !cfg.ExportSnapshot && !cfg.InspectRestore {
		return memory.ErrInvalid
	}
	if cfg.WriteGate == "" {
		if cfg.ExpectedStorage != "" || cfg.ExpectedGeneration != "" && !cfg.ExportSnapshot && !cfg.InspectRestore {
			return memory.ErrInvalid
		}
		return nil
	}
	count := 3
	if cfg.WriteGate == "status" {
		count = 2
	}
	if flag.NFlag() != count || flag.NArg() != 0 || cfg.ExpectedStorage == "" {
		return memory.ErrInvalid
	}
	if cfg.WriteGate == "status" && cfg.ExpectedGeneration != "" || cfg.WriteGate != "status" && cfg.ExpectedGeneration == "" {
		return memory.ErrInvalid
	}
	return nil
}
func validateDaemonSnapshotFlags(cfg daemonOptions) error {
	if !cfg.ExportSnapshot && !cfg.InspectRestore {
		return nil
	}
	count := 1
	if cfg.ReceiptCauses {
		count++
	}
	if cfg.ExpectedGeneration != "" {
		count++
	}
	if flag.NFlag() != count || flag.NArg() != 0 {
		return memory.ErrInvalid
	}
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return memory.ErrInvalid
	}
	return nil
}
func daemonLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}
func validateDaemonRuntimeOptions(cfg daemonOptions, standalone string) error {
	if standalone != "" && standalone != "true" {
		return memory.ErrInvalid
	}
	if standalone == "true" && !cfg.Worker && !daemonLoopback(cfg.Listen) {
		return memory.ErrInvalid
	}
	if cfg.Recovery && (!daemonLoopback(cfg.Listen) || cfg.InitDB || cfg.CheckIndexes) {
		return memory.ErrInvalid
	}
	return validateDaemonDynamicsOptions(cfg)
}
func (cfg daemonOptions) usesWorkerToken() bool {
	return cfg.TrainReview != "" || cfg.Worker || cfg.CollectBlobs || cfg.AdoptBlobs || cfg.Reindex || cfg.BackupExpiry != "" || cfg.MemoryReach != "" || cfg.DynamicsRepair
}
func snapshotDaemonMode(mode string) bool {
	return mode == "WriteGate" || mode == "ExportSnapshot" || mode == "InspectRestore"
}
func daemonMode(cfg daemonOptions) (string, error) {
	modes := []struct {
		name string
		on   bool
	}{
		{"WriteGate", cfg.WriteGate != ""},
		{"ExportSnapshot", cfg.ExportSnapshot},
		{"InspectRestore", cfg.InspectRestore},
		{"RecallBaseline", cfg.RecallBaseline != ""},
		{"TrainReview", cfg.TrainReview != ""},
		{"InitDB", cfg.InitDB},
		{"SemanticIndex", cfg.SemanticIndex != ""},
		{"CheckIndexes", cfg.CheckIndexes},
		{"MemoryReach", cfg.MemoryReach != ""},
		{"BackupExpiry", cfg.BackupExpiry != ""},
		{"AuditExpiry", cfg.AuditExpiry != ""},
		{"CollectBlobs", cfg.CollectBlobs},
		{"AdoptBlobs", cfg.AdoptBlobs},
		{"Reindex", cfg.Reindex},
		{"DynamicsRepair", cfg.DynamicsRepair},
		{"Worker", cfg.Worker},
	}
	selected := "HTTP"
	for _, mode := range modes {
		if !mode.on {
			continue
		}
		if selected != "HTTP" {
			return "", memory.ErrInvalid
		}
		selected = mode.name
	}
	if cfg.Recovery && selected != "HTTP" && selected != "Worker" {
		return "", memory.ErrInvalid
	}
	return selected, nil
}
