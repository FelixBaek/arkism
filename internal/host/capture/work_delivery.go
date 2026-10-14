package capture

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Read-only metadata about received work saves; never returns queued dialogue.

var deliveryRootNames = []string{"arkism-capture", "arkism-capture-pc-claude_code", "arkism-capture-pc-codex", "arkism-capture-pc-zcode"}

// Scan limits; tests lower them.
var (
	deliveryMaxFiles      = 1000
	deliveryMaxBytes      = int64(1024 * 1024)
	deliveryMaxTotalBytes = 4 * int64(1024*1024)
	deliveryMaxSeconds    = 250 * time.Millisecond
)

const deliveryTTL = 7 * 86400

const deliveryNote = "서버 수신 대기의 관찰입니다. 대기 또는 확인 불가이면 최근 발언이 아직 저장되지 않았을 수 있습니다. " +
	"대기가 없어도 클라이언트 미전송·진행 중 대화의 반영을 보증하지 않습니다."

// deliveryRead reads one bounded JSON object without following a final symlink.
func deliveryRead(path string, limit int64) (map[string]any, int64, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, 0, valueErr("unreadable delivery metadata")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(raw)) > limit {
		return nil, 0, valueErr("oversized delivery metadata")
	}
	value, err := loadObjBytes(raw)
	if err != nil {
		return nil, 0, err
	}
	return value, int64(len(raw)), nil
}

// deliveryStatus counts this scope's unreceived saves of workID across the capture roots.
func deliveryStatus(workID string) (any, error) {
	root := absPath(capState)
	roots := []string{root}
	for _, name := range deliveryRootNames {
		if filepath.Base(root) == name {
			roots = nil
			for _, n := range deliveryRootNames {
				roots = append(roots, filepath.Join(filepath.Dir(root), n))
			}
			break
		}
	}
	scope := capScope()
	pending, failed, scanned := 0, 0, 0
	incomplete := false
	now := timeNow()
	deadline := time.Now().Add(deliveryMaxSeconds)
	remaining := deliveryMaxTotalBytes
	exhausted := func() bool {
		return scanned > deliveryMaxFiles || !time.Now().Before(deadline) || remaining <= 0
	}
	count := func(job, created, errText any) {
		c, isNum := num(created)
		if !isNum || !(now-deliveryTTL < c && c <= now+60) {
			panic(valueErr("invalid or expired delivery metadata"))
		}
		if !truthy(autosaveGet(job, "receipt")) {
			pending++
			if truthy(errText) || truthy(autosaveGet(job, "owner_recovery")) {
				failed++
			}
		}
	}
	scanFolder := func(directory, folder, suffix string) (err error) {
		defer recovered(&err)
		if !resolved(directory) {
			return valueErr("aliased delivery directory")
		}
		if !exists(directory) {
			return nil
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		name := regexp.MustCompile(`^[a-f0-9]{64}` + regexp.QuoteMeta(suffix) + `$`)
		for _, entry := range entries {
			scanned++
			if exhausted() {
				return valueErr("delivery scan limit")
			}
			if !name.MatchString(entry.Name()) {
				continue
			}
			value, size, err := deliveryRead(filepath.Join(directory, entry.Name()), min(deliveryMaxBytes, remaining))
			if err != nil {
				return err
			}
			remaining -= size
			if !eq(value["scope"], scope) {
				continue
			}
			if folder == "work-handoff" {
				if value["id"] == workID {
					count(value, value["created_at"], value["last_error"])
				}
				continue
			}
			events, ok := value["events"].([]any)
			if !ok {
				return valueErr("invalid delivery events")
			}
			for _, entry := range events {
				event := autosaveItem(entry, "event")
				if autosaveGet(event, "work_id") == workID {
					job, has := objGet(entry, "job")
					if !has {
						job = map[string]any{}
					}
					count(job, autosaveGet(event, "created_at"), autosaveGet(entry, "last_error"))
				}
			}
		}
		return nil
	}
outer:
	for _, base := range roots {
		for _, f := range [][2]string{{"work-handoff", ".pending.json"}, {"pc-work-events", ".json"}} {
			if scanFolder(filepath.Join(base, f[0]), f[0], f[1]) != nil {
				incomplete = true
			}
			if exhausted() {
				incomplete = true
				break outer
			}
		}
	}
	state := "no_pending_observed"
	if incomplete {
		state = "unknown"
	} else if pending > 0 {
		state = "pending"
	}
	return host.Obj{{Key: "state", Value: state}, {Key: "pending", Value: pending}, {Key: "failed", Value: failed}, {Key: "note", Value: deliveryNote}}, nil
}
