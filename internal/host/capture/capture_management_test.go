package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpiredCaptureLeavesBodylessUnknownCompletionReceipt(t *testing.T) {
	root := t.TempDir()
	now := timeNow()
	path := filepath.Join(root, "private-session.json")
	secret := "synthetic-private-utterance-never-in-receipts"
	body := map[string]any{"queued_at": now - spoolTTL, "text": secret, "session_prepared": map[string]any{"text": secret}}
	raw, _ := json.Marshal(body)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	n, err := cleanupSpool(root, "queued_at", now)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	files, err := os.ReadDir(filepath.Join(root, ".expiry-receipts"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	raw, err = os.ReadFile(filepath.Join(root, ".expiry-receipts", files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private-session") || strings.Contains(string(raw), root) {
		t.Fatal("private content in receipt")
	}
	var r captureExpiryReceipt
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if !r.Prepared || !r.Removed || r.Completion != "not_verified" {
		t.Fatal(r)
	}
	// The metadata itself expires even if no new copy is removed that day.
	touch(t, filepath.Join(root, ".expiry-receipts", files[0].Name()), now-spoolTTL)
	if _, err = cleanupSpool(root, "queued_at", now); err != nil {
		t.Fatal(err)
	}
	files, err = os.ReadDir(filepath.Join(root, ".expiry-receipts"))
	if err != nil || len(files) != 0 {
		t.Fatal("receipt retention", files, err)
	}
}

func TestSessionValidationDiagnosticDoesNotClaimSemanticCompleteness(t *testing.T) {
	d := sessionValidationDiagnostic("candidate_context_evidence_mismatch", 0, 4, 0)
	if d["status"] != "withheld" || d["semantic_completeness"] != "not_measured" {
		t.Fatal(d)
	}
	d = sessionValidationDiagnostic("", 0, 4, 2)
	if d["status"] != "validated" || d["semantic_completeness"] != "not_measured" {
		t.Fatal(d)
	}
	d = sessionValidationDiagnostic("unexpected private payload", 0, 4, 0)
	if d["reason"] != "validation_unavailable" {
		t.Fatal("unsafe reason copied", d)
	}
}

func TestCaptureExpiryReceiptBoundsAndRejectsDirectoryAlias(t *testing.T) {
	root := t.TempDir()
	now := timeNow()
	dir := filepath.Join(root, ".expiry-receipts")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 260; i++ {
		name := sha256Hex([]byte(fmt.Sprint(i))) + ".json"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	foreign := filepath.Join(dir, "unmanaged-note")
	os.WriteFile(foreign, []byte("keep"), 0600)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("keep"), 0600)
	link := filepath.Join(dir, sha256Hex([]byte("link"))+".json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	if err := pruneCaptureExpiryReceipts(root, now); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 258 {
		t.Fatal("bounded owned metadata", len(files))
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "keep" {
		t.Fatal("followed alias", err)
	}
	aliasRoot := t.TempDir()
	target := t.TempDir()
	os.Symlink(target, filepath.Join(aliasRoot, ".expiry-receipts"))
	if err := saveCaptureExpiryReceipt(aliasRoot, "copy.json", now-1, now, false, true); err == nil {
		t.Fatal("directory alias accepted")
	}
	files, _ = os.ReadDir(target)
	if len(files) != 0 {
		t.Fatal("wrote outside receipt root")
	}
}
