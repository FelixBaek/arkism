package capture

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Bind a selected server JSONL session to the durable work writer.

// handoffUUID is uuid.uuid4().hex.
func handoffUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b)
}

// handoffRoot is STATE/work-handoff.
func handoffRoot() string { return filepath.Join(capState, "work-handoff") }

// handoffOrigin reports whether origin can hand off work.
func handoffOrigin(origin any) bool {
	return origin == "claude_code" || origin == "codex" || origin == "zcode"
}

// handoffDump is json.dumps(v, ensure_ascii=False).
func handoffDump(v any) string { return host.MustDumps(v, host.DumpOpts{NoASCII: true}) }

// handoffLoad reads a JSON file keeping key order.
func handoffLoad(path string) (host.Obj, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := loadsOrdered(raw)
	if err != nil {
		return nil, err
	}
	o, ok := v.(host.Obj)
	if !ok {
		panic("work handoff state is not an object")
	}
	return o, nil
}

// handoffResolve is str(Path(p).resolve(strict=True)).
func handoffResolve(p string) (string, error) { return filepath.EvalSymlinks(absPath(p)) }

// handoffContext identifies the hook's server session, or nil when it cannot hand off.
func handoffContext(hook map[string]any, origin string) (value host.Obj, err error) {
	defer recovered(&err)
	if origin == "" {
		origin = capOrigin
	}
	if !handoffOrigin(origin) || client() != "server" {
		return nil, nil
	}
	path, sid, cwd := hook["transcript_path"], hook["session_id"], hook["cwd"]
	s, _ := sid.(string)
	c, _ := cwd.(string)
	if s == "" || c == "" || capAgentOff(origin) {
		return nil, nil
	}
	resolvedCwd, err := handoffResolve(c)
	if origin == "zcode" {
		if err != nil {
			return nil, err
		}
		value = host.Obj{{Key: "path", Value: absPath(capZcodeDB)}, {Key: "device", Value: "server"}, {Key: "origin", Value: "zcode"},
			{Key: "logical", Value: "db-session"}, {Key: "session_id", Value: s}, {Key: "cwd", Value: resolvedCwd},
			{Key: "archive_id", Value: archiveIdentity("server", "zcode", s, "db-session")}}
		err := zcodeSnapshot(absPath(capZcodeDB), s, func(snapshot string, _ float64, _ any) error {
			_, _, err := wzcodeWindow(snapshot, s, value[5].Value, nil, true, false)
			return err
		})
		if err != nil {
			return nil, err
		}
		return value, nil
	}
	root := capProjects
	if origin == "codex" {
		root = capCodexSessions
	}
	if p, _ := path.(string); p == "" && origin == "codex" {
		var matches []string
		for _, candidate := range globAll(root, "*.jsonl") {
			if found := evidenceFileContext(candidate, root, s, "server", true, "codex"); found != nil {
				fp, _ := found.Get("path")
				matches = append(matches, fp.(string))
				if len(matches) > 1 {
					return nil, nil
				}
			}
		}
		if len(matches) != 1 {
			return nil, nil
		}
		path = matches[0]
	}
	p, _ := path.(string)
	if p == "" {
		return nil, nil
	}
	value = evidenceFileContext(p, root, s, "server", true, origin)
	if value != nil && origin == "codex" {
		ok, err := handoffCodexMeta(p, s, c)
		if err != nil || !ok {
			return nil, err
		}
	}
	if value != nil {
		if err != nil {
			return nil, err
		}
		value.Set("cwd", resolvedCwd)
	}
	return value, nil
}

// handoffCodexMeta accepts only an interactive Codex session started in cwd.
// Opening and reading fail loudly, as Python did outside its try block.
func handoffCodexMeta(path, sid, cwd string) (ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	raw, err := readLine(bufio.NewReader(f), 2*archiveChunk+1)
	if err != nil {
		return false, err
	}
	defer func() {
		if recover() != nil {
			ok, err = false, nil
		}
	}()
	record, err := loadsBytes(raw)
	if err != nil {
		return false, nil
	}
	meta := autosaveItem(record, "payload")
	_, nested := autosaveGet(meta, "source").(map[string]any)
	mcwd, isStr := autosaveGet(meta, "cwd").(string)
	if autosaveGet(record, "type") != "session_meta" || autosaveGet(meta, "id") != sid ||
		autosaveGet(meta, "originator") == "codex_exec" || nested || !isStr {
		return false, nil
	}
	a, err := filepath.EvalSymlinks(absPath(mcwd))
	if err != nil {
		return false, nil
	}
	b, err := filepath.EvalSymlinks(absPath(cwd))
	return err == nil && a == b, nil
}

// handoffKeyFor names a session's binding: sha256 of [scope, context] with sorted keys.
func handoffKeyFor(value any) string {
	return sha256Hex([]byte(host.MustDumps([]any{capScope(), value}, host.DumpOpts{SortKeys: true})))
}

// handoffLocked runs fn holding STATE/work-handoff/.lock without waiting. Never nest it:
// a second open of the lock file conflicts with the first.
func handoffLocked(fn func(root string) error) error {
	root := handoffRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(root, ".lock"), false)
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn(root)
}

// handoffPrefix hashes the first size bytes of path, which must end a line.
func handoffPrefix(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	digest := sha256.New()
	remaining := size
	last := byte('\n')
	buf := make([]byte, 1<<20)
	for remaining > 0 {
		n, err := f.Read(buf[:min(int64(len(buf)), remaining)])
		if n == 0 {
			if err != nil && err != io.EOF {
				return "", err
			}
			return "", valueErr("work_transcript_changed")
		}
		digest.Write(buf[:n])
		last = buf[n-1]
		remaining -= int64(n)
	}
	if last != '\n' {
		return "", valueErr("work_transcript_pending")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// handoffInt reads a JSON integer offset.
func handoffInt(v any) int64 {
	f, ok := num(v)
	if !ok {
		panic("not a number")
	}
	return int64(f)
}

// handoffBind records the selected work and the transcript prefix it already covers.
func handoffBind(hook map[string]any, rendered string) (err error) {
	defer recovered(&err)
	value, err := handoffContext(hook, "")
	if err != nil || value == nil || rendered == "" {
		return err
	}
	parsed, err := loads([]byte(rendered[min(len(host.HookHeader), len(rendered)):]))
	if err != nil {
		return err
	}
	id := autosaveItem(parsed, "id").(string)
	_, isInt := host.Int(autosaveGet(parsed, "revision"))
	if !autosaveKey.MatchString(id) || !isInt {
		return valueErr("work_binding_invalid")
	}
	revision := autosaveGet(parsed, "revision")
	key := handoffKeyFor(value)
	return handoffLocked(func(root string) (err error) {
		defer recovered(&err)
		path := filepath.Join(root, key+".binding.json")
		if exists(path) {
			old, err := handoffLoad(path)
			if err != nil {
				return err
			}
			// Never silently rebase a local session onto a concurrent writer.
			if len(old) > 0 && autosaveItem(old, "id") == id && handoffActive(old) {
				return nil
			}
		}
		pending := filepath.Join(root, key+".pending.json")
		if exists(pending) {
			if err := os.Rename(pending, filepath.Join(root, key+"."+handoffUUID()+".cancelled.json")); err != nil {
				return err
			}
		}
		var records any
		var offset int64
		var digest string
		vpath := autosaveItem(value, "path").(string)
		vsid := autosaveItem(value, "session_id").(string)
		if autosaveItem(value, "origin") == "zcode" {
			err := zcodeSnapshot(vpath, vsid, func(snapshot string, _ float64, _ any) error {
				r, _, err := wzcodeWindow(snapshot, vsid, autosaveItem(value, "cwd"), nil, false, false)
				records = r
				return err
			})
			if err != nil {
				return err
			}
			digest = wzcodeDigest(records)
		} else {
			before, err := archiveSignature(vpath)
			if err != nil {
				return err
			}
			if digest, err = handoffPrefix(vpath, before[1].(int64)); err != nil {
				return err
			}
			after, err := archiveSignature(vpath)
			if err != nil {
				return err
			}
			if !eq(after, before) {
				return valueErr("work_transcript_pending")
			}
			offset = before[1].(int64)
		}
		mode := "folder"
		if truthy(hook["arkism_work_id"]) {
			mode = "hook"
		} else if os.Getenv("ARKISM_WORK_ID") != "" {
			mode = "env"
		}
		var stamp any
		if mode == "folder" {
			if stamp, err = handoffSelectionStamp(autosaveItem(value, "cwd")); err != nil {
				return err
			}
		}
		return atomicWrite(path, handoffDump(host.Obj{{Key: "id", Value: id}, {Key: "expected_revision", Value: revision},
			{Key: "scope", Value: capScope()}, {Key: "context", Value: value}, {Key: "offset", Value: offset},
			{Key: "prefix_sha256", Value: digest}, {Key: "records", Value: records}, {Key: "epoch", Value: handoffUUID()},
			{Key: "selection_mode", Value: mode}, {Key: "selection_stamp", Value: stamp}}))
	})
}

// handoffSelectionStamp is [st_ino, st_mtime_ns] of the checkout's selection file.
func handoffSelectionStamp(cwd any) (any, error) {
	_, path, err := selectionPath(cwd)
	if err != nil {
		return nil, err
	}
	st, err := pathStamp(path, true)
	if err != nil {
		return nil, err
	}
	return []any{int64(st.Ino), st.MtimeNs}, nil
}

// handoffSuspendBound stops a binding and cancels its pending checkpoint.
func handoffSuspendBound(root, key string, binding host.Obj) error {
	binding.Set("suspended", true)
	binding.Set("epoch", handoffUUID())
	if err := atomicWrite(filepath.Join(root, key+".binding.json"), handoffDump(binding)); err != nil {
		return err
	}
	pending := filepath.Join(root, key+".pending.json")
	if exists(pending) {
		return os.Rename(pending, filepath.Join(root, key+"."+handoffUUID()+".cancelled.json"))
	}
	return nil
}

// handoffSuspend stops the hook session's binding, if any.
func handoffSuspend(hook map[string]any) (err error) {
	defer recovered(&err)
	value, err := handoffContext(hook, "")
	if err != nil || value == nil || !exists(handoffRoot()) {
		return err
	}
	key := handoffKeyFor(value)
	return handoffLocked(func(root string) error {
		path := filepath.Join(root, key+".binding.json")
		if !exists(path) {
			return nil
		}
		binding, err := handoffLoad(path)
		if err != nil {
			return err
		}
		return handoffSuspendBound(root, key, binding)
	})
}

// handoffActive reports whether binding still names the selected work; it panics on a
// malformed binding, as Python raised KeyError.
func handoffActive(binding host.Obj) bool {
	origin := autosaveItem(autosaveItem(binding, "context"), "origin")
	if !handoffOrigin(origin) || capAgentOff(origin.(string)) || truthy(autosaveGet(binding, "suspended")) ||
		!eq(autosaveItem(binding, "scope"), capScope()) {
		return false
	}
	switch autosaveItem(binding, "selection_mode") {
	case "folder":
		cwd := autosaveItem(autosaveItem(binding, "context"), "cwd")
		stamp, err := handoffSelectionStamp(cwd)
		if err != nil {
			return false
		}
		return selectionSelected(cwd) == autosaveItem(binding, "id") && eq(stamp, autosaveGet(binding, "selection_stamp"))
	case "env":
		return os.Getenv("ARKISM_WORK_ID") == autosaveItem(binding, "id")
	}
	return true
}

// handoffQueueBinding writes a checkpoint job when the session grew past the binding.
func handoffQueueBinding(root, key string, binding host.Obj) error {
	path := filepath.Join(root, key+".pending.json")
	if exists(path) || !handoffActive(binding) {
		return nil
	}
	value := autosaveItem(binding, "context")
	vpath := autosaveItem(value, "path").(string)
	if autosaveItem(value, "origin") == "zcode" {
		sid := autosaveItem(value, "session_id").(string)
		same := false
		err := zcodeSnapshot(vpath, sid, func(snapshot string, _ float64, _ any) error {
			records, _, err := wzcodeWindow(snapshot, sid, autosaveItem(value, "cwd"), autosaveItem(binding, "records"), true, false)
			same = err == nil && eq(records, autosaveItem(binding, "records"))
			return err
		})
		if err != nil || same {
			return err
		}
	} else {
		fi, err := os.Stat(vpath)
		if err != nil {
			return err
		}
		if fi.Size() <= handoffInt(autosaveItem(binding, "offset")) {
			return nil
		}
	}
	return atomicWrite(path, handoffDump(host.Obj{{Key: "type", Value: "work_checkpoint"}, {Key: "id", Value: autosaveItem(binding, "id")},
		{Key: "scope", Value: autosaveItem(binding, "scope")}, {Key: "expected_revision", Value: autosaveItem(binding, "expected_revision")},
		{Key: "event_id", Value: "auto-" + handoffUUID()}, {Key: "archive_id", Value: autosaveItem(value, "archive_id")},
		{Key: "start_byte", Value: autosaveItem(binding, "offset")}, {Key: "context", Value: value},
		{Key: "epoch", Value: autosaveItem(binding, "epoch")}, {Key: "created_at", Value: timeNow()}}))
}

// handoffQueue queues a checkpoint for the hook session, or suspends it when the
// selected work changed.
func handoffQueue(hook map[string]any) (err error) {
	defer recovered(&err)
	value, err := handoffContext(hook, "")
	if err != nil || value == nil {
		return err
	}
	key := handoffKeyFor(value)
	return handoffLocked(func(root string) (err error) {
		defer recovered(&err)
		path := filepath.Join(root, key+".binding.json")
		if !exists(path) {
			return nil
		}
		binding, err := handoffLoad(path)
		if err != nil {
			return err
		}
		selected, err := selectedWorkID(hook)
		if err != nil {
			return err
		}
		if selected != autosaveItem(binding, "id") && (selected != "" || autosaveItem(binding, "selection_mode") != "hook") {
			return handoffSuspendBound(root, key, binding)
		}
		return handoffQueueBinding(root, key, binding)
	})
}

// handoffChecked reloads the binding and requires it to still own job.
func handoffChecked(root, key string, job host.Obj) (host.Obj, error) {
	binding, err := handoffLoad(filepath.Join(root, key+".binding.json"))
	if err != nil {
		return nil, err
	}
	if autosaveItem(binding, "epoch") != autosaveItem(job, "epoch") || autosaveItem(binding, "id") != autosaveItem(job, "id") || !handoffActive(binding) {
		return nil, valueErr("work_selection_changed")
	}
	return binding, nil
}

// handoffProcess archives the session up to now and saves it into the bound work.
func handoffProcess(root, key string, job *host.Obj) (err error) {
	defer recovered(&err)
	if truthy(autosaveGet(*job, "owner_recovery")) {
		return valueErr("work_recovery_pending")
	}
	path := filepath.Join(root, key+".pending.json")
	var binding host.Obj
	err = handoffLocked(func(root string) (err error) {
		defer recovered(&err)
		binding, err = handoffChecked(root, key, *job)
		return err
	})
	if err != nil {
		return err
	}
	persist := func() error {
		return handoffLocked(func(root string) (err error) {
			defer recovered(&err)
			if _, err := handoffChecked(root, key, *job); err != nil {
				return err
			}
			return atomicWrite(path, handoffDump(*job))
		})
	}
	if !autosaveHas(*job, "prepared") && !autosaveHas(*job, "receipt") {
		value := autosaveItem(*job, "context")
		at := func(k string) any { return autosaveItem(value, k) }
		current, err := handoffContext(map[string]any{"transcript_path": at("path"), "session_id": at("session_id"), "cwd": at("cwd")}, at("origin").(string))
		if err != nil {
			return err
		}
		if !eq(current, value) {
			return valueErr("work_transcript_changed")
		}
		vpath, sid := at("path").(string), at("session_id").(string)
		client := evidenceClient{capPost, capScope()}
		if at("origin") == "zcode" {
			err := zcodeSnapshot(vpath, sid, func(snapshot string, observed float64, revision any) error {
				records, eligible, err := wzcodeWindow(snapshot, sid, at("cwd"), autosaveItem(binding, "records"), false, false)
				if err != nil {
					return err
				}
				collector := newCollector(client, nil, true)
				if err := collector.upload(snapshot, at("device").(string), "zcode", sid, "db-session", observed, "reconstructed_zcode", revision); err != nil {
					return err
				}
				saved, has := collector.state.Get(at("archive_id").(string))
				if collector.counts.n["committed"] == 0 || !has || !truthy(saved) {
					return valueErr("work_transcript_pending")
				}
				job.Set("sha256", autosaveItem(saved, "sha256"))
				job.Set("end_byte", 0)
				job.Set("end_records", records)
				job.Set("eligible_record_keys", eligible)
				return nil
			})
			if err != nil {
				return err
			}
		} else {
			got, err := handoffPrefix(vpath, handoffInt(autosaveItem(binding, "offset")))
			if err != nil {
				return err
			}
			if got != autosaveItem(binding, "prefix_sha256") {
				return valueErr("work_transcript_changed")
			}
			collector := newCollector(client, nil, true)
			if err := collector.file(vpath, at("device").(string), at("origin").(string), at("logical").(string), sid, -1); err != nil {
				return err
			}
			saved, has := collector.state.Get(at("archive_id").(string))
			if collector.counts.n["committed"] == 0 || !has || !truthy(saved) {
				return valueErr("work_transcript_pending")
			}
			end := handoffInt(autosaveItem(saved, "signature").([]any)[1])
			endHash, err := handoffPrefix(vpath, end)
			if err != nil {
				return err
			}
			startHash, err := handoffPrefix(vpath, handoffInt(autosaveItem(binding, "offset")))
			if err != nil {
				return err
			}
			if endHash != autosaveItem(saved, "sha256") || startHash != autosaveItem(binding, "prefix_sha256") {
				return valueErr("work_transcript_changed")
			}
			job.Set("sha256", autosaveItem(saved, "sha256"))
			job.Set("end_byte", end)
		}
		if err := persist(); err != nil {
			return err
		}
	}
	request := func(endpoint string, body any) (map[string]any, error) {
		// Check the binding immediately before dispatch; never write a different work ID.
		if strings.HasSuffix(endpoint, "/save") {
			err := handoffLocked(func(root string) (err error) {
				defer recovered(&err)
				_, err = handoffChecked(root, key, *job)
				return err
			})
			if err != nil {
				return nil, err
			}
		}
		return capPost(endpoint, body, 60*time.Second)
	}
	result, err := autosaveProcess(job, request, capScope(), autosaveExtract, persist)
	if err != nil {
		return err
	}
	return handoffLocked(func(root string) (err error) {
		defer recovered(&err)
		binding, err := handoffChecked(root, key, *job)
		if err != nil {
			return err
		}
		b := func(k string) any { return autosaveItem(binding, k) }
		j := func(k string) any { return autosaveItem(*job, k) }
		revision := autosaveItem(result, "revision")
		applied := eq(b("expected_revision"), revision) && eq(b("offset"), j("end_byte")) && eq(b("prefix_sha256"), j("sha256"))
		if !applied && (!eq(b("expected_revision"), j("expected_revision")) || !eq(b("offset"), j("start_byte"))) {
			return valueErr("work_binding_changed")
		}
		binding.Set("expected_revision", revision)
		binding.Set("offset", j("end_byte"))
		binding.Set("prefix_sha256", j("sha256"))
		if autosaveItem(autosaveItem(binding, "context"), "origin") == "zcode" {
			binding.Set("records", j("end_records"))
		}
		if err := atomicWrite(filepath.Join(root, key+".binding.json"), handoffDump(binding)); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		return handoffQueueBinding(root, key, binding)
	})
}

// handoffCode is the metric code of a failure: a work_* ValueError text or the Python type.
func handoffCode(err error) string {
	var missing zcodeMissing
	switch {
	case errors.As(err, &missing):
		return "ZcodeSessionMissing"
	case errors.Is(err, errBusy):
		return "BlockingIOError"
	}
	return workCode(err)
}

// handoffGlob is sorted(root.glob(pattern)) for one folder.
func handoffGlob(root, pattern string) []string {
	paths, _ := filepath.Glob(filepath.Join(globEscape(root), pattern))
	sort.Strings(paths)
	return paths
}

// handoffDrain expires old cancelled jobs and processes due checkpoints.
func handoffDrain(allOrigins bool) error {
	if !handoffOrigin(capOrigin) || client() != "server" {
		return nil
	}
	root := handoffRoot()
	if !exists(root) {
		return nil
	}
	for _, path := range handoffGlob(root, "*.cancelled.json") {
		err := handoffLocked(func(string) (err error) {
			defer recovered(&err)
			job, err := handoffLoad(path)
			if err != nil {
				return err
			}
			created, ok := num(autosaveItem(job, "created_at"))
			if !ok {
				panic("created_at is not a number")
			}
			if created+autosaveTTL <= timeNow() {
				return os.Remove(path)
			}
			return nil
		})
		if err != nil {
			metric(kv("event", "work_handoff_failure"), kv("error", "cancelled_state_unreadable"))
		}
	}
	for _, path := range handoffGlob(root, "*.pending.json") {
		var job host.Obj
		err := func() (err error) {
			defer recovered(&err)
			if job, err = handoffLoad(path); err != nil {
				return err
			}
			ctx, has := job.Get("context")
			if !has {
				ctx = map[string]any{}
			}
			if !allOrigins && autosaveGet(ctx, "origin") != capOrigin {
				return nil
			}
			created, ok := num(autosaveItem(job, "created_at"))
			if !ok {
				panic("created_at is not a number")
			}
			if created+autosaveTTL <= timeNow() {
				err := handoffLocked(func(string) error {
					if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
						return err
					}
					return nil
				})
				if err != nil {
					return err
				}
				metric(kv("event", "work_handoff_expired"))
				return nil
			}
			if truthy(autosaveGet(job, "owner_recovery")) {
				return nil
			}
			if after, has := job.Get("retry_after"); has {
				f, ok := num(after)
				if !ok {
					panic("retry_after is not a number")
				}
				if f > timeNow() {
					return nil
				}
			}
			return handoffProcess(root, strings.TrimSuffix(filepath.Base(path), ".pending.json"), &job)
		}()
		if err == nil {
			continue
		}
		code := handoffCode(err)
		metric(kv("event", "work_handoff_failure"), kv("error", code))
		if job != nil && exists(path) {
			job.Set("retry_after", timeNow()+60)
			job.Set("last_error", code)
			// A newer hook or worker owns the path when the event changed; never overwrite it.
			handoffLocked(func(string) (err error) {
				defer recovered(&err)
				current, err := handoffLoad(path)
				if err == nil && eq(autosaveGet(current, "event_id"), autosaveItem(job, "event_id")) {
					return atomicWrite(path, handoffDump(job))
				}
				return err
			})
		}
	}
	return nil
}
