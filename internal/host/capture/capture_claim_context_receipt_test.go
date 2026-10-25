package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func claimReceiptTestText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func contextReceiptFixture(t *testing.T) (map[string]any, map[string]any, []any, []byte) {
	t.Helper()
	users := []any{map[string]any{"text": "Remember the bounded test setting.", "at": "2026-01-01T00:00:00Z"}}
	receipt, err := newClaimContextReceipt(users, field(users[0], "text").(string))
	if err != nil {
		t.Fatal(err)
	}
	receipt.reject(0, "context_fields_invalid", "synthetic proposal", "synthetic quotation")
	job := map[string]any{"users": users, "event_id": "synthetic-receipt-event", "cwd": ""}
	envelope, err := bindClaimContextReceipt(receipt, job["event_id"], users)
	if err != nil {
		t.Fatal(err)
	}
	prepared := map[string]any{"claims": []any{}, "context_rejections": envelope}
	raw, err := json.Marshal(prepared["context_rejections"])
	if err != nil {
		t.Fatal(err)
	}
	return job, prepared, users, raw
}

func TestCaptureContextReceiptIsDurableBoundAndIdempotent(t *testing.T) {
	capFake(t)
	job, prepared, users, _ := contextReceiptFixture(t)
	if err := retainClaimContextReceipt(job, prepared, users); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(capState, "claim-context-rejections")
	before, _ := os.ReadDir(dir)
	if len(before) != 1 {
		t.Fatal("rejection has no durable receipt")
	}
	path := filepath.Join(dir, before[0].Name())
	original, _ := os.ReadFile(path)
	// A spool retry returns JSON maps instead of the original typed envelope.
	reloaded := mustLoad(t, dumps(prepared))
	if err := retainClaimContextReceipt(job, reloaded, users); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(dir)
	actual, _ := os.ReadFile(path)
	if len(after) != 1 || !bytes.Equal(original, actual) {
		t.Fatal("retry changed or duplicated its receipt")
	}
	for _, marker := range []string{"synthetic proposal", "synthetic quotation", "synthetic-receipt-event", "Remember"} {
		if bytes.Contains(actual, []byte(marker)) {
			t.Fatal("receipt contains unhashed source data")
		}
	}
	job["event_id"] = "changed-event"
	if errorCode(retainClaimContextReceipt(job, prepared, users)) != "claim_context_receipt_binding_changed" {
		t.Fatal("event change silently reused receipt")
	}
	job["event_id"] = "synthetic-receipt-event"
	users[0].(map[string]any)["text"] = "changed input"
	if errorCode(retainClaimContextReceipt(job, prepared, users)) != "claim_context_receipt_binding_changed" {
		t.Fatal("input change silently reused receipt")
	}
}

func TestCaptureContextReceiptPolicyStopsCleanupAndWrites(t *testing.T) {
	for _, mode := range []string{"paused", "private_prepared", "private_users", "late_policy"} {
		t.Run(mode, func(t *testing.T) {
			capFake(t)
			job, prepared, users, raw := contextReceiptFixture(t)
			dir := filepath.Join(capState, "claim-context-rejections")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, sha256Hex(raw)+".json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-73 * time.Hour)
			os.Chtimes(path, old, old)
			var err error
			switch mode {
			case "paused":
				privateTestPause(t)
				err = retainClaimContextReceipt(job, prepared, users)
			case "private_prepared":
				prepared["claims"] = []any{map[string]any{"text": "<private>synthetic opt-out</private>"}}
				err = retainClaimContextReceipt(job, prepared, users)
			case "private_users":
				users[0].(map[string]any)["text"] = "<private>synthetic opt-out</private>"
				err = retainClaimContextReceipt(job, prepared, users)
			case "late_policy":
				calls := 0
				err = storeClaimContextReceipt(raw, func() error {
					calls++
					if calls > 1 {
						return sessionWait("capture_policy_wait")
					}
					return nil
				})
			}
			actual, readErr := os.ReadFile(path)
			files, _ := os.ReadDir(dir)
			if !errors.Is(err, errSessionPending) || readErr != nil || !bytes.Equal(actual, raw) || len(files) != 1 {
				t.Fatal("policy wait cleaned or rewrote receipt storage", err, readErr)
			}
		})
	}
}

func TestCaptureContextReceiptRetentionIsBoundedAndForeignFilesSurvive(t *testing.T) {
	for _, mode := range []string{"count", "age"} {
		t.Run(mode, func(t *testing.T) {
			capFake(t)
			_, _, _, raw := contextReceiptFixture(t)
			dir := filepath.Join(capState, "claim-context-rejections")
			os.Mkdir(dir, 0o700)
			foreign := put(t, filepath.Join(dir, "foreign.json"), "foreign sentinel")
			partial := put(t, filepath.Join(dir, ".claim-context-"+sha256Hex(raw)+"-1.tmp"), "partial sentinel")
			count := claimReceiptRecords
			if mode == "age" {
				count = 1
			}
			var first string
			for index := range count {
				envelope, err := decodeClaimContextEnvelope(raw)
				if err != nil {
					t.Fatal(err)
				}
				envelope.EventSHA256 = sha256Hex([]byte(strconv.Itoa(index)))
				data, _ := json.Marshal(envelope)
				data = append(data, bytes.Repeat([]byte(" "), claimReceiptBytes-len(data))...)
				path := filepath.Join(dir, sha256Hex(data)+".json")
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				when := time.Now().Add(-time.Hour)
				if mode == "age" {
					when = time.Now().Add(-73 * time.Hour)
				}
				os.Chtimes(path, when, when)
				first = path
			}
			if err := storeClaimContextReceipt(raw, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			managed, err := managedClaimReceipts(dir, func() error { return nil })
			total := int64(0)
			for _, file := range managed {
				total += file.info.Size()
			}
			if err != nil || len(managed) > claimReceiptRecords || total > claimReceiptTotalBytes || claimReceiptTestText(t, foreign) != "foreign sentinel" || claimReceiptTestText(t, partial) != "partial sentinel" {
				t.Fatal("receipt cap or foreign preservation failed", err, len(managed), total)
			}
			if mode == "age" && exists(first) {
				t.Fatal("expired managed receipt retained")
			}
		})
	}
}

func TestCaptureContextReceiptRejectsAliasesAndMalformedMetadata(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	os.Mkdir(dir, 0o700)
	outside := put(t, filepath.Join(t.TempDir(), "sentinel"), "untouched")
	link := filepath.Join(dir, strings.Repeat("a", 64)+".json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("platform cannot create fixture symlink")
	}
	if err := storeClaimContextReceipt(raw, func() error { return nil }); err == nil || claimReceiptTestText(t, outside) != "untouched" {
		t.Fatal("receipt followed or cleaned an alias")
	}
	if _, err := decodeClaimContextEnvelope(append(raw, []byte(" {}")...)); err == nil {
		t.Fatal("receipt accepted trailing data")
	}
}

func TestCaptureContextReceiptForeignTemporaryAliasIsNeverFollowed(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	os.Mkdir(dir, 0o700)
	outside := put(t, filepath.Join(t.TempDir(), "sentinel"), "outside sentinel")
	name := sha256Hex(raw) + ".json"
	poison := filepath.Join(dir, "."+name+".tmp")
	if err := os.Symlink(outside, poison); err != nil {
		t.Skip("platform cannot create fixture symlink")
	}
	err := storeClaimContextReceipt(raw, func() error { return nil })
	if claimReceiptTestText(t, outside) != "outside sentinel" {
		t.Fatal("foreign temporary alias was followed")
	}
	if err == nil {
		info, statErr := os.Lstat(filepath.Join(dir, name))
		if statErr != nil || !info.Mode().IsRegular() {
			t.Fatal("receipt publication installed an alias")
		}
	}
}

func TestCaptureContextReceiptFailureKeepsPreparedAndRetriesWithoutModel(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{}, true, nil }
	models, posts, checkpoints := 0, 0, 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		models++
		invalid := optionalClaimContext()
		invalid["observed_at"] = "not a model field"
		return []any{optionalContextClaim(invalid), optionalContextClaim(optionalClaimContext())}, nil
	}
	capPost = func(_ string, payload any, _ time.Duration) (map[string]any, error) {
		posts++
		if len(field(payload, "claims").([]any)) != 1 {
			t.Fatal("invalid item was delivered")
		}
		return map[string]any{"items": []any{map[string]any{"status": "candidate"}}}, nil
	}
	job := map[string]any{"session_id": "synthetic-context", "event_id": "synthetic-context-event", "type": "turn_completed",
		"users": []any{map[string]any{"text": optionalContextSource, "at": "2026-01-01T00:00:00Z"}}}
	dir := put(t, filepath.Join(capState, "claim-context-rejections"), "directory blocker")
	persist := func() error { checkpoints++; return nil }
	if errorCode(process(job, persist)) != "claim_context_receipt_directory_invalid" || models != 1 || posts != 0 || checkpoints != 1 {
		t.Fatal("diagnostic failure became delivery or re-extraction", models, posts, checkpoints)
	}
	prepared := mustLoad(t, dumps(job["prepared_capture"]))
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	job = mustLoad(t, dumps(job))
	if err := process(job, persist); err != nil || models != 1 || posts != 1 || !reflect.DeepEqual(mustLoad(t, dumps(job["prepared_capture"])), prepared) {
		t.Fatal("retry lost its prepared claims or reasons", err, models, posts)
	}
}

func TestCaptureOptionalContextAllWithheldHasDurableReceiptAndDistinctMetric(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{}, true, nil }
	models := 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		models++
		invalid := optionalClaimContext()
		invalid["domains"] = []any{}
		return []any{optionalContextClaim(invalid)}, nil
	}
	statuses := []string{}
	metric = func(fields ...host.KV) {
		for _, field := range fields {
			if field.Key == "status" {
				statuses = append(statuses, field.Value.(string))
			}
			if text, ok := field.Value.(string); ok && strings.Contains(text, "시험 설정") {
				t.Fatal("model text leaked into metrics")
			}
		}
	}
	job := map[string]any{"session_id": "synthetic-context", "event_id": "synthetic-all-withheld", "type": "turn_completed",
		"users": []any{map[string]any{"text": optionalContextSource, "at": "2026-01-01T00:00:00Z"}}}
	checkpoints := 0
	if err := process(job, func() error { checkpoints++; return nil }); errorCode(err) != "capture_context_all_withheld" {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(capState, "claim-context-rejections"))
	if err != nil || len(files) != 1 || truthy(job["captured"]) || checkpoints != 1 || !reflect.DeepEqual(statuses, []string{"context_withheld"}) {
		t.Fatal("withheld extraction resembled a successful empty extraction", err, statuses, checkpoints)
	}
	job = mustLoad(t, dumps(job))
	if errorCode(process(job, func() error { checkpoints++; return nil })) != "capture_context_all_withheld" || models != 1 || checkpoints != 1 {
		t.Fatal("withheld retry re-extracted or silently succeeded", models, checkpoints)
	}
}

func TestCaptureContextReceiptRechecksPolicyAfterPreparedCheckpoint(t *testing.T) {
	for _, mode := range []string{"paused", "private_context"} {
		t.Run(mode, func(t *testing.T) {
			capFake(t)
			t.Setenv("ARKISM_SESSION_PROCESSOR", "")
			search = func(string, string, int, float64) ([]any, bool, error) { return []any{}, true, nil }
			llm = func(string, string, string, time.Duration, string, string) (any, error) {
				invalid := optionalClaimContext()
				invalid["domains"] = []any{}
				return []any{optionalContextClaim(invalid), optionalContextClaim(optionalClaimContext())}, nil
			}
			job := map[string]any{"session_id": "synthetic-context", "event_id": "synthetic-policy-change", "type": "turn_completed",
				"users": []any{map[string]any{"text": optionalContextSource, "at": "2026-01-01T00:00:00Z"}}}
			persist := func() error {
				if mode == "paused" {
					privateTestPause(t)
				} else {
					claim := field(job["prepared_capture"], "claims").([]any)[0].(map[string]any)
					context := claim["memory_context"].(host.Obj)
					context.Set("applicability", "<private>synthetic opt-out</private>")
					claim["memory_context"] = context
				}
				return nil
			}
			if err := process(job, persist); !errors.Is(err, errSessionPending) || exists(filepath.Join(capState, "claim-context-rejections")) {
				t.Fatal("late policy change wrote a diagnostic", err)
			}
		})
	}
}

func TestCaptureContextReceiptBindsInputRowIdentity(t *testing.T) {
	for _, mode := range []string{"time", "role", "archive", "boundary"} {
		t.Run(mode, func(t *testing.T) {
			capFake(t)
			job, prepared, users, _ := contextReceiptFixture(t)
			changed := []any{mustLoad(t, dumps(users[0]))}
			row := changed[0].(map[string]any)
			switch mode {
			case "time":
				row["at"] = "2026-01-02T00:00:00Z"
			case "role":
				row["role"] = "user"
			case "archive":
				row["archive_hint"] = map[string]any{"id": "synthetic-changed-source"}
			case "boundary":
				row["text"] = "Remember the bounded"
				changed = append(changed, map[string]any{"text": "test setting.", "at": row["at"]})
				// Keep the joined extraction text exactly the same across two boundaries.
				joined := "Remember the bounded\ntest setting."
				original := []any{map[string]any{"text": joined, "at": row["at"]}}
				receipt, _ := newClaimContextReceipt(original, joined)
				receipt.reject(0, "context_fields_invalid", "synthetic proposal", "synthetic quote")
				envelope, err := bindClaimContextReceipt(receipt, job["event_id"], original)
				if err != nil {
					t.Fatal(err)
				}
				prepared["context_rejections"] = envelope
			}
			if errorCode(retainClaimContextReceipt(job, prepared, changed)) != "claim_context_receipt_binding_changed" || exists(filepath.Join(capState, "claim-context-rejections")) {
				t.Fatal("same-text source identity change reached receipt storage")
			}
		})
	}
}

func TestCaptureContextReceiptPublicationCleansOnlyItsTemporary(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	os.Mkdir(dir, 0o700)
	path := filepath.Join(dir, sha256Hex(raw)+".json")
	foreign := put(t, filepath.Join(dir, "foreign.tmp"), "foreign neighbor")
	calls := 0
	err := writeClaimReceipt(path, raw, func() error {
		calls++
		if calls == 3 {
			if err := os.WriteFile(path, []byte("foreign destination"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	files, _ := os.ReadDir(dir)
	if errorCode(err) != "claim_context_receipt_file_changed" || len(files) != 2 || claimReceiptTestText(t, path) != "foreign destination" || claimReceiptTestText(t, foreign) != "foreign neighbor" {
		t.Fatal("publication failure deleted foreign state or retained its own temporary", err, len(files))
	}
}

func TestCaptureContextReceiptLatePauseRetainsBoundedTemporary(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	os.Mkdir(dir, 0o700)
	path := filepath.Join(dir, sha256Hex(raw)+".json")
	calls := 0
	err := writeClaimReceipt(path, raw, func() error {
		calls++
		if calls >= 3 {
			return sessionWait("capture_policy_wait")
		}
		return nil
	})
	files, _ := os.ReadDir(dir)
	if !errors.Is(err, errSessionPending) || len(files) != 1 || exists(path) {
		t.Fatal("late pause published or cleaned state", err, len(files))
	}
	temporary := filepath.Join(dir, files[0].Name())
	if claimReceiptTestText(t, temporary) != string(raw) {
		t.Fatal("paused temporary lost content binding")
	}
	when := time.Now().Add(-73 * time.Hour)
	os.Chtimes(temporary, when, when)
	if err := storeClaimContextReceipt(raw, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	files, _ = os.ReadDir(dir)
	if len(files) != 1 || !exists(path) || exists(temporary) {
		t.Fatal("resumed retention did not reclaim only its expired bound temporary")
	}
}

func TestCaptureContextReceiptTornTemporaryDoesNotBlockStorage(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	os.Mkdir(dir, 0o700)
	torn := put(t, filepath.Join(dir, ".claim-context-"+sha256Hex(raw)+"-1.tmp"), string(raw[:len(raw)/2]))
	if err := storeClaimContextReceipt(raw, func() error { return nil }); err != nil || !exists(torn) {
		t.Fatal("fresh torn temporary blocked storage or was removed early", err)
	}
	when := time.Now().Add(-73 * time.Hour)
	os.Chtimes(torn, when, when)
	if err := storeClaimContextReceipt(raw, func() error { return nil }); err != nil || exists(torn) {
		t.Fatal("expired torn temporary was not reclaimed", err)
	}
}

func TestCaptureOptionalContextGeneratorIsFixedInPreparedRetry(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	oldModel := capModel
	t.Cleanup(func() { capModel = oldModel })
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{}, true, nil }
	models, posts := 0, 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		models++
		return []any{optionalContextClaim(optionalClaimContext())}, nil
	}
	var first any
	capPost = func(_ string, payload any, _ time.Duration) (map[string]any, error) {
		posts++
		actual := mustLoad(t, dumps(field(payload, "generator")))
		if posts == 1 {
			first = actual
			return nil, runtimeErr("synthetic_delivery_failure")
		}
		if !reflect.DeepEqual(actual, first) || field(payload, "generator") == nil {
			t.Fatal("retry generator changed")
		}
		return map[string]any{"items": []any{map[string]any{"status": "candidate"}}}, nil
	}
	job := map[string]any{"session_id": "synthetic-context", "event_id": "synthetic-generator", "type": "turn_completed", "users": []any{map[string]any{"text": optionalContextSource, "at": "2026-01-01T00:00:00Z"}}}
	if errorCode(process(job, func() error { return nil })) != "synthetic_delivery_failure" {
		t.Fatal("first delivery did not fail")
	}
	if field(job["prepared_capture"], "generator") == nil || field(first, "prompt_version") != "capture-extract-v5" {
		t.Fatal("new extraction has no fixed version")
	}
	job = mustLoad(t, dumps(job))
	capModel = "synthetic-retry-model"
	if err := process(job, func() error { return nil }); err != nil || models != 1 || posts != 2 {
		t.Fatal("prepared retry re-extracted", err, models, posts)
	}
	prepared := field(job, "prepared_capture").(map[string]any)
	delete(prepared, "generator")
	gen, err := preparedClaimGenerator(prepared, "synthetic-old")
	if err != nil || field(gen, "prompt_version") != "capture-extract-v2" || field(defaultGenerator(), "prompt_version") != "capture-extract-v2" {
		t.Fatal("old prepared/backfill version changed", err)
	}
}

func claimReceiptUnknownTemporary(t *testing.T, dir string, raw []byte, kind string) (string, []byte, os.FileInfo) {
	t.Helper()
	digest, content := sha256Hex(raw), []byte{}
	switch kind {
	case "partial":
		content = raw[:len(raw)/2]
	case "wrong_hash":
		content = []byte("foreign ordinary sentinel")
	case "invalid_envelope":
		content = []byte(`{"module":"unrecognized"}`)
		digest = sha256Hex(content)
	}
	path := put(t, filepath.Join(dir, ".claim-context-"+digest+"-1.tmp"), string(content))
	original, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-73 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path, content, original
}

func claimReceiptExpiredOrphan(t *testing.T, dir string, raw []byte) string {
	t.Helper()
	envelope, err := decodeClaimContextEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	envelope.EventSHA256 = sha256Hex([]byte("expired valid orphan"))
	orphanRaw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	path := put(t, filepath.Join(dir, ".claim-context-"+sha256Hex(orphanRaw)+"-2.tmp"), string(orphanRaw))
	old := time.Now().Add(-73 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCaptureContextReceiptUnrecognizedTemporaryDoesNotBlockSiblings(t *testing.T) {
	for _, kind := range []string{"empty", "partial", "wrong_hash", "invalid_envelope"} {
		t.Run(kind, func(t *testing.T) {
			capFake(t)
			_, _, _, raw := contextReceiptFixture(t)
			dir := filepath.Join(capState, "claim-context-rejections")
			unknown, content, original := claimReceiptUnknownTemporary(t, dir, raw, kind)
			orphan := claimReceiptExpiredOrphan(t, dir, raw)
			managed, err := managedClaimReceipts(dir, func() error { return nil })
			if err != nil || len(managed) != 1 || managed[0].path != orphan {
				t.Fatal("unrecognized temporary blocked a valid sibling", err, len(managed))
			}
			if err := storeClaimContextReceipt(raw, func() error { return nil }); err != nil {
				t.Fatal("unrecognized temporary blocked the incoming receipt", err)
			}
			current, err := os.Lstat(unknown)
			actual, readErr := os.ReadFile(unknown)
			if err != nil || readErr != nil || !os.SameFile(original, current) || !bytes.Equal(content, actual) || exists(orphan) {
				t.Fatal("retention changed unrecognized state or kept an expired valid orphan", err, readErr)
			}
			managed, err = managedClaimReceipts(dir, func() error { return nil })
			if err != nil || len(managed) != 1 || managed[0].path != filepath.Join(dir, sha256Hex(raw)+".json") {
				t.Fatal("normal receipt did not survive orphan recovery", err, len(managed))
			}
			if kind == "partial" {
				if exists(unknown) {
					t.Fatal("complete final reference did not authorize expired prefix cleanup")
				}
			} else {
				claimReceiptAssertPreserved(t, unknown, original, content)
			}
		})
	}
}

func TestCaptureContextReceiptPartialTemporaryPreparedRetries(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{}, true, nil }
	models, posts := 0, 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		models++
		invalid := optionalClaimContext()
		invalid["observed_at"] = "not a model field"
		return []any{optionalContextClaim(invalid), optionalContextClaim(optionalClaimContext())}, nil
	}
	capPost = func(_ string, payload any, _ time.Duration) (map[string]any, error) {
		posts++
		if len(field(payload, "claims").([]any)) != 1 {
			t.Fatal("withheld context item was delivered")
		}
		if posts < 3 {
			return nil, runtimeErr("synthetic_delivery_failure")
		}
		return map[string]any{"items": []any{map[string]any{"status": "candidate"}}}, nil
	}
	dir := filepath.Join(capState, "claim-context-rejections")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, _, raw := contextReceiptFixture(t)
	partial := filepath.Join(dir, ".claim-context-"+sha256Hex(raw)+"-1.tmp")
	content := raw[:len(raw)/2]
	if err := os.WriteFile(partial, content, 0o600); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{"session_id": "synthetic-context", "event_id": "synthetic-partial-retry", "type": "turn_completed",
		"users": []any{map[string]any{"text": optionalContextSource, "at": "2026-01-01T00:00:00Z"}}}
	persist := func() error { return nil }
	var prepared map[string]any
	for attempt := range 3 {
		err := process(job, persist)
		if attempt < 2 && errorCode(err) != "synthetic_delivery_failure" || attempt == 2 && err != nil {
			t.Fatal("partial temporary prevented normal sibling delivery", attempt, err)
		}
		actual := mustLoad(t, dumps(job["prepared_capture"]))
		if attempt == 0 {
			prepared = actual
		}
		if !reflect.DeepEqual(prepared, actual) {
			t.Fatal("serialized prepared retry changed its claims", attempt)
		}
		job = mustLoad(t, dumps(job)) // Restart-style spool reload; no extraction state is regenerated.
	}
	if models != 1 || posts != 3 || !truthy(job["captured"]) || !bytes.Equal([]byte(claimReceiptTestText(t, partial)), content) {
		t.Fatal("successful retry removed the foreign partial or stayed uncaptured")
	}
	managed, err := managedClaimReceipts(dir, func() error { return nil })
	if err != nil || len(managed) != 1 {
		t.Fatal("serialized retries duplicated the bound receipt", err, len(managed))
	}
}

func TestCaptureContextReceiptOwnStyleTemporaryAliasStillFailsClosed(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := put(t, filepath.Join(t.TempDir(), "sentinel"), "outside sentinel")
	alias := filepath.Join(dir, ".claim-context-"+sha256Hex(raw)+"-1.tmp")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skip("platform cannot create fixture symlink")
	}
	original, err := os.Lstat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeClaimContextReceipt(raw, func() error { return nil }); err == nil {
		t.Fatal("own-style temporary alias bypassed no-follow checks")
	}
	current, err := os.Lstat(alias)
	if err != nil || !os.SameFile(original, current) || claimReceiptTestText(t, outside) != "outside sentinel" || exists(filepath.Join(dir, sha256Hex(raw)+".json")) {
		t.Fatal("alias failure modified an unrelated file or published a receipt", err)
	}
}

func TestCaptureContextReceiptExpiredForeignTemporaryIsPreserved(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	foreign := put(t, filepath.Join(dir, ".claim-context-"+sha256Hex(raw)+"-1.tmp"), "foreign ordinary sentinel")
	original, err := os.Lstat(foreign)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-73 * time.Hour)
	if err := os.Chtimes(foreign, old, old); err != nil {
		t.Fatal(err)
	}
	if err := storeClaimContextReceipt(raw, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	current, err := os.Lstat(foreign)
	if err != nil || !os.SameFile(original, current) || claimReceiptTestText(t, foreign) != "foreign ordinary sentinel" {
		t.Fatal("expired filename-looking foreign file was removed", err)
	}
}

func claimReceiptAssertPreserved(t *testing.T, path string, original os.FileInfo, content []byte) {
	t.Helper()
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(original, current) || !bytes.Equal([]byte(claimReceiptTestText(t, path)), content) {
		t.Fatal("unrecognized receipt file changed", err)
	}
}

func claimReceiptReferenceFixture(t *testing.T, kind string) ([]byte, string, []byte, os.FileInfo) {
	t.Helper()
	_, _, _, incoming := contextReceiptFixture(t)
	full := append([]byte(nil), incoming...)
	if kind == "no_reference" {
		envelope, err := decodeClaimContextEnvelope(full)
		if err != nil {
			t.Fatal(err)
		}
		envelope.EventSHA256 = sha256Hex([]byte("unpublished different event"))
		full, err = json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
	}
	if kind == "invalid_reference" {
		full = []byte(`{"module":"unrecognized"}`)
	}
	if kind == "noncanonical_reference" {
		full = append(full, ' ')
	}
	partial := full[:len(full)/2]
	if kind == "empty" {
		partial = nil
	}
	if kind == "nonprefix" {
		partial = []byte("foreign ordinary sentinel")
	}
	dir := filepath.Join(capState, "claim-context-rejections")
	digest := sha256Hex(full)
	path := put(t, filepath.Join(dir, ".claim-context-"+digest+"-1.tmp"), string(partial))
	if kind != "no_reference" {
		name := digest + ".json"
		if kind == "complete_temp" {
			name = ".claim-context-" + digest + "-2.tmp"
		}
		put(t, filepath.Join(dir, name), string(full))
	}
	if kind != "fresh_prefix" {
		old := time.Now().Add(-73 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return incoming, path, partial, info
}

func TestCaptureContextReceiptPartialReferenceRules(t *testing.T) {
	for _, kind := range []string{"empty", "nonprefix", "no_reference", "invalid_reference", "noncanonical_reference", "fresh_prefix", "complete_temp"} {
		t.Run(kind, func(t *testing.T) {
			capFake(t)
			raw, path, partial, original := claimReceiptReferenceFixture(t, kind)
			if err := storeClaimContextReceipt(raw, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if kind == "complete_temp" {
				if exists(path) {
					t.Fatal("expired proper prefix of a complete temporary was not reclaimed")
				}
				return
			}
			claimReceiptAssertPreserved(t, path, original, partial)
		})
	}
}

func claimReceiptMutation(t *testing.T, kind, partial, reference string) func() error {
	t.Helper()
	switch kind {
	case "reference_bytes":
		return func() error { return os.WriteFile(reference, []byte("changed reference"), 0o600) }
	case "partial_bytes":
		return func() error { return os.WriteFile(partial, []byte("changed partial"), 0o600) }
	case "partial_identity":
		return func() error {
			if err := os.Rename(partial, partial+".saved"); err != nil {
				return err
			}
			return os.WriteFile(partial, []byte("replacement sentinel"), 0o600)
		}
	case "directory_identity":
		return func() error {
			dir := filepath.Dir(partial)
			if err := os.Rename(dir, dir+".saved"); err != nil {
				return err
			}
			return os.Mkdir(dir, 0o700)
		}
	default:
		return func() error { return sessionWait("capture_policy_wait") }
	}
}

func assertClaimReceiptMutationPreserved(t *testing.T, kind, partial string, content []byte) {
	t.Helper()
	switch kind {
	case "partial_bytes":
		if claimReceiptTestText(t, partial) != "changed partial" {
			t.Fatal("cleanup deleted the changed partial")
		}
	case "partial_identity":
		if claimReceiptTestText(t, partial) != "replacement sentinel" || !bytes.Equal([]byte(claimReceiptTestText(t, partial+".saved")), content) {
			t.Fatal("cleanup deleted a replacement or its original")
		}
	case "directory_identity":
		moved := filepath.Join(filepath.Dir(partial)+".saved", filepath.Base(partial))
		if !bytes.Equal([]byte(claimReceiptTestText(t, moved)), content) {
			t.Fatal("cleanup deleted through a replaced directory")
		}
	default:
		if !bytes.Equal([]byte(claimReceiptTestText(t, partial)), content) {
			t.Fatal("cleanup deleted a partial after reference or policy changed")
		}
	}
}

func TestCaptureContextReceiptPartialCleanupRechecksInputs(t *testing.T) {
	for _, kind := range []string{"reference_bytes", "partial_bytes", "partial_identity", "directory_identity", "late_policy"} {
		t.Run(kind, func(t *testing.T) {
			capFake(t)
			_, partial, _, _ := claimReceiptReferenceFixture(t, "nonprefix")
			_, _, _, full := contextReceiptFixture(t)
			// Turn the foreign fixture into a real expired prefix, then mutate
			// only after both scan passes have selected this complete reference.
			content := full[:len(full)/2]
			if err := os.WriteFile(partial, content, 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-73 * time.Hour)
			if err := os.Chtimes(partial, old, old); err != nil {
				t.Fatal(err)
			}
			reference := filepath.Join(filepath.Dir(partial), sha256Hex(full)+".json")
			mutation := claimReceiptMutation(t, kind, partial, reference)
			calls := 0
			_, err := managedClaimReceipts(filepath.Dir(partial), func() error {
				calls++
				if calls == 2 {
					return mutation()
				}
				return nil
			})
			if err == nil || calls != 2 {
				t.Fatal("changed cleanup inputs were ignored", err, calls)
			}
			assertClaimReceiptMutationPreserved(t, kind, partial, content)
		})
	}
}

func TestCaptureContextReceiptPartialCannotRelaxIncomingTarget(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	target := put(t, filepath.Join(dir, sha256Hex(raw)+".json"), "foreign incoming target")
	partial := put(t, filepath.Join(dir, ".claim-context-"+sha256Hex(raw)+"-1.tmp"), string(raw[:len(raw)/2]))
	old := time.Now().Add(-73 * time.Hour)
	if err := os.Chtimes(partial, old, old); err != nil {
		t.Fatal(err)
	}
	if errorCode(storeClaimContextReceipt(raw, func() error { return nil })) != "claim_context_receipt_file_changed" {
		t.Fatal("a partial temporary authorized replacing a foreign target")
	}
	if claimReceiptTestText(t, target) != "foreign incoming target" || claimReceiptTestText(t, partial) != string(raw[:len(raw)/2]) {
		t.Fatal("target collision deleted unrecognized state")
	}
}

func TestCaptureContextReceiptTemporaryReadBoundStillFailsClosed(t *testing.T) {
	capFake(t)
	_, _, _, raw := contextReceiptFixture(t)
	dir := filepath.Join(capState, "claim-context-rejections")
	content := bytes.Repeat([]byte("x"), claimReceiptBytes+1)
	oversized := put(t, filepath.Join(dir, ".claim-context-"+sha256Hex(raw)+"-1.tmp"), string(content))
	old := time.Now().Add(-73 * time.Hour)
	if err := os.Chtimes(oversized, old, old); err != nil {
		t.Fatal(err)
	}
	if errorCode(storeClaimContextReceipt(raw, func() error { return nil })) != "claim_context_receipt_file_invalid" {
		t.Fatal("unknown temporary bypassed the bounded read")
	}
	if claimReceiptTestText(t, oversized) != string(content) || exists(filepath.Join(dir, sha256Hex(raw)+".json")) {
		t.Fatal("oversized temporary was removed or a receipt was published")
	}
}
