package capture

import (
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Seven-day lifetime of managed capture transport copies, not app originals.

const (
	spoolTTL = 7 * 86400
	pcTime   = "arkism_queued_at"
)

// spoolTimestamp is an aware ISO time or a finite number (never a bool); ok false otherwise.
func spoolTimestamp(v any) (float64, bool) {
	if s, isStr := v.(string); isStr {
		ts, aware, ok := fromISO(s)
		return ts, ok && aware
	}
	if _, isBool := v.(bool); isBool {
		return 0, false
	}
	f, ok := num(v)
	if !ok || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// queuedAt validates a queue time against now (timeNow() when now < 0): never negative,
// at most 60 s ahead, and capped at now.
func queuedAt(v any, now float64) (float64, error) {
	if now < 0 {
		now = timeNow()
	}
	at, ok := spoolTimestamp(v)
	if !ok || at < 0 || at > now+60 {
		return 0, valueErr("capture_queue_time_invalid")
	}
	return min(at, now), nil
}

// cleanupSpool removes expired *.json copies directly in root; the caller owns the
// transport lock. It never recurses or follows path aliases.
func cleanupSpool(root, key string, now float64) (int, error) {
	root = absPath(root)
	if !resolved(root) {
		return 0, valueErr("capture_spool_alias")
	}
	if now < 0 {
		now = timeNow()
	}
	paths, _ := filepath.Glob(filepath.Join(globEscape(root), "*.json"))
	removed := 0
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		at := mtime(info)
		if raw, err := os.ReadFile(path); err == nil {
			if body, err := loadObjBytes(raw); err == nil {
				if v, has := body[key]; has {
					if q, err := queuedAt(v, now); err == nil {
						at = q
					} // legacy or malformed owned copies retain their original mtime
				}
			}
		} else {
			return removed, err
		}
		if at+spoolTTL <= now {
			if err := os.Remove(path); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

// spoolISO is datetime.fromtimestamp(at, utc).isoformat() with a Z.
func spoolISO(at float64) string {
	return strings.Replace(isoformat(utcFromTimestamp(at)), "+00:00", "Z", 1)
}

// mtime is st_mtime as a float.
func mtime(info os.FileInfo) float64 { return float64(info.ModTime().UnixNano()) / 1e9 }

// globEscape quotes glob metacharacters in a literal directory.
func globEscape(dir string) string {
	r := strings.NewReplacer(`*`, `\*`, `?`, `\?`, `[`, `\[`)
	if filepath.Separator == '\\' {
		r = strings.NewReplacer(`*`, `[*]`, `?`, `[?]`, `[`, `[[]`)
	}
	return r.Replace(dir)
}
