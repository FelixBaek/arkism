package capture

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Export recent Zcode sessions to the configured PC synchronization outbox.

const exportTTL = 7 * 86400

// exportRetention asks the server which old copies it no longer needs; nil means unavailable.
type exportRetention func(items []any) ([]string, error)

// exportHandleStamp is os.fstat; tests replace it.
var exportHandleStamp = handleStamp

// exportSessions copies recent sessions to outbox and removes expired copies the
// server confirms. now < 0 means the current time.
func exportSessions(database, outbox string, execute bool, now float64, retention exportRetention) (host.Obj, error) {
	outbox = absPath(outbox)
	if !resolved(outbox) {
		return nil, valueErr("zcode_export_path_alias")
	}
	if !execute {
		return exportRun(database, outbox, false, now, retention)
	}
	if err := os.MkdirAll(outbox, 0o777); err != nil {
		return nil, err
	}
	lock, err := lockFile(filepath.Join(outbox, ".export.lock"), false)
	if errors.Is(err, errBusy) {
		return host.Obj{{Key: "busy", Value: 1}}, nil
	}
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return exportRun(database, outbox, true, now, retention)
}

func exportRun(database, outbox string, execute bool, now float64, retention exportRetention) (host.Obj, error) {
	if now < 0 {
		now = timeNow()
	}
	database, outbox = absPath(database), absPath(outbox)
	counts := newCounter()
	if !resolved(database) || !resolved(outbox) {
		return nil, valueErr("zcode_export_path_alias")
	}
	sessions, err := exportRecent(database, now, counts)
	if err != nil {
		return nil, err
	}
	if execute {
		if err := os.MkdirAll(outbox, 0o777); err != nil {
			return nil, err
		}
	}
	for _, sid := range sessions {
		err := zcodeSnapshot(database, sid, func(snapshot string, observed float64, _ any) error {
			switch {
			case observed+exportTTL <= now:
				counts.inc("expired")
				return nil
			case observed > now+60:
				counts.inc("invalid_time")
				return nil
			}
			target := filepath.Join(outbox, sha256Hex([]byte(sid))+".jsonl")
			if isSymlink(target) {
				counts.inc("symlink_skipped")
				return nil
			}
			if !execute {
				counts.inc("planned")
				return nil
			}
			if isFile(target) && exportSame(snapshot, target) {
				counts.inc("unchanged")
				return nil
			}
			if err := exportCopy(snapshot, target, observed); err != nil {
				return err
			}
			counts.inc("exported")
			return nil
		})
		if err != nil {
			counts.inc("failed")
		}
	}
	// The server owns renewal decisions. A local old timestamp is only a query hint.
	type candidate struct {
		path string
		info *exportInfo
	}
	var candidates []candidate
	paths, _ := filepath.Glob(filepath.Join(globEscape(outbox), "*.jsonl"))
	for _, p := range paths {
		info, err := exportCopyInfo(p, now-exportTTL, true)
		if err != nil {
			counts.inc("cleanup_failed")
		} else if info != nil && info.observed+exportTTL <= now {
			candidates = append(candidates, candidate{p, info})
		}
	}
	for start := 0; start < len(candidates); start += 64 {
		batch := candidates[start:min(start+64, len(candidates))]
		decisions, err := func() ([]string, error) {
			if retention == nil {
				return nil, valueErr("retention_unavailable")
			}
			items := make([]any, len(batch))
			for i, c := range batch {
				items[i] = host.Obj{{Key: "session_id", Value: c.info.sid}, {Key: "sha256", Value: c.info.digest}}
			}
			d, err := retention(items)
			if err != nil {
				return nil, err
			}
			if len(d) != len(batch) {
				return nil, valueErr("retention_invalid")
			}
			for _, s := range d {
				if !retentionStates[s] {
					return nil, valueErr("retention_invalid")
				}
			}
			return d, nil
		}()
		if err != nil {
			counts.add("retention_unavailable", len(batch))
			continue
		}
		for i, c := range batch {
			if decisions[i] != "expired" {
				counts.inc("retention_" + decisions[i])
				continue
			}
			exportExpire(c.path, c.info, execute, counts)
		}
	}
	return counts.obj(), nil
}

// exportRecent lists the sessions updated within the export window; none when the database is missing.
func exportRecent(database string, now float64, counts *counter) ([]string, error) {
	var sessions []string
	if !isFile(database) {
		counts.inc("missing_database")
	} else {
		db, err := openRO(database)
		if err != nil {
			return nil, err
		}
		err = eachRow(db, "SELECT id FROM session WHERE time_updated>? ORDER BY id", []any{(now - exportTTL) * 1000}, func(_ []string, vals []any) error {
			sessions = append(sessions, str(vals[0]))
			return nil
		})
		db.Close()
		if err != nil {
			return nil, err
		}
	}
	return sessions, nil
}

// exportExpire removes a copy the server expired, unless it changed since it was listed.
func exportExpire(path string, info *exportInfo, execute bool, counts *counter) {
	now, err := exportCopyInfo(path, 0, false)
	switch {
	case err != nil:
		counts.inc("cleanup_failed")
	case now == nil || *now != *info:
		counts.inc("changed_before_cleanup")
	default:
		counts.inc("expired_copies")
		if execute {
			if err := os.Remove(path); err != nil {
				counts.inc("cleanup_failed")
			}
		}
	}
}

// exportSame is filecmp.cmp(a, b, shallow=False); unreadable files differ.
func exportSame(a, b string) bool {
	x, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	y, err := os.ReadFile(b)
	return err == nil && bytes.Equal(x, y)
}

// exportCopy atomically replaces target with snapshot, stamped with the session time.
func exportCopy(snapshot, target string, observed float64) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".zcode-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	err = func() error {
		defer tmp.Close()
		src, err := os.Open(snapshot)
		if err != nil {
			return err
		}
		defer src.Close()
		if _, err := io.Copy(tmp, src); err != nil {
			return err
		}
		return tmp.Sync()
	}()
	if err != nil {
		return err
	}
	at := exportUtime(observed)
	if err := os.Chtimes(name, at, at); err != nil {
		return err
	}
	return os.Rename(name, target)
}

// exportUtime is the time os.utime sets for a float: whole nanoseconds, rounded down.
func exportUtime(x float64) time.Time {
	sec, frac := math.Modf(x)
	ns := math.Floor(frac * 1e9)
	if ns < 0 {
		ns += 1e9
		sec--
	}
	return time.Unix(int64(sec), int64(ns))
}

// exportInfo identifies one exported copy: its session, time, content hash and stat.
type exportInfo struct {
	sid      string
	observed float64
	digest   string
	mark     fileStamp
}

// exportCopyInfo reads an exported copy's identity; nil when it is not a plain copy
// (or newer than olderThan when check is set).
func exportCopyInfo(path string, olderThan float64, check bool) (info *exportInfo, err error) {
	defer recovered(&err)
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	before, err := pathStamp(path, false)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || before.Nlink != 1 || !resolved(path) {
		return nil, nil
	}
	mark := func(s fileStamp) fileStamp { s.Nlink = 0; return s }
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := exportHandleStamp(f)
	if err != nil {
		return nil, err
	}
	// Windows path and handle stats can report different ctime values.
	// Compare identity across them, then changes within each stat source.
	first := func(s fileStamp) fileStamp { s.Nlink, s.CtimeNs = 0, 0; return s }
	if first(opened) != first(before) {
		return nil, valueErr("copy_changed")
	}
	r := bufio.NewReader(f)
	raw, err := readLine(r, 8<<20+1)
	if err != nil {
		return nil, err
	}
	sid, observed, ok, err := exportHead(path, raw)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if check && observed > olderThan {
		return nil, nil
	}
	h := sha256.New()
	h.Write(raw)
	if _, err := io.Copy(h, r); err != nil {
		return nil, err
	}
	after, err := exportHandleStamp(f)
	if err != nil {
		return nil, err
	}
	again, err := pathStamp(path, false)
	if err != nil {
		return nil, err
	}
	if mark(after) != mark(opened) || mark(again) != mark(before) {
		return nil, valueErr("copy_changed")
	}
	return &exportInfo{sid, observed, hex.EncodeToString(h.Sum(nil)), mark(before)}, nil
}

// exportHead reads the session id and time from a copy's first line; ok is false when the line
// is cut, empty or unterminated, or the session does not match the copy's file name.
func exportHead(path string, raw []byte) (sid string, observed float64, ok bool, err error) {
	if len(raw) > 8<<20 || len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return "", 0, false, nil
	}
	head, err := loadObjBytes(raw)
	if err != nil {
		return "", 0, false, err
	}
	sessionV, has := head["session"]
	if !has {
		sessionV = map[string]any{}
	}
	session, isMap := sessionV.(map[string]any)
	if !isMap {
		return "", 0, false, fmt.Errorf("%w: session", errType)
	}
	sid, isStr := session["id"].(string)
	observed, ok = sessionTimestamp(session["time_updated"])
	if !isStr || !ok || filepath.Base(path) != sha256Hex([]byte(sid))+".jsonl" {
		return "", 0, false, nil
	}
	return sid, observed, true, nil
}

// exportOutbox is the configured PC session outbox: the environment, then
// session-export.json next to this binary, then ~/.config/arkism/session-outbox.
func exportOutbox() (string, error) {
	value := os.Getenv("ARKISM_PC_SESSIONS_OUTBOX")
	config := filepath.Join(filepath.Dir(host.Executable()), "session-export.json")
	if value == "" && isFile(config) {
		raw, err := os.ReadFile(config)
		if err != nil {
			return "", err
		}
		m, err := loadObjBytes(raw)
		if err != nil {
			return "", err
		}
		if v := m["outbox"]; truthy(v) {
			s, ok := v.(string)
			if !ok {
				return "", fmt.Errorf("%w: outbox", errType)
			}
			value = s
		}
	}
	if value != "" {
		return value, nil
	}
	return filepath.Join(host.Home, ".config", "arkism", "session-outbox"), nil
}

func exportMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	outbox, err := exportOutbox()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("arkism export-zcode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	database := fs.String("database", filepath.Join(host.Home, ".zcode", "cli", "db", "db.sqlite"), "Zcode database")
	output := fs.String("output", filepath.Join(outbox, "zcode-sessions"), "export folder")
	execute := fs.Bool("execute", false, "write and clean up copies")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "arkism export-zcode: error: unrecognized arguments: %s\n", fs.Arg(0))
		return 2
	}
	counts, err := exportSessions(*database, *output, *execute, -1, retentionRemoteStates)
	if err != nil {
		fmt.Fprintln(stdout, `{"failed": 1}`)
		return 1
	}
	fmt.Fprintln(stdout, host.MustDumps(counts, host.DumpOpts{}))
	for _, k := range []string{"failed", "cleanup_failed", "retention_unavailable"} {
		if v, _ := counts.Get(k); truthy(v) {
			return 1
		}
	}
	return 0
}

func init() { host.Register("export-zcode", exportMain) }
