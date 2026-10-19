package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Expiry receipts contain no path, session id, text, quote or model response.
// They distinguish retention cleanup from a verified successful delivery.
type captureExpiryReceipt struct {
	Version    int     `json:"version"`
	CopyKey    string  `json:"copy_key"`
	ExpiredAt  float64 `json:"expired_at"`
	QueuedAt   float64 `json:"queued_at"`
	Prepared   bool    `json:"extraction_prepared"`
	Completion string  `json:"completion"`
	Removed    bool    `json:"removal_verified"`
}

func saveCaptureExpiryReceipt(root, path string, queued, now float64, prepared, removed bool) error {
	dir := filepath.Join(root, ".expiry-receipts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !resolved(dir) {
		return valueErr("capture_expiry_receipt_alias")
	}
	key := sha256Hex([]byte(filepath.Base(path) + "/" + spoolISO(queued)))
	r := captureExpiryReceipt{Version: 1, CopyKey: key, ExpiredAt: now, QueuedAt: queued, Prepared: prepared, Completion: "not_verified", Removed: removed}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(dir, key+".json"), string(raw)); err != nil {
		return err
	}
	return pruneCaptureExpiryReceipts(root, now)
}

func pruneCaptureExpiryReceipts(root string, now float64) error {
	dir := filepath.Join(root, ".expiry-receipts")
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if !resolved(dir) {
		return valueErr("capture_expiry_receipt_alias")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type owned struct {
		path string
		at   int64
	}
	entries := []owned{}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") || !sha256Hex64.MatchString(strings.TrimSuffix(file.Name(), ".json")) {
			continue
		}
		info, err := file.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		p := filepath.Join(dir, file.Name())
		if float64(info.ModTime().Unix())+spoolTTL <= now {
			if err = os.Remove(p); err != nil {
				return err
			}
			continue
		}
		entries = append(entries, owned{p, info.ModTime().UnixNano()})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].at != entries[j].at {
			return entries[i].at < entries[j].at
		}
		return entries[i].path < entries[j].path
	})
	for len(entries) > 256 {
		if err = os.Remove(entries[0].path); err != nil {
			return err
		}
		entries = entries[1:]
	}
	return nil
}
