package capture

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type sealTestInput struct {
	Scope       map[string]string      `json:"scope"`
	Renderer    string                 `json:"renderer"`
	BlockHash   string                 `json:"block_hash"`
	Fingerprint string                 `json:"fingerprint"`
	Refs        []host.StableMemoryRef `json:"refs"`
}

func sealTestRequest(t *testing.T, body any) sealTestInput {
	t.Helper()
	raw, e := json.Marshal(body)
	if e != nil {
		t.Fatal(e)
	}
	var in sealTestInput
	if e = json.Unmarshal(raw, &in); e != nil {
		t.Fatal(e)
	}
	if in.Refs == nil {
		in.Refs = []host.StableMemoryRef{}
	}
	return in
}
func sealTestUnavailable(in sealTestInput) map[string]any {
	rows := []any{}
	for _, ref := range in.Refs {
		rows = append(rows, map[string]any{"collection": ref.Collection, "id": ref.ID, "revision": ref.Revision, "status": "unavailable"})
	}
	return map[string]any{"authority": "data_only_untrusted", "integrity": false, "items": rows}
}

// Adds a synthetic server-key epoch to the existing current-state API fixture.
// Eligibility still comes from that fixture; only the seal's lifetime changes.
func sealTestEpochServer(t *testing.T, epoch *int) {
	t.Helper()
	post := capPost
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path != "/v1/context/validate" {
			return post(path, body, d)
		}
		in := sealTestRequest(t, body)
		if in.Fingerprint != "" {
			underlying := stableHash(struct {
				Scope               map[string]string
				Renderer, BlockHash string
				Refs                []host.StableMemoryRef
			}{in.Scope, in.Renderer, in.BlockHash, in.Refs})
			if in.Fingerprint != stableHash([]any{*epoch, underlying}) {
				return sealTestUnavailable(in), nil
			}
			body = host.Obj{{Key: "scope", Value: in.Scope}, {Key: "renderer", Value: in.Renderer}, {Key: "block_hash", Value: in.BlockHash}, {Key: "fingerprint", Value: underlying}, {Key: "refs", Value: in.Refs}}
		}
		res, e := post(path, body, d)
		if e != nil {
			return nil, e
		}
		if fp, ok := res["fingerprint"].(string); ok && fp != "" {
			res["fingerprint"] = stableHash([]any{*epoch, fp})
		}
		return res, nil
	}
}
func sealTestSetup(t *testing.T) (string, *[]any) {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	standing := stableFake(t)
	for k, v := range map[string]string{"ARKISM_WORKSPACE": "w", "ARKISM_PROJECT": "p", "ARKISM_USER": "u", "ARKISM_AGENT": "", "ARKISM_WORK_AGENT": "", "ARKISM_WORK_ID": "", "ARKISM_REPO": "", "ARKISM_SESSION_PROCESSOR": "0", "ARKISM_MEMORY_OUTCOME_SPOOL": ""} {
		t.Setenv(k, v)
	}
	oldScope := capScope
	capScope = host.Scope
	t.Cleanup(func() { capScope = oldScope })
	return root, standing
}
func sealTestStart(t *testing.T, sid, cwd string) (host.Part, *stableOutput, error) {
	t.Helper()
	lock, e := stableHookLock(sid)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	part, out, e := stableBlock(sid, cwd)
	if e == nil {
		markStableShown(sid, part.Full, out, cwd)
	}
	return part, out, e
}

func TestStableSealMismatchRebuildsCurrentStateOnce(t *testing.T) {
	root, standing := sealTestSetup(t)
	epoch := 1
	sealTestEpochServer(t, &epoch)
	old := memoryHit("epoch-rule", 7, "")
	old["kind"], old["text"] = "user", "synthetic old snapshot body"
	*standing = []any{old}
	first, _, e := sealTestStart(t, "epoch", root)
	if e != nil {
		t.Fatal(e)
	}
	saved, _ := sessionStore().Stable("epoch")
	epoch = 2
	current := memoryHit("epoch-rule", 8, "")
	current["kind"], current["text"] = "user", "synthetic current canonical body"
	*standing = []any{current}
	get, post := capGet, capPost
	reads, sealed, fresh := 0, 0, 0
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories?") {
			reads++
			if _, ok := sessionStore().Stable("epoch"); ok {
				t.Fatal("old snapshot survived into fresh canonical fetch")
			}
		}
		return get(path, d)
	}
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" {
			in := sealTestRequest(t, body)
			if in.Fingerprint == "" {
				fresh++
				if len(in.Refs) != 1 || in.Refs[0].Revision != 8 || in.Refs[0].Fingerprint != "" || in.BlockHash == saved.BlockHash {
					t.Fatal("cached evidence was reused for initial issuance")
				}
			} else {
				sealed++
			}
		}
		return post(path, body, d)
	}
	recovered, out, e := sealTestStart(t, "epoch", root)
	if e != nil || out == nil || out.Block == nil || !strings.Contains(recovered.Full, "synthetic current canonical body") || strings.Contains(recovered.Full, "synthetic old snapshot body") || recovered.Full == first.Full {
		t.Fatalf("server key rotation left a valid session unavailable: %v", e)
	}
	rebuilt, ok := sessionStore().Stable("epoch")
	if !ok || rebuilt.Fingerprint == saved.Fingerprint || rebuilt.Refs[0].Revision != 8 || reads != 2 || sealed != 1 || fresh != 1 {
		t.Fatal("recovery was not one fresh canonical attempt", ok, reads, sealed, fresh)
	}
	// The repaired snapshot must replay without another canonical fetch or mint.
	capGet = get
	again, _, e := sealTestStart(t, "epoch", root)
	if e != nil || again.Full != recovered.Full || fresh != 1 || sealed != 2 {
		t.Fatal("new-key snapshot did not replay normally", e)
	}
}

func TestStableSealMalformedMismatchDoesNotDiscardSnapshot(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"authority", func(r map[string]any) { r["authority"] = "other" }},
		{"integrity_type", func(r map[string]any) { r["integrity"] = "false" }},
		{"missing_items", func(r map[string]any) { delete(r, "items") }},
		{"null_items", func(r map[string]any) { r["items"] = nil }},
		{"partial_items", func(r map[string]any) { r["items"] = r["items"].([]any)[:1] }},
		{"reordered_refs", func(r map[string]any) { rows := r["items"].([]any); rows[0], rows[1] = rows[1], rows[0] }},
		{"duplicate_refs", func(r map[string]any) { rows := r["items"].([]any); rows[1] = rows[0] }},
		{"string_revision", func(r map[string]any) { r["items"].([]any)[0].(map[string]any)["revision"] = "7" }},
		{"fractional_revision", func(r map[string]any) { r["items"].([]any)[0].(map[string]any)["revision"] = 7.5 }},
		{"collection", func(r map[string]any) { r["items"].([]any)[0].(map[string]any)["collection"] = "sources" }},
		{"semantic_status", func(r map[string]any) { r["items"].([]any)[0].(map[string]any)["status"] = "valid" }},
		{"item_fingerprint", func(r map[string]any) { r["items"].([]any)[0].(map[string]any)["fingerprint"] = "" }},
		{"envelope_fingerprint", func(r map[string]any) { r["fingerprint"] = "" }},
		{"unexpected_body", func(r map[string]any) { r["items"].([]any)[0].(map[string]any)["text"] = "synthetic unexpected body" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, standing := sealTestSetup(t)
			epoch := 1
			sealTestEpochServer(t, &epoch)
			*standing = nil
			for _, id := range []string{"first", "second"} {
				it := memoryHit(id, 7, "")
				it["kind"] = "user"
				*standing = append(*standing, it)
			}
			_, _, e := sealTestStart(t, "malformed", root)
			if e != nil {
				t.Fatal(e)
			}
			original, _ := sessionStore().Stable("malformed")
			get, post := capGet, capPost
			reads, fresh := 0, 0
			capGet = func(path string, d time.Duration) (map[string]any, error) {
				if strings.HasPrefix(path, "/v1/memories?") {
					reads++
				}
				return get(path, d)
			}
			capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
				if path != "/v1/context/validate" {
					return post(path, body, d)
				}
				in := sealTestRequest(t, body)
				if in.Fingerprint == "" {
					fresh++
				}
				res := sealTestUnavailable(in)
				c.change(res)
				return res, nil
			}
			part, _, e := sealTestStart(t, "malformed", root)
			saved, ok := sessionStore().Stable("malformed")
			if e == nil || part.Full != "" || !ok || saved.Fingerprint != original.Fingerprint || saved.BlockHash != original.BlockHash || reads != 0 || fresh != 0 {
				t.Fatal("ambiguous false response discarded or replayed the cache", e, ok, reads, fresh)
			}
		})
	}
}

func TestStableSealSourceCorrectionStillPreservesRejectedSnapshot(t *testing.T) {
	root, standing := sealTestSetup(t)
	epoch := 1
	sealTestEpochServer(t, &epoch)
	item := memoryHit("source-correction", 7, "")
	item["kind"], item["text"] = "user", "synthetic unchanged memory body"
	*standing = []any{item}
	_, _, e := sealTestStart(t, "source", root)
	if e != nil {
		t.Fatal(e)
	}
	original, _ := sessionStore().Stable("source")
	item["source_state"] = "synthetic changed source without memory revision"
	get, post := capGet, capPost
	reads, fresh := 0, 0
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories?") {
			reads++
		}
		return get(path, d)
	}
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" && sealTestRequest(t, body).Fingerprint == "" {
			fresh++
		}
		return post(path, body, d)
	}
	part, _, e := sealTestStart(t, "source", root)
	saved, ok := sessionStore().Stable("source")
	if e == nil || part.Full != "" || !ok || saved.Fingerprint != original.Fingerprint || saved.Refs[0].Fingerprint != original.Refs[0].Fingerprint || reads != 0 || fresh != 0 {
		t.Fatal("source-invalid result became a seal recovery", e, ok, reads, fresh)
	}
}

func TestStableSealRecoveryCanonicalReadFailureNeverResealsOldBytes(t *testing.T) {
	root, standing := sealTestSetup(t)
	epoch := 1
	sealTestEpochServer(t, &epoch)
	(*standing)[0].(map[string]any)["text"] = "synthetic unavailable old canonical body"
	_, _, e := sealTestStart(t, "fetch-failure", root)
	if e != nil {
		t.Fatal(e)
	}
	epoch = 2
	get, post := capGet, capPost
	reads, sealed, fresh := 0, 0, 0
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories?") {
			reads++
			return nil, errTimeout
		}
		return get(path, d)
	}
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" {
			if sealTestRequest(t, body).Fingerprint == "" {
				fresh++
			} else {
				sealed++
			}
		}
		return post(path, body, d)
	}
	part, out, _ := sealTestStart(t, "fetch-failure", root)
	_, cached := sessionStore().Stable("fetch-failure")
	// The existing fresh renderer may still emit freshly fetched skills, but it
	// cannot store a partial block or request a seal over the unavailable memory.
	if strings.Contains(part.Full, "synthetic unavailable old canonical body") || cached || out != nil && out.Block != nil || sealed != 1 || fresh != 0 || reads != 1 {
		t.Fatal("canonical failure reused cached bytes or retried issuance", cached, sealed, fresh, reads)
	}
}

func TestStableSealRecoveryFreshIssuanceFailureStopsAfterOneAttempt(t *testing.T) {
	root, _ := sealTestSetup(t)
	epoch := 1
	sealTestEpochServer(t, &epoch)
	_, _, e := sealTestStart(t, "issuance-failure", root)
	if e != nil {
		t.Fatal(e)
	}
	epoch = 2
	get, post := capGet, capPost
	reads, sealed, fresh := 0, 0, 0
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories?") {
			reads++
		}
		return get(path, d)
	}
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" {
			in := sealTestRequest(t, body)
			if in.Fingerprint == "" {
				fresh++
				return sealTestUnavailable(in), nil
			}
			sealed++
		}
		return post(path, body, d)
	}
	part, _, e := sealTestStart(t, "issuance-failure", root)
	_, cached := sessionStore().Stable("issuance-failure")
	if e == nil || part.Full != "" || cached || reads != 2 || sealed != 1 || fresh != 1 {
		t.Fatal("fresh issuance failure retried or restored the old cache", e, cached, reads, sealed, fresh)
	}
}

func TestStableSealLateMismatchDoesNotBypassReplayDeadline(t *testing.T) {
	root, _ := sealTestSetup(t)
	epoch := 1
	sealTestEpochServer(t, &epoch)
	_, _, e := sealTestStart(t, "late", root)
	if e != nil {
		t.Fatal(e)
	}
	original, _ := sessionStore().Stable("late")
	get, post := capGet, capPost
	reads := 0
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories?") {
			reads++
		}
		return get(path, d)
	}
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" {
			time.Sleep(d + 10*time.Millisecond)
			return sealTestUnavailable(sealTestRequest(t, body)), nil
		}
		return post(path, body, d)
	}
	part, _, e := sealTestStart(t, "late", root)
	saved, ok := sessionStore().Stable("late")
	if e == nil || part.Full != "" || !ok || saved.Fingerprint != original.Fingerprint || reads != 0 {
		t.Fatal("late mismatch reset the revalidation budget", e, ok, reads)
	}
}

func sealTestAppend(t *testing.T, path string, data []byte) {
	t.Helper()
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write(data); e != nil {
		f.Close()
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
}
func sealTestConcurrentChild(t *testing.T, root string) {
	_, standing := sealTestSetup(t)
	t.Chdir(root)
	capState = root
	epoch := 2
	sealTestEpochServer(t, &epoch)
	item := memoryHit("shared-epoch-rule", 8, "")
	item["kind"], item["text"] = "user", "synthetic shared current canonical body"
	*standing = []any{item}
	get, post := capGet, capPost
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories?") {
			if _, cached := sessionStore().Stable("shared-epoch"); cached {
				t.Fatal("old cache not discarded before canonical fetch")
			}
			sealTestAppend(t, filepath.Join(root, "canonical-reads"), []byte("r"))
		}
		return get(path, d)
	}
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" && sealTestRequest(t, body).Fingerprint == "" {
			sealTestAppend(t, filepath.Join(root, "initial-seals"), []byte("s"))
		}
		return post(path, body, d)
	}
	label := os.Getenv("ARKISM_SEAL_RECOVERY_LABEL")
	if e := os.WriteFile(filepath.Join(root, label+"-ready"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	input := strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"shared-epoch"}`)
	if label == "first" {
		hookMain(input, blockedStableWriter{root})
	} else {
		hookMain(input, os.Stdout)
	}
}

func TestStableSealRecoverySerializesConcurrentStarts(t *testing.T) {
	if root := os.Getenv("ARKISM_SEAL_RECOVERY_ROOT"); root != "" {
		sealTestConcurrentChild(t, root)
		return
	}
	root, standing := sealTestSetup(t)
	capState = root
	epoch := 1
	sealTestEpochServer(t, &epoch)
	item := memoryHit("shared-epoch-rule", 7, "")
	item["kind"], item["text"] = "user", "synthetic shared old body"
	*standing = []any{item}
	if _, _, e := sealTestStart(t, "shared-epoch", root); e != nil {
		t.Fatal(e)
	}
	original, _ := sessionStore().Stable("shared-epoch")
	run := func(label string) (*exec.Cmd, *bytes.Buffer) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStableSealRecoverySerializesConcurrentStarts$")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "ARKISM_SEAL_RECOVERY_ROOT="+root, "ARKISM_SEAL_RECOVERY_LABEL="+label)
		out := new(bytes.Buffer)
		cmd.Stdout = out
		cmd.Stderr = out
		if e := cmd.Start(); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
		return cmd, out
	}
	first, firstOut := run("first")
	if !waitStableFixture(filepath.Join(root, "first-output"), 10*time.Second) {
		t.Fatal("recovery never reached its fresh output")
	}
	second, secondOut := run("second")
	if !waitStableFixture(filepath.Join(root, "second-ready"), 10*time.Second) {
		t.Fatal("second process never became ready")
	}
	done := make(chan error, 1)
	go func() { done <- second.Wait() }()
	early := false
	var secondError error
	select {
	case secondError = <-done:
		early = true
	case <-time.After(150 * time.Millisecond):
	}
	if e := os.WriteFile(filepath.Join(root, "release"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e := first.Wait(); e != nil {
		t.Fatal("first recovery failed", e, firstOut.String())
	}
	if !early {
		secondError = <-done
	}
	if secondError != nil {
		t.Fatal("second recovery failed", secondError, secondOut.String())
	}
	line := func(b *bytes.Buffer) string {
		for s := range strings.Lines(b.String()) {
			if strings.HasPrefix(s, `{"hookSpecificOutput"`) {
				return s
			}
		}
		return ""
	}
	a, b := line(firstOut), line(secondOut)
	reads, e := os.ReadFile(filepath.Join(root, "canonical-reads"))
	if e != nil {
		t.Fatal(e)
	}
	seals, e := os.ReadFile(filepath.Join(root, "initial-seals"))
	if e != nil {
		t.Fatal(e)
	}
	saved, ok := sessionStore().Stable("shared-epoch")
	if early || a == "" || a != b || !strings.Contains(a, "synthetic shared current canonical body") || strings.Contains(a, "synthetic shared old body") || len(reads) != 2 || len(seals) != 1 || !ok || saved.Fingerprint == original.Fingerprint || saved.Refs[0].Revision != 8 {
		t.Fatal("concurrent recovery did not share one newly committed snapshot", early, len(reads), len(seals), ok)
	}
}

func TestStableSealMismatchHandlesEmptyMemorySnapshotStrictly(t *testing.T) {
	root, standing := sealTestSetup(t)
	*standing = nil
	epoch := 1
	sealTestEpochServer(t, &epoch)
	_, _, e := sealTestStart(t, "empty-refs", root)
	if e != nil {
		t.Fatal(e)
	}
	original, _ := sessionStore().Stable("empty-refs")
	if len(original.Refs) != 0 {
		t.Fatal("fixture unexpectedly has memory refs")
	}
	epoch = 2
	get, post := capGet, capPost
	reads, fresh := 0, 0
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories?") {
			reads++
		}
		return get(path, d)
	}
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" && sealTestRequest(t, body).Fingerprint == "" {
			fresh++
		}
		return post(path, body, d)
	}
	_, _, e = sealTestStart(t, "empty-refs", root)
	rebuilt, ok := sessionStore().Stable("empty-refs")
	if e != nil || !ok || len(rebuilt.Refs) != 0 || rebuilt.Fingerprint == original.Fingerprint || reads != 2 || fresh != 1 {
		t.Fatal("valid empty-array mismatch did not rebuild once", e, ok, reads, fresh)
	}
	reads = 0
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" {
			reply := sealTestUnavailable(sealTestRequest(t, body))
			reply["items"] = nil
			return reply, nil
		}
		return post(path, body, d)
	}
	part, _, e := sealTestStart(t, "empty-refs", root)
	saved, ok := sessionStore().Stable("empty-refs")
	if e == nil || part.Full != "" || !ok || saved.Fingerprint != rebuilt.Fingerprint || reads != 0 {
		t.Fatal("null items was accepted as an exact empty-array mismatch", e, ok, reads)
	}
}
