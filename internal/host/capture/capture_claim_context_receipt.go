package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const claimReceiptRecords = 256
const claimReceiptBytes = 16 << 10
const claimReceiptTotalBytes = claimReceiptRecords * claimReceiptBytes
const claimReceiptRetention = 72 * time.Hour

type claimContextEnvelope struct {
	Module          string               `json:"module"`
	Binding         string               `json:"binding_status"`
	ScopeSHA256     string               `json:"scope_sha256"`
	EventSHA256     string               `json:"event_sha256"`
	InputRowsSHA256 string               `json:"input_rows_sha256"`
	Receipt         *ClaimContextReceipt `json:"context_rejections"`
}

// Input rows are the exact cued envelope available again on a spool retry.
// This includes boundaries and archive identity without claiming server authority.
func claimContextInputDigest(users []any) (string, error) {
	raw, err := host.Dumps(users, host.DumpOpts{NoASCII: true, SortKeys: true})
	if err != nil {
		return "", runtimeErr("claim_context_receipt_binding_changed")
	}
	return sha256Hex([]byte(raw)), nil
}

func bindClaimContextReceipt(receipt *ClaimContextReceipt, event any, cued []any) (*claimContextEnvelope, error) {
	input, err := claimContextInputDigest(cued)
	if err != nil {
		return nil, err
	}
	return &claimContextEnvelope{"capture-claim-context-v1", "bound", sdigest(capScope()), sdigest(event), input, receipt}, nil
}

func claimReceiptHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !strings.ContainsRune("0123456789abcdef", ch) {
			return false
		}
	}
	return true
}

func validClaimContextReceipt(r *ClaimContextReceipt) bool {
	if r == nil || r.Version != 1 || r.BindingStatus != "unbound" || !claimReceiptHash(r.UserInputSHA256) ||
		!claimReceiptHash(r.SourceRowsSHA256) || len(r.Rejected) < 1 || len(r.Rejected) > 16 {
		return false
	}
	previous := -1
	for _, item := range r.Rejected {
		if item.Index <= previous || item.Index >= 16 || !claimReceiptHash(item.TextSHA256) || !claimReceiptHash(item.QuoteSHA256) ||
			!slices.Contains([]string{"context_fields_invalid", "context_domains_invalid", "context_text_invalid", "context_kind_invalid", "context_bytes_exceeded", "context_batch_bytes_exceeded"}, item.Reason) {
			return false
		}
		previous = item.Index
	}
	return true
}

func decodeClaimContextEnvelope(raw []byte) (*claimContextEnvelope, error) {
	if len(raw) > claimReceiptBytes {
		return nil, runtimeErr("claim_context_receipt_invalid")
	}
	var envelope claimContextEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Module != "capture-claim-context-v1" ||
		envelope.Binding != "bound" || !claimReceiptHash(envelope.ScopeSHA256) || !claimReceiptHash(envelope.EventSHA256) || !claimReceiptHash(envelope.InputRowsSHA256) || !validClaimContextReceipt(envelope.Receipt) {
		return nil, runtimeErr("claim_context_receipt_invalid")
	}
	return &envelope, nil
}

func claimContextReceiptPolicy(job, prepared map[string]any) error {
	if err := privateCapturePolicy("capture_policy_wait", sstr(job["cwd"])); err != nil {
		return err
	}
	if privatePayload(prepared) || privatePayload(job["users"]) {
		return sessionWait("capture_private_prepared")
	}
	return nil
}

func retainClaimContextReceipt(job, prepared map[string]any, cued []any) error {
	value := prepared["context_rejections"]
	if value == nil {
		return nil
	}
	policy := func() error { return claimContextReceiptPolicy(job, prepared) }
	if err := policy(); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return runtimeErr("claim_context_receipt_invalid")
	}
	envelope, err := decodeClaimContextEnvelope(raw)
	if err != nil {
		return err
	}
	input, inputErr := claimContextInputDigest(cued)
	if inputErr != nil || envelope.InputRowsSHA256 != input || envelope.ScopeSHA256 != sdigest(capScope()) || envelope.EventSHA256 != sdigest(job["event_id"]) {
		return runtimeErr("claim_context_receipt_binding_changed")
	}
	texts := make([]string, len(cued))
	for index, user := range cued {
		text, ok := autosaveGet(user, "text").(string)
		if !ok {
			return runtimeErr("claim_context_receipt_binding_changed")
		}
		texts[index] = text
	}
	if envelope.Receipt.UserInputSHA256 != sha256Hex([]byte(strings.Join(texts, "\n"))) {
		return runtimeErr("claim_context_receipt_binding_changed")
	}
	// Canonicalize typed and JSON-reloaded prepared values identically on retry.
	raw, err = json.Marshal(envelope)
	if err != nil {
		return runtimeErr("claim_context_receipt_invalid")
	}
	return storeClaimContextReceipt(raw, policy)
}

func claimReceiptDir() (string, error) {
	dir := filepath.Join(capState, "claim-context-rejections")
	if !resolved(capState) {
		return "", runtimeErr("claim_context_receipt_directory_invalid")
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return dir, nil
	}
	if err != nil || !info.IsDir() || !resolved(dir) {
		return "", runtimeErr("claim_context_receipt_directory_invalid")
	}
	return dir, nil
}

type storedClaimReceipt struct {
	path string
	info os.FileInfo
}

func readStoredClaimReceipt(path string, info os.FileInfo) ([]byte, error) {
	if !info.Mode().IsRegular() || info.Size() > claimReceiptBytes || !resolved(path) {
		return nil, runtimeErr("claim_context_receipt_file_invalid")
	}
	file, err := openNoFollow(path)
	if err != nil {
		return nil, runtimeErr("claim_context_receipt_read_failed")
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !os.SameFile(info, current) || !current.Mode().IsRegular() || current.Size() > claimReceiptBytes {
		return nil, runtimeErr("claim_context_receipt_file_invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, claimReceiptBytes+1))
	if err != nil || len(raw) > claimReceiptBytes {
		return nil, runtimeErr("claim_context_receipt_read_failed")
	}
	return raw, nil
}

func managedClaimReceipts(dir string) ([]storedClaimReceipt, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, runtimeErr("claim_context_receipt_read_failed")
	}
	managed := []storedClaimReceipt{}
	for _, entry := range entries {
		name := entry.Name()
		digest := claimReceiptNameDigest(name)
		if digest == "" {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, runtimeErr("claim_context_receipt_read_failed")
		}
		raw, err := readStoredClaimReceipt(path, info)
		if err != nil {
			return nil, err
		}
		if sha256Hex(raw) != digest {
			if strings.HasPrefix(name, ".claim-context-") {
				return nil, runtimeErr("claim_context_receipt_temporary_unknown")
			}
			continue // Foreign ordinary files are never retention targets.
		}
		if _, err := decodeClaimContextEnvelope(raw); err == nil {
			managed = append(managed, storedClaimReceipt{path, info})
		}
	}
	slices.SortFunc(managed, func(a, b storedClaimReceipt) int {
		if order := a.info.ModTime().Compare(b.info.ModTime()); order != 0 {
			return order
		}
		return strings.Compare(a.path, b.path)
	})
	return managed, nil
}

func removeClaimReceipt(entry storedClaimReceipt, policy func() error) error {
	if err := policy(); err != nil {
		return err
	}
	if _, err := claimReceiptDir(); err != nil {
		return err
	}
	info, err := os.Lstat(entry.path)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, entry.info) || !resolved(entry.path) {
		return runtimeErr("claim_context_receipt_file_changed")
	}
	raw, err := readStoredClaimReceipt(entry.path, info)
	if err != nil || sha256Hex(raw) != claimReceiptNameDigest(filepath.Base(entry.path)) {
		return runtimeErr("claim_context_receipt_file_changed")
	}
	if _, err := decodeClaimContextEnvelope(raw); err != nil {
		return runtimeErr("claim_context_receipt_file_changed")
	}
	if err := policy(); err != nil {
		return err
	}
	if err := os.Remove(entry.path); err != nil {
		return runtimeErr("claim_context_receipt_cleanup_failed")
	}
	return nil
}

func trimClaimReceipts(entries []storedClaimReceipt, target string, size int, policy func() error) error {
	// Reserve the final receipt plus its one exclusive temporary record.
	// Valid orphan temporaries count toward the same 256-record/4-MiB limit.
	count, total := 2, 2*size
	for _, entry := range entries {
		if entry.path != target {
			count++
			total += int(entry.info.Size())
		}
	}
	now := time.UnixMilli(int64(timeNow() * 1000))
	for _, entry := range entries {
		expired := !entry.info.ModTime().Add(claimReceiptRetention).After(now)
		if !expired && (entry.path == target || count <= claimReceiptRecords && total <= claimReceiptTotalBytes) {
			continue
		}
		if err := removeClaimReceipt(entry, policy); err != nil {
			return err
		}
		if entry.path != target {
			count--
			total -= int(entry.info.Size())
		}
	}
	return nil
}

// Only content-bound final files or our exclusive temporary naming scheme are
// managed. Foreign files (including the old predictable .tmp name) stay intact.
func claimReceiptNameDigest(name string) string {
	if strings.HasSuffix(name, ".json") && claimReceiptHash(strings.TrimSuffix(name, ".json")) {
		return strings.TrimSuffix(name, ".json")
	}
	if !strings.HasPrefix(name, ".claim-context-") || !strings.HasSuffix(name, ".tmp") {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, ".claim-context-"), ".tmp"), "-")
	if len(parts) != 2 || !claimReceiptHash(parts[0]) || len(parts[1]) < 1 || len(parts[1]) > 20 {
		return ""
	}
	for _, ch := range parts[1] {
		if ch < '0' || ch > '9' {
			return ""
		}
	}
	return parts[0]
}

func claimReceiptDirectoryCurrent(dir string, original os.FileInfo) error {
	current, err := os.Lstat(dir)
	if err != nil || !current.IsDir() || !os.SameFile(original, current) || !resolved(dir) {
		return runtimeErr("claim_context_receipt_directory_invalid")
	}
	return nil
}

func removeClaimReceiptTemp(path string, original os.FileInfo, dirInfo os.FileInfo, policy func() error) error {
	if err := policy(); err != nil {
		return err
	} // A pause also forbids cleanup writes.
	if err := claimReceiptDirectoryCurrent(filepath.Dir(path), dirInfo); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(original, current) || !resolved(path) {
		return runtimeErr("claim_context_receipt_temporary_changed")
	}
	if err := policy(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return runtimeErr("claim_context_receipt_cleanup_failed")
	}
	return nil
}

func publishClaimReceipt(path string, raw []byte, policy func() error) (result error) {
	dir := filepath.Dir(path)
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || !resolved(dir) {
		return runtimeErr("claim_context_receipt_directory_invalid")
	}
	if err := policy(); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".claim-context-"+sha256Hex(raw)+"-*.tmp")
	if err != nil {
		return runtimeErr("claim_context_receipt_write_failed")
	}
	owned, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return runtimeErr("claim_context_receipt_temporary_unknown")
	}
	defer func() { result = errors.Join(result, removeClaimReceiptTemp(file.Name(), owned, dirInfo, policy)) }()
	// The policy check immediately before CreateTemp covers this single write.
	// A later pause leaves a complete, content-bound temporary for bounded
	// retention after capture is resumed, rather than deleting during a pause.
	writeErr := func() error {
		if _, err := file.Write(raw); err != nil {
			return runtimeErr("claim_context_receipt_write_failed")
		}
		if err := file.Sync(); err != nil {
			return runtimeErr("claim_context_receipt_write_failed")
		}
		return nil
	}()
	closeErr := file.Close()
	if writeErr != nil {
		if closeErr != nil {
			return errors.Join(writeErr, runtimeErr("claim_context_receipt_close_failed"))
		}
		return writeErr
	}
	if closeErr != nil {
		return runtimeErr("claim_context_receipt_write_failed")
	}
	if err := policy(); err != nil {
		return err
	}
	if err := claimReceiptDirectoryCurrent(dir, dirInfo); err != nil {
		return err
	}
	current, err := os.Lstat(file.Name())
	if err != nil || !os.SameFile(owned, current) {
		return runtimeErr("claim_context_receipt_temporary_changed")
	}
	actual, err := readStoredClaimReceipt(file.Name(), current)
	if err != nil || !bytes.Equal(raw, actual) {
		return runtimeErr("claim_context_receipt_temporary_changed")
	}
	if err := policy(); err != nil {
		return err
	}
	if err := claimReceiptDirectoryCurrent(dir, dirInfo); err != nil {
		return err
	}
	// Same-directory hard-link publication is atomic and never replaces a foreign
	// destination. Unsupported filesystems fail closed rather than overwrite it.
	if err := os.Link(file.Name(), path); err != nil {
		current, statErr := os.Lstat(path)
		if statErr != nil {
			return runtimeErr("claim_context_receipt_write_failed")
		}
		actual, readErr := readStoredClaimReceipt(path, current)
		if readErr != nil || !bytes.Equal(raw, actual) {
			return runtimeErr("claim_context_receipt_file_changed")
		}
	}
	return nil
}

func writeClaimReceipt(path string, raw []byte, policy func() error) error {
	if err := policy(); err != nil {
		return err
	}
	if _, err := claimReceiptDir(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil {
		existing, err := readStoredClaimReceipt(path, info)
		if err != nil || !bytes.Equal(existing, raw) {
			return runtimeErr("claim_context_receipt_file_changed")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return runtimeErr("claim_context_receipt_write_failed")
	}
	return publishClaimReceipt(path, raw, policy)
}

func storeClaimContextReceipt(raw []byte, policy func() error) error {
	if err := policy(); err != nil {
		return err
	}
	if _, err := decodeClaimContextEnvelope(raw); err != nil {
		return err
	}
	dir, err := claimReceiptDir()
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return runtimeErr("claim_context_receipt_write_failed")
	}
	if _, err := claimReceiptDir(); err != nil {
		return err
	}
	entries, err := managedClaimReceipts(dir)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, sha256Hex(raw)+".json")
	if err := trimClaimReceipts(entries, path, len(raw), policy); err != nil {
		return err
	}
	return writeClaimReceipt(path, raw, policy)
}
