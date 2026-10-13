package capture

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/FelixBaek/arkism/internal/host"
)

// Ordered PC work events; no network or app registration is implicit.

const outboxTTL = 7 * 86400

var outboxKey = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// outboxLocked runs fn holding root's nonblocking OS lock; a held lock is an OSError.
func outboxLocked(root string, fn func(folder string) error) error {
	root = absPath(root)
	if !resolved(root) {
		return valueErr("work_pc_state_alias")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(root, ".lock")
	if isSymlink(lockPath) {
		return valueErr("work_pc_state_alias")
	}
	lock, err := lockFile(lockPath, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn(root)
}

// outboxWrite durably replaces path with data. Tests replace it to simulate a crash.
var outboxWrite = func(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".work-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func outboxUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:])
}

// outboxPrepareLocked validates one work event and reserves its sequence number.
func outboxPrepareLocked(folder, origin string, sid, cwd, workID any, action string, marker any) (map[string]any, error) {
	invalid := valueErr("work_pc_event_invalid")
	if s, ok := sid.(string); (origin != "claude_code" && origin != "codex" && origin != "zcode") || !ok || s == "" ||
		(action != "bind" && action != "stop" && action != "suspend") {
		return nil, invalid
	}
	folderKey, err := boundaryFolder(cwd)
	if err != nil {
		return nil, err
	}
	if action == "suspend" {
		if truthy(workID) || marker != nil {
			return nil, invalid
		}
	} else {
		if w, ok := workID.(string); !ok || !outboxKey.MatchString(w) {
			return nil, invalid
		}
		if err := boundaryValidate(marker); err != nil {
			return nil, err
		}
		m := marker.(map[string]any)
		if m["origin"] != origin || !eq(m["session_id"], sid) {
			return nil, invalid
		}
		mk, err := boundaryFolder(m["cwd"])
		if err != nil {
			return nil, err
		}
		if mk != folderKey {
			return nil, invalid
		}
	}
	state := filepath.Join(folder, "sequence.json")
	if isSymlink(state) {
		return nil, valueErr("work_pc_state_alias")
	}
	var sequence int64
	if exists(state) {
		raw, err := os.ReadFile(state)
		if err != nil {
			return nil, err
		}
		v, err := loads(raw)
		if err != nil {
			return nil, err
		}
		n, ok := host.Int(v)
		if !ok || n < 0 || n >= 1<<63-1 {
			return nil, valueErr("work_pc_sequence_invalid")
		}
		sequence = n
	}
	sequence++
	// A crash may leave a gap, but must never reuse a reserved sequence.
	if err := outboxWrite(state, []byte(strconv.FormatInt(sequence, 10))); err != nil {
		return nil, err
	}
	return map[string]any{"version": int64(1), "event_id": "pc-work-" + outboxUUID(), "sequence": sequence,
		"origin": origin, "session_id": sid, "cwd": cwd, "work_id": workID, "action": action,
		"created_at": timeNow(), "boundary": marker}, nil
}

// outboxRaw is json.dumps(event, ensure_ascii=False, sort_keys=True).
func outboxRaw(event any) []byte {
	return []byte(host.MustDumps(pyNorm(event), host.DumpOpts{NoASCII: true, SortKeys: true}))
}

// outboxPublishLocked writes event once; an existing different file is a conflict.
func outboxPublishLocked(folder string, event map[string]any) error {
	seq, ok := host.Int(event["sequence"])
	id, idOK := event["event_id"].(string)
	if !ok || !idOK {
		return fmt.Errorf("%w: event", errType)
	}
	path := filepath.Join(folder, fmt.Sprintf("%020d-%s.json", seq, id))
	raw := outboxRaw(event)
	if exists(path) {
		if isSymlink(path) {
			return valueErr("work_pc_event_conflict")
		}
		cur, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(cur) != string(raw) {
			return valueErr("work_pc_event_conflict")
		}
		return nil
	}
	return outboxWrite(path, raw)
}

// outboxEnqueue reserves, writes and returns one event id.
func outboxEnqueue(root, origin string, sid, cwd, workID any, action string, marker any) (string, error) {
	var id string
	err := outboxLocked(root, func(folder string) error {
		event, err := outboxPrepareLocked(folder, origin, sid, cwd, workID, action, marker)
		if err != nil {
			return err
		}
		if err := outboxPublishLocked(folder, event); err != nil {
			return err
		}
		id = event["event_id"].(string)
		return nil
	})
	return id, err
}

// outboxDrain sends queued events in order until limit acknowledgements; an event is
// removed only after a receipt for exactly its bytes. Expired events are dropped.
func outboxDrain(root string, send func(raw []byte) (any, error), limit int) (out host.Obj, err error) {
	if limit < 1 || limit > 100 {
		return nil, valueErr("work_pc_limit_invalid")
	}
	acknowledged, expired := 0, 0
	err = outboxLocked(root, func(folder string) (err error) {
		defer recovered(&err)
		paths, _ := filepath.Glob(filepath.Join(globEscape(folder), "*-pc-work-*.json"))
		for _, path := range paths {
			if isSymlink(path) {
				return valueErr("work_pc_state_alias")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			event, err := loadObjBytes(raw)
			if err != nil {
				return err
			}
			created, ok := num(event["created_at"])
			if !ok {
				return fmt.Errorf("%w: created_at", errType)
			}
			if created+outboxTTL <= timeNow() {
				if err := os.Remove(path); err != nil {
					return err
				}
				expired++
				continue
			}
			if acknowledged >= limit {
				break
			}
			reply, err := send(raw)
			if err != nil {
				return err
			}
			r, isMap := reply.(map[string]any)
			seq, seqOK := host.Int(r["sequence"])
			want, _ := host.Int(event["sequence"])
			if !isMap || r["accepted"] != true || !eq(r["event_id"], event["event_id"]) || !seqOK ||
				seq != want || r["sha256"] != sha256Hex(raw) {
				return valueErr("work_pc_unacknowledged")
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			acknowledged++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return host.Obj{{Key: "acknowledged", Value: acknowledged}, {Key: "expired", Value: expired}}, nil
}
