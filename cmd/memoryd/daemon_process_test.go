package main

import (
	"strings"
	"testing"
	"time"
)

// Every refused configuration exits 1 before memoryd touches a database.
func TestDaemonRefusesInvalidConfigurationBeforeConnecting(t *testing.T) {
	unreachable := "postgres://arkism@127.0.0.1:1/arkism_none?sslmode=disable&connect_timeout=2"
	base := func(kv ...string) map[string]string {
		env := map[string]string{"MEMORY_DATABASE_URL": unreachable}
		for i := 0; i+1 < len(kv); i += 2 {
			env[kv[i]] = kv[i+1]
		}
		return env
	}
	cases := []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"log level", base(), []string{"-log-level", "loud"}},
		{"log level from environment", base("ARKISM_LOG_LEVEL", "loud"), nil},
		{"receipt causes without snapshot", base(), []string{"-receipt-causes"}},
		{"storage identity without write gate", base(), []string{"-expected-storage-identity", "s"}},
		{"generation without write gate", base(), []string{"-expected-write-generation", "1"}},
		{"write gate pause without generation", base(), []string{"-recovery-write-gate", "pause", "-expected-storage-identity", "s", "-log-level", "info"}},
		{"write gate status with generation", base(), []string{"-recovery-write-gate", "status", "-expected-storage-identity", "s", "-expected-write-generation", "1"}},
		{"write gate status with extra flag", base(), []string{"-recovery-write-gate", "status", "-expected-storage-identity", "s", "-log-level", "info"}},
		{"write gate without identity", base(), []string{"-recovery-write-gate", "status", "-log-level", "info"}},
		{"snapshot with extra flag", base(), []string{"-export-recovery-snapshot", "-log-level", "info"}},
		{"two modes", base(), []string{"-init-db", "-check-indexes"}},
		{"standalone value", base("MEMORY_LOCAL_STANDALONE", "yes"), nil},
		{"standalone public listener", base("MEMORY_LOCAL_STANDALONE", "true"), []string{"-listen", "0.0.0.0:8091"}},
		{"recovery with init", base(), []string{"-recovery", "-init-db"}},
		{"no database", map[string]string{}, nil},
		{"retired store", base("MEMORY_STORE", "atlas"), nil},
		{"synthetic value", base("MEMORY_SYNTHETIC_HANDOFF", "yes"), nil},
		{"synthetic public listener", base("MEMORY_SYNTHETIC_HANDOFF", "1"), []string{"-listen", "0.0.0.0:8091"}},
		{"synthetic without embedder", base("MEMORY_SYNTHETIC_HANDOFF", "1"), nil},
		{"synthetic remote database", base("MEMORY_SYNTHETIC_HANDOFF", "1", "MEMORY_EMBEDDER", "none", "MEMORY_DATABASE_URL", "postgres://db.example/arkism"), nil},
		{"synthetic unparsable database", base("MEMORY_SYNTHETIC_HANDOFF", "1", "MEMORY_EMBEDDER", "none", "MEMORY_DATABASE_URL", "postgres://[bad"), nil},
		{"synthetic remote fallback", base("MEMORY_SYNTHETIC_HANDOFF", "1", "MEMORY_EMBEDDER", "none", "MEMORY_DATABASE_URL", "host=127.0.0.1,db.example dbname=arkism"), nil},
		{"synthetic with provider key", base("MEMORY_SYNTHETIC_HANDOFF", "1", "MEMORY_EMBEDDER", "none", "SKILL_LLM_API_KEY", "k"), nil},
		{"pprof without token", base(), []string{"-pprof", "127.0.0.1:0"}},
		{"unreachable database", base(), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, code := runDaemon(t, c.env, c.args...); code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
		})
	}
}

// A synthetic handoff passes every gate with only loopback services and no
// provider secrets; it then stops at the unreachable database.
func TestDaemonSyntheticHandoffAcceptsLoopbackOnly(t *testing.T) {
	env := map[string]string{"MEMORY_SYNTHETIC_HANDOFF": "1", "MEMORY_EMBEDDER": "none",
		"MEMORY_DATABASE_URL": "host=127.0.0.1,localhost port=1 dbname=arkism_none connect_timeout=2"}
	d := newDaemon(env)
	err := d.cmd.Run()
	if exitCode(err) != 1 || !strings.Contains(d.stderr.String(), "memoryd stopped") {
		t.Fatal("synthetic run", exitCode(err), d.stderr.String())
	}
}

func TestDaemonMaintenanceCommands(t *testing.T) {
	db := newTestDB(t, true)
	if _, code := runDaemon(t, db.env(), "-init-db"); code != 0 {
		t.Fatal("init-db", code)
	}
	if _, code := runDaemon(t, db.env(), "-check-indexes"); code != 0 {
		t.Fatal("check-indexes", code)
	}
	if _, code := runDaemon(t, db.env(), "-semantic-index", "create"); code != 1 {
		t.Fatal("semantic index without opt-in", code)
	}
	if _, code := runDaemon(t, db.env("MEMORY_SEMANTIC_RETRIEVAL", "1"), "-semantic-index", "rebuild"); code != 1 {
		t.Fatal("unknown semantic index action", code)
	}
	for _, action := range []string{"create", "drop"} {
		if _, code := runDaemon(t, db.env("MEMORY_SEMANTIC_RETRIEVAL", "1"), "-semantic-index", action); code != 0 {
			t.Fatal("semantic index", action, code)
		}
	}
	if _, code := runDaemon(t, db.env(), "-memory-reach", "sideways"); code != 1 {
		t.Fatal("unknown memory reach", code)
	}
	for _, direction := range []string{"apply", "revert"} {
		out, code := runDaemon(t, db.env(), "-memory-reach", direction)
		if code != 0 || !strings.Contains(out, `"direction":"`+direction+`"`) {
			t.Fatal("memory reach", direction, code, out)
		}
	}
	if _, code := runDaemon(t, db.env(), "-privacy-backup-expiry", "yesterday"); code != 1 {
		t.Fatal("unparsable backup expiry", code)
	}
	out, code := runDaemon(t, db.env(), "-privacy-backup-expiry", time.Now().UTC().Format(time.RFC3339))
	if code != 0 || !strings.Contains(out, `"advanced":0`) {
		t.Fatal("backup expiry", code, out)
	}
	if _, code := runDaemon(t, db.env(), "-reindex"); code != 1 {
		t.Fatal("reindex without an embedder", code)
	}
	tei := db.env("MEMORY_EMBEDDER", "tei", "TEI_ENDPOINT_URL", "http://127.0.0.1:1/embed", "TEI_MODEL", "m", "TEI_REVISION", "r",
		"TEI_QUERY_PREFIX", "", "TEI_DOCUMENT_PREFIX", "")
	if out, code = runDaemon(t, tei, "-reindex"); code != 0 || !strings.Contains(out, `"queued":0`) {
		t.Fatal("reindex", code, out)
	}
	if _, code = runDaemon(t, db.env("MEMORY_EMBEDDER", "unknown"), "-reindex"); code != 1 {
		t.Fatal("unknown embedder", code)
	}
}
