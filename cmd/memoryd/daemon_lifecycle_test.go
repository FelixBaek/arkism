package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/memory"
	"github.com/jackc/pgx/v5"
)

func TestDaemonBlobMaintenance(t *testing.T) {
	db := newTestDB(t, true)
	blobs := filepath.Join(t.TempDir(), "blobs")
	if err := os.Mkdir(blobs, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, code := runDaemon(t, db.env("MEMORY_BLOB_DIRECTORY", blobs), "-collect-blobs"); code != 0 || !json.Valid([]byte(out)) {
		t.Fatal("collect blobs", code, out)
	}
	// Adoption is for a restored database only; a live one refuses it.
	if _, code := runDaemon(t, db.env("MEMORY_BLOB_DIRECTORY", blobs), "-adopt-restored-blobs"); code != 1 {
		t.Fatal("adopt on a live database", code)
	}
	weak, _ := memory.NewPrincipalToken()
	if _, code := runDaemon(t, db.env("MEMORY_BLOB_DIRECTORY", blobs, "MEMORY_WORKER_TOKEN", weak), "-collect-blobs"); code != 1 {
		t.Fatal("collect without permission", code)
	}
}

func TestDaemonRecallBaseline(t *testing.T) {
	db := newTestDB(t, true)
	out, code := runDaemon(t, db.env(), "-recall-baseline", "self")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatal("self baseline", code, out)
	}
	queries := filepath.Join(t.TempDir(), "q.jsonl")
	if err := os.WriteFile(queries, []byte(`{"query":"anything","negative":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code = runDaemon(t, db.env("MEMORY_WORKER_TOKEN", ""), "-recall-baseline", queries, "-recall-baseline-scope", "default/default/management:owner/"); code != 0 {
		t.Fatal("file baseline", code)
	}
	for name, args := range map[string][]string{
		"too many flags": {"-recall-baseline", "self", "-recall-baseline-scope", "a/b/c/", "-log-level", "info"},
		"bad scope":      {"-recall-baseline", "self", "-recall-baseline-scope", "a/b"},
		"missing file":   {"-recall-baseline", filepath.Join(t.TempDir(), "absent.jsonl")},
		"invalid scope":  {"-recall-baseline", "self", "-recall-baseline-scope", "///"},
	} {
		if _, code := runDaemon(t, db.env(), args...); code != 1 {
			t.Fatal(name, code)
		}
	}
	if _, code = runDaemon(t, db.env("MEMORY_EMBEDDER", "unknown"), "-recall-baseline", "self"); code != 1 {
		t.Fatal("unknown embedder", code)
	}
}

func TestDaemonTrainReviewClassifier(t *testing.T) {
	db := newTestDB(t, true)
	model := filepath.Join(t.TempDir(), "model.json")
	// Never called: with no reviewed candidates there is nothing to embed.
	tei := []string{"MEMORY_EMBEDDER", "tei", "TEI_ENDPOINT_URL", "http://127.0.0.1:1/embed", "TEI_MODEL", "m", "TEI_REVISION", "r",
		"TEI_QUERY_PREFIX", "", "TEI_DOCUMENT_PREFIX", ""}
	for name, run := range map[string]struct {
		env  map[string]string
		args []string
	}{
		"bad scope":        {db.env(), []string{"-train-review-classifier", model, "-recall-baseline-scope", "a/b"}},
		"bad embedder":     {db.env("MEMORY_EMBEDDER", "unknown"), []string{"-train-review-classifier", model}},
		"no token":         {db.env("MEMORY_WORKER_TOKEN", ""), []string{"-train-review-classifier", model}},
		"bad cost rates":   {db.env("JEV_USD_PER_REQUEST", "free"), []string{"-train-review-classifier", model}},
		"no embedder":      {db.env(), []string{"-train-review-classifier", model}},
		"nothing to learn": {db.env(tei...), []string{"-train-review-classifier", model, "-train-review-exclude", "a, ,b"}},
		"no schema":        {newTestDB(t, false).env(), []string{"-train-review-classifier", model}},
	} {
		if _, code := runDaemon(t, run.env, run.args...); code != 1 {
			t.Fatal(name, code)
		}
	}
	if _, err := os.Stat(model); !os.IsNotExist(err) {
		t.Fatal("an empty model was written", err)
	}
}

func storageIdentity(t *testing.T, db testDB) string {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), db.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var identity string
	if err = conn.QueryRow(context.Background(), `SELECT encode(value,'hex') FROM arkism.memory_storage_metadata WHERE key='storage_identity'`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestDaemonRecoveryWriteGateAndSnapshots(t *testing.T) {
	db := newTestDB(t, true)
	if _, code := runDaemon(t, db.env(), "-recovery-write-gate", "status", "-expected-storage-identity", "not-this-storage"); code != 1 {
		t.Fatal("write gate with a wrong storage identity", code)
	}
	identity := storageIdentity(t, db)
	gate := func(args ...string) (generation, state string) {
		t.Helper()
		out, code := runDaemon(t, db.env(), append(args, "-expected-storage-identity", identity)...)
		var status struct {
			Gate struct{ Generation, State string } `json:"write_gate"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &status) != nil || status.Gate.Generation == "" {
			t.Fatal("write gate", args, code, out)
		}
		return status.Gate.Generation, status.Gate.State
	}
	open, _ := gate("-recovery-write-gate", "status")
	paused, state := gate("-recovery-write-gate", "pause", "-expected-write-generation", open)
	if state != "paused" {
		t.Fatal("pause", state)
	}
	for _, args := range [][]string{
		{"-export-recovery-snapshot"},
		{"-export-recovery-snapshot", "-receipt-causes"},
		{"-export-recovery-snapshot", "-expected-write-generation", paused},
	} {
		if out, code := runDaemon(t, db.env(), args...); code != 0 || !json.Valid([]byte(out)) {
			t.Fatal("snapshot", args, code)
		}
	}
	terminal := newDaemon(db.env(), "-export-recovery-snapshot")
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	terminal.cmd.Stdout = devNull // a character device, like a terminal
	if code := exitCode(terminal.cmd.Run()); code != 1 {
		t.Fatal("snapshot written to a character device", code)
	}
	// A live database is not a publication-blocked restore: inspection refuses it.
	for _, args := range [][]string{{"-inspect-restored-recovery"}, {"-inspect-restored-recovery", "-expected-write-generation", paused}} {
		if _, code := runDaemon(t, db.env(), args...); code != 1 {
			t.Fatal("inspection of a live database", args, code)
		}
	}
	if _, state = gate("-recovery-write-gate", "resume", "-expected-write-generation", paused); state != "open" {
		t.Fatal("resume", state)
	}
}

func healthy(address string) func(string) bool {
	return func(string) bool {
		resp, err := http.Get("http://" + address + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
		}
		return err == nil && resp.StatusCode == http.StatusOK
	}
}

func TestDaemonServesHTTPUntilSIGTERM(t *testing.T) {
	db := newTestDB(t, true)
	keyFile := filepath.Join(t.TempDir(), "anthropic.key")
	if err := os.WriteFile(keyFile, []byte("sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	budget := `{"scope":{"workspace_id":"default","project_id":"default"},"principals":["memoryd-test-worker"],"monthly_pico_usd":1000000000000,"general_pico_usd":1000000000000,"expires_at":"2099-01-01T00:00:00Z"}`
	address := freeAddress(t)
	env := db.env("MEMORY_VECTOR_MAX_DISTANCE", "0.6", "MEMORY_TOPIC_DIGEST_MAX", "12", "MEMORY_CANDIDATE_DAILY_LIMIT", "50",
		"MEMORY_SKILL_SCOPE", "default/default/management:owner", "MEMORY_SESSION_ARCHIVE_KEY", strings.Repeat("ab", 32),
		"MEMORY_GOOGLE_CLIENT_ID", "id", "MEMORY_GOOGLE_CLIENT_SECRET", "secret", "MEMORY_GOOGLE_ALLOWED_EMAILS", "A@example.com, ,b@example.com",
		"MEMORY_DESKTOP_TOKEN", strings.Repeat("t", 32), "MEMORY_MODEL_BUDGET_POLICY", budget,
		"ARKISM_ANTHROPIC_SKILL_DRAFTER", "1", "ARKISM_ANTHROPIC_KEY_FILE", keyFile, "ARKISM_ANTHROPIC_WORKSPACE_ID", "wrk",
		"ARKISM_UNSPLASH_KEY", "u", "SKILL_LLM_API_KEY", "k", "SKILL_LLM_ENDPOINT_URL", "http://127.0.0.1:1/v1", "ARKISM_WIKI", "on",
		"TYPESAFE_API_KEY", "j", "CLOUDFLARE_ACCOUNT_ID", "a", "CLOUDFLARE_AI_TOKEN", "c",
		"ARKISM_ROUTING_PICKER_URL", "http://127.0.0.1:1/v1", "ARKISM_ROUTING_PICKER_MODEL", "m")
	if code := serveUntilSIGTERM(t, env, healthy(address), "-listen", address); code != 0 {
		t.Fatal("HTTP server stop", code)
	}
	address = freeAddress(t)
	// Both picks are random and released, so they can collide.
	pprofAddress := freeAddress(t)
	for pprofAddress == address {
		pprofAddress = freeAddress(t)
	}
	env = db.env("TYPESAFE_API_KEY", "j", "MEMORY_UNSPLASH_ACCESS_KEY", "u", "MEMORY_DIAGNOSTICS_TOKEN", strings.Repeat("d", 32))
	if code := serveUntilSIGTERM(t, env, healthy(address), "-listen", address, "-pprof", pprofAddress); code != 0 {
		t.Fatal("HTTP server with diagnostics stop", code)
	}
	address = freeAddress(t)
	if code := serveUntilSIGTERM(t, db.env("SKILL_LLM_API_KEY", "ignored in recovery"), healthy(address), "-listen", address, "-recovery"); code != 0 {
		t.Fatal("recovery server stop", code)
	}
}

func TestDaemonRefusesInvalidServiceSettings(t *testing.T) {
	db := newTestDB(t, true)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	missing := filepath.Join(t.TempDir(), "absent")
	cases := map[string]struct {
		env  map[string]string
		args []string
	}{
		"vector distance":         {db.env("MEMORY_VECTOR_MAX_DISTANCE", "2"), nil},
		"topic digest":            {db.env("MEMORY_TOPIC_DIGEST_MAX", "0"), nil},
		"candidate limit":         {db.env("MEMORY_CANDIDATE_DAILY_LIMIT", "-1"), nil},
		"merge similarity":        {db.env("MEMORY_CAPTURE_MERGE_SIMILARITY", "1.5"), nil},
		"cost rates":              {db.env("SKILL_LLM_INPUT_USD_PER_MILLION_TOKENS", "0.5"), nil},
		"partial google login":    {db.env("MEMORY_GOOGLE_CLIENT_ID", "id"), nil},
		"review classifier":       {db.env("MEMORY_REVIEW_CLASSIFIER", missing), nil},
		"budget policy":           {db.env("MEMORY_MODEL_BUDGET_POLICY", "{"), nil},
		"review policies":         {db.env("MEMORY_REVIEW_POLICIES", "{"), nil},
		"skill automation":        {db.env("MEMORY_SKILL_AUTOMATION_POLICIES", "{"), nil},
		"archive key":             {db.env("MEMORY_SESSION_ARCHIVE_KEY", "short"), nil},
		"blob directory":          {db.env("MEMORY_BLOB_DIRECTORY", missing), nil},
		"anthropic without key":   {db.env("ARKISM_ANTHROPIC_SKILL_DRAFTER", "1", "ARKISM_ANTHROPIC_KEY_FILE", missing), nil},
		"desktop control":         {db.env("ARKISM_DESKTOP_CONTROL", "2"), nil},
		"listener in use":         {db.env(), []string{"-listen", busy.Addr().String()}},
		"worker without embedder": {db.env(), []string{"-worker"}},
		"worker without token":    {db.env("MEMORY_EMBEDDER", "none", "MEMORY_WORKER_TOKEN", ""), []string{"-worker"}},
		"worker expiry settings":  {db.env("MEMORY_EMBEDDER", "none", "MEMORY_CANDIDATE_EXPIRY_DAYS", "0", "MEMORY_CANDIDATE_EXPIRY_SCOPE", "a/b/c/d"), []string{"-worker"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, code := runDaemon(t, c.env, c.args...); code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
		})
	}
	// Without a schema the restore publication check fails before any service starts.
	bare := newTestDB(t, false)
	if _, code := runDaemon(t, map[string]string{"MEMORY_DATABASE_URL": bare.url}); code != 1 {
		t.Fatal("uninitialized database served", code)
	}
}

func TestDaemonWorkerRunsRoundsUntilSIGTERM(t *testing.T) {
	db := newTestDB(t, true)
	iterated := func(stderr string) bool { return strings.Contains(stderr, "memory worker iteration") }
	sweep := []string{"MEMORY_EMBEDDER", "none", "MEMORY_PRIVACY_NO_AGENT_RUNTIME", "true",
		"MEMORY_CANDIDATE_EXPIRY_DAYS", "30", "MEMORY_CANDIDATE_EXPIRY_SCOPE", "default/default/management:owner/"}
	if code := serveUntilSIGTERM(t, db.env(sweep...), iterated, "-worker", "-log-level", "debug"); code != 0 {
		t.Fatal("worker stop", code)
	}
	// A token nobody granted defers each step with a warning instead of exiting.
	weak, _ := memory.NewPrincipalToken()
	if code := serveUntilSIGTERM(t, db.env(append(sweep, "MEMORY_WORKER_TOKEN", weak)...), iterated, "-worker", "-log-level", "debug"); code != 0 {
		t.Fatal("worker with an unknown token stop", code)
	}
	if code := serveUntilSIGTERM(t, db.env("MEMORY_EMBEDDER", "none"), iterated, "-worker", "-recovery", "-log-level", "debug"); code != 0 {
		t.Fatal("recovery worker stop", code)
	}
}
