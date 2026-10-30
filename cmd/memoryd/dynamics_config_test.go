package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/memory"
)

// Explicit isolated fixture budget, never a daemon/product default.
func daemonDynamicsFixture(t *testing.T) ([]byte, memory.DynamicsLoadConfig) {
	t.Helper()
	load := []byte(`{"protocol":"sys05-load-config-v1","run_id":"fixture","seed":1,"mode":"async","scenario":"normal","cache":"cold","start_ns":1000,"duration_ns":1000000000,"arrival_schedule":[],"fault_schedule":[],"queue_admission_limit":4,"batch_limit":2,"concurrency_limit":1,"retry_limit":2,"deadline_ns":5000000000,"allowed_freshness_ns":1000000000,"quality_criterion_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","model_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","corpus_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`)
	c := memory.DynamicsLoadConfig{Protocol: "arkism-sys05-load-v1", Profile: "root-approved-isolated-fixture-draft-v1", Mode: "async", LoadContractSHA256: "398df795b2eb03fc421158a10ca4094cdb199f3577545f28715b3d2d8c9ea2bc", LoadConfigSHA256: memory.ContentHash(string(load)), Scopes: []memory.Scope{{WorkspaceID: "fixture", ProjectID: "project"}}}
	c.Limits = memory.DynamicsLoadLimits{QueueCountLimit: 4, QueueActiveBytesLimit: 65536, MaxWaitNS: 5000000000, BatchLimit: 2, ConcurrencyLimit: 1, RetryLimit: 2, RetryBackoffNS: 1000000, DeadlineNS: 5000000000, WorkerSubstepTimeoutNS: 1000000000, LeaseNS: 10000000000, FinishTimeoutNS: 2000000000, PollIntervalNS: 5000000, PublishIntervalNS: 5000000, AllowedFreshnessNS: 1000000000, SnapshotMaxItems: 16, SnapshotMaxBytes: 65536, MaxConfiguredScopes: 2, TelemetryMaxBytes: 262144, TerminalLedgerRetentionBudget: 1048576}
	return load, c
}

func daemonDynamicsFiles(t *testing.T, load []byte, c memory.DynamicsLoadConfig) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runtimePath, loadPath := filepath.Join(dir, "运行-설정.json"), filepath.Join(dir, "부하-é.json")
	raw, err := json.Marshal(c)
	if err != nil || os.WriteFile(runtimePath, raw, 0600) != nil || os.WriteFile(loadPath, load, 0600) != nil {
		t.Fatal("fixture files", err)
	}
	return runtimePath, loadPath
}

func TestDaemonDynamicsConfigPairAndImmutablePlan(t *testing.T) {
	if p, err := loadDaemonDynamicsPlan("", ""); p != nil || err != nil {
		t.Fatal("absent must remain off", err)
	}
	for _, pair := range [][2]string{{"only-runtime", ""}, {"", "only-load"}} {
		if _, err := loadDaemonDynamicsPlan(pair[0], pair[1]); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal("partial config accepted", err)
		}
	}
	load, config := daemonDynamicsFixture(t)
	runtimePath, loadPath := daemonDynamicsFiles(t, load, config)
	p, err := loadDaemonDynamicsPlan(runtimePath, loadPath)
	if err != nil || p == nil || p.config.Mode != "async" {
		t.Fatal("valid pinned pair refused", err)
	}
	before := bytes.Clone(p.raw)
	if os.WriteFile(runtimePath, []byte("null"), 0600) != nil || os.WriteFile(loadPath, []byte("null"), 0600) != nil {
		t.Fatal("fixture replacement")
	}
	if !bytes.Equal(before, p.raw) {
		t.Fatal("loaded plan changed after file replacement")
	}
	if _, err := memory.NewDynamicsLoadRuntime(p.raw); err != nil {
		t.Fatal("frozen plan cannot construct runtime", err)
	}
}

func TestDaemonDynamicsConfigBindings(t *testing.T) {
	load, c := daemonDynamicsFixture(t)
	cases := map[string][]byte{
		"mode":          bytes.Replace(load, []byte(`"mode":"async"`), []byte(`"mode":"sync"`), 1),
		"count":         bytes.Replace(load, []byte(`"queue_admission_limit":4`), []byte(`"queue_admission_limit":5`), 1),
		"fraction":      bytes.Replace(load, []byte(`"batch_limit":2`), []byte(`"batch_limit":2.0`), 1),
		"exponent":      bytes.Replace(load, []byte(`"batch_limit":2`), []byte(`"batch_limit":2e0`), 1),
		"null":          bytes.Replace(load, []byte(`"retry_limit":2`), []byte(`"retry_limit":null`), 1),
		"duplicate":     bytes.Replace(load, []byte(`"mode":"async"`), []byte(`"mode":"async","mode":"async"`), 1),
		"alias":         bytes.Replace(load, []byte(`"mode":"async"`), []byte(`"mode":"async","Mode":"async"`), 1),
		"escaped-alias": bytes.Replace(load, []byte(`"mode":"async"`), []byte(`"mode":"async","\u006dode":"async"`), 1),
		"unknown":       bytes.Replace(load, []byte(`"seed":1`), []byte(`"seed":1,"unused":1`), 1),
		"missing":       bytes.Replace(load, []byte(`"seed":1,`), nil, 1),
		"trailing":      append(bytes.Clone(load), []byte(`{}`)...),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := c
			cfg.LoadConfigSHA256 = memory.ContentHash(string(raw)) // Test validation, not merely hash mismatch.
			r, l := daemonDynamicsFiles(t, raw, cfg)
			if _, err := loadDaemonDynamicsPlan(r, l); !errors.Is(err, memory.ErrInvalid) {
				t.Fatal("invalid binding accepted", err)
			}
		})
	}
	r, l := daemonDynamicsFiles(t, load, c)
	if err := os.WriteFile(l, append(load, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDaemonDynamicsPlan(r, l); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("raw hash mismatch accepted", err)
	}
}

func TestDaemonDynamicsConfigFileBoundaries(t *testing.T) {
	load, c := daemonDynamicsFixture(t)
	r, l := daemonDynamicsFiles(t, load, c)
	alias := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(r, alias); err != nil {
		t.Fatal(err)
	}
	parentAlias := filepath.Join(t.TempDir(), "parent")
	if err := os.Symlink(filepath.Dir(r), parentAlias); err != nil {
		t.Fatal(err)
	}
	paths := []string{alias, filepath.Join(parentAlias, filepath.Base(r)), r + string(os.PathSeparator) + "missing", filepath.Dir(r), filepath.Dir(r) + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(filepath.Dir(r)) + string(os.PathSeparator) + filepath.Base(r), "invalid\x00name"}
	for _, path := range paths {
		if _, err := loadDaemonDynamicsPlan(path, l); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal("unsafe path accepted", err)
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "oversize")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate((64 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err = loadDaemonDynamicsPlan(f.Name(), l); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("oversize accepted", err)
	}
}

func TestDaemonDynamicsJSONDepthAndCanonicalKeys(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"a":{"Mode":1}}`, `{"a":{"key":1,"key":2}}`, strings.Repeat(`{"a":`, 34) + `0` + strings.Repeat(`}`, 34)} {
		if _, err := daemonDynamicsJSONObject([]byte(raw)); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal("noncanonical input accepted", err)
		}
	}
	p, err := daemonDynamicsJSONObject([]byte(`{"profile":"Mixed-Case-Value"}`))
	if err != nil || p["profile"] != "Mixed-Case-Value" {
		t.Fatal("value changed", err)
	}
}
