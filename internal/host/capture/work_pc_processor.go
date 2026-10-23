package capture

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Process PC work events under capture's shared drain lock.

// processorSave writes the event state with sorted keys.
func processorSave(path string, state map[string]any) error {
	return outboxWrite(path, []byte(host.MustDumps(state, host.DumpOpts{NoASCII: true, SortKeys: true})))
}

// processorLoad reads one event state file.
func processorLoad(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadObj(raw)
}

// processorCurrent reloads path and requires entry to still head its queue.
func processorCurrent(path string, entry map[string]any, scope any) (map[string]any, error) {
	state, err := processorLoad(path)
	if err != nil {
		return nil, err
	}
	events, _ := autosaveItem(state, "events").([]any)
	if !eq(autosaveItem(state, "scope"), scope) || len(events) == 0 ||
		!eq(autosaveItem(autosaveItem(events[0], "event"), "event_id"), autosaveItem(autosaveItem(entry, "event"), "event_id")) ||
		!eq(autosaveItem(events[0], "sha256"), autosaveItem(entry, "sha256")) {
		return nil, valueErr("work_pc_event_changed")
	}
	return state, nil
}

// processorSourceRoot is the one PC JSONL folder of the event's origin.
func processorSourceRoot(event any) (string, error) {
	var roots []string
	for _, r := range archiveRoots() {
		if r.origin == autosaveItem(event, "origin") && r.device == "pc" && r.glob == "*.jsonl" {
			roots = append(roots, r.root)
		}
	}
	if len(roots) != 1 {
		return "", valueErr("work_pc_root_invalid")
	}
	return roots[0], nil
}

// processorProcess applies the head event of path: bind, suspend, or save new dialogue.
func processorProcess(path string, entry map[string]any, binding any) (err error) {
	defer recovered(&err)
	if err := privateCapturePolicy("work_capture_policy_wait", str(or(evidenceField(evidenceField(entry, "event"), "cwd"), "")),
		privateCheckpointRepository(evidenceField(evidenceField(entry, "job"), "prepared"), false)); err != nil {
		return err
	}
	job0, has := objGet(entry, "job")
	if !has {
		job0 = map[string]any{}
	}
	if truthy(autosaveGet(job0, "owner_recovery")) {
		return valueErr("work_recovery_pending")
	}
	event := autosaveItem(entry, "event")
	ev := func(k string) any { return autosaveItem(event, k) }
	if capAgentOff(ev("origin").(string)) {
		return valueErr("work_pc_agent_disabled")
	}
	scope := capScope()
	pcZcodeRoot := filepath.Join(archivePC, "zcode-sessions")
	var updated any
	switch ev("action") {
	case "suspend":
	case "bind":
		if err := processorBind(event, ev, pcZcodeRoot); err != nil {
			return err
		}
		updated = map[string]any{"id": ev("work_id"), "revision": autosaveItem(entry, "work_revision"), "boundary": ev("boundary")}
	default:
		updated, err = processorDialogue(path, entry, binding, event, ev, scope, pcZcodeRoot)
		if err != nil {
			return err
		}
	}
	return outboxLocked(filepath.Dir(path), func(string) (err error) {
		defer recovered(&err)
		state, err := processorCurrent(path, entry, scope)
		if err != nil {
			return err
		}
		state["binding"] = updated
		state["events"] = state["events"].([]any)[1:]
		return processorSave(path, state)
	})
}

// processorBind verifies the boundary a bind event starts the work at.
func processorBind(event any, ev func(string) any, pcZcodeRoot string) error {
	if ev("origin") == "zcode" {
		return pcZcodeSnapshot(pcZcodeRoot, ev("session_id").(string), func(snapshot string, _ float64, _ []any) error {
			_, _, err := pcZcodeVerify(snapshot, ev("boundary"))
			return err
		})
	}
	root, err := processorSourceRoot(event)
	if err != nil {
		return err
	}
	_, err = boundaryVerify(root, ev("boundary"))
	return err
}

// processorDialogue saves the dialogue between the binding's boundary and the event's
// and returns the updated binding.
func processorDialogue(path string, entry map[string]any, binding, event any, ev func(string) any, scope map[string]any, pcZcodeRoot string) (any, error) {
	if !truthy(binding) || !eq(autosaveItem(binding, "id"), ev("work_id")) {
		return nil, valueErr("work_pc_binding_missing")
	}
	start, end := autosaveItem(binding, "boundary"), ev("boundary")
	if err := processorCheckBoundary(ev, start, end); err != nil {
		return nil, err
	}
	var job host.Obj
	if j, has := entry["job"]; has && truthy(j) {
		job, _ = evidenceDict(j)
	}
	persist := func() error {
		return outboxLocked(filepath.Dir(path), func(string) (err error) {
			defer recovered(&err)
			state, err := processorCurrent(path, entry, scope)
			if err != nil {
				return err
			}
			state["events"].([]any)[0].(map[string]any)["job"] = job
			return processorSave(path, state)
		})
	}
	sid := ev("session_id").(string)
	client := evidenceClient{capPost, scope}
	if job == nil || !autosaveHas(job, "prepared") && !autosaveHas(job, "receipt") {
		var err error
		if ev("origin") == "zcode" {
			job, err = processorZcodeJob(pcZcodeRoot, sid, start, end, client, scope, binding, ev)
		} else {
			job, err = processorFileJob(event, sid, start, end, client, scope, binding, ev)
		}
		if err != nil {
			return nil, err
		}
		if err := persist(); err != nil {
			return nil, err
		}
	}
	request := func(endpoint string, body any) (map[string]any, error) {
		return capPost(endpoint, body, 60*time.Second)
	}
	result, err := autosaveProcess(&job, request, scope, autosaveExtract, persist)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": autosaveItem(binding, "id"), "revision": autosaveItem(result, "revision"), "boundary": end}, nil
}

// processorCheckBoundary requires the event's boundary to continue the binding's.
func processorCheckBoundary(ev func(string) any, start, end any) error {
	for _, k := range []string{"origin", "session_id", "logical"} {
		if !eq(autosaveItem(start, k), autosaveItem(end, k)) {
			return valueErr("work_pc_boundary_changed")
		}
	}
	a, err := boundaryFolder(autosaveItem(start, "cwd"))
	if err != nil {
		return err
	}
	b, err := boundaryFolder(autosaveItem(end, "cwd"))
	if err != nil {
		return err
	}
	if a != b {
		return valueErr("work_pc_boundary_changed")
	}
	if ev("origin") != "zcode" {
		so, _ := num(autosaveItem(start, "offset"))
		eo, _ := num(autosaveItem(end, "offset"))
		if eo < so {
			return valueErr("work_pc_boundary_changed")
		}
	}
	return nil
}

// processorZcodeJob uploads the Zcode session snapshot and returns its save job.
func processorZcodeJob(pcZcodeRoot, sid string, start, end any, client evidenceClient, scope map[string]any, binding any, ev func(string) any) (host.Obj, error) {
	var job host.Obj
	err := pcZcodeSnapshot(pcZcodeRoot, sid, func(snapshot string, observed float64, revision []any) error {
		keys, err := pcZcodeEligible(snapshot, start, end)
		if err != nil {
			return err
		}
		collector := newCollector(client, nil, true)
		if err := collector.upload(snapshot, "pc", "zcode", sid, "db-session", observed, "reconstructed_zcode", revision); err != nil {
			return err
		}
		aid := archiveIdentity("pc", "zcode", sid, "db-session")
		stored, has := collector.state.Get(aid)
		if collector.counts.n["committed"] == 0 || !has || !truthy(stored) {
			return valueErr("work_pc_sync_pending")
		}
		job = host.Obj{kv("scope", scope), kv("id", autosaveItem(binding, "id")), kv("event_id", ev("event_id")),
			kv("expected_revision", autosaveItem(binding, "revision")), kv("archive_id", aid),
			kv("sha256", autosaveItem(stored, "sha256")), kv("eligible_record_keys", keys)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

// processorFileJob uploads the JSONL source between start and end and returns its save job.
func processorFileJob(event any, sid string, start, end any, client evidenceClient, scope map[string]any, binding any, ev func(string) any) (host.Obj, error) {
	root, err := processorSourceRoot(event)
	if err != nil {
		return nil, err
	}
	if _, err := boundaryVerify(root, start); err != nil {
		return nil, err
	}
	source, err := boundaryVerify(root, end)
	if err != nil {
		return nil, err
	}
	logical := autosaveItem(end, "logical").(string)
	collector := newCollector(client, nil, true)
	if err := collector.file(source, "pc", ev("origin").(string), logical, sid, -1); err != nil {
		return nil, err
	}
	aid := archiveIdentity("pc", ev("origin").(string), sid, logical)
	stored, has := collector.state.Get(aid)
	if collector.counts.n["committed"] == 0 || !has || !truthy(stored) {
		return nil, valueErr("work_pc_sync_pending")
	}
	if _, err := boundaryVerify(root, start); err != nil {
		return nil, err
	}
	if _, err := boundaryVerify(root, end); err != nil {
		return nil, err
	}
	size, _ := num(autosaveItem(stored, "signature").([]any)[1])
	got, err := boundaryPrefix(source, int64(size))
	if err != nil {
		return nil, err
	}
	if got != autosaveItem(stored, "sha256") {
		return nil, valueErr("work_pc_source_changed")
	}
	return host.Obj{kv("scope", scope), kv("id", autosaveItem(binding, "id")), kv("event_id", ev("event_id")),
		kv("expected_revision", autosaveItem(binding, "revision")), kv("archive_id", aid),
		kv("sha256", autosaveItem(stored, "sha256")), kv("start_byte", autosaveItem(start, "offset")),
		kv("stop_byte", autosaveItem(end, "offset"))}, nil
}

// processorAdvanceControl drops a failed head when a later bind or suspend replaces it.
func processorAdvanceControl(state map[string]any) bool {
	events := autosaveItem(state, "events").([]any)
	if len(events) == 0 || !truthy(autosaveGet(events[0], "last_error")) {
		return false
	}
	for index := 1; index < len(events); index++ {
		if action := autosaveItem(autosaveItem(events[index], "event"), "action"); action == "bind" || action == "suspend" {
			cancelled, has := state["cancelled"]
			if !has {
				cancelled = []any{}
			}
			state["cancelled"] = append(append([]any{}, cancelled.([]any)...), events[:index]...)
			state["events"] = events[index:]
			state["binding"] = nil
			return true
		}
	}
	return false
}

// processorLive keeps the rows of list whose created_at (read by at) is within the TTL.
func processorLive(list []any, now float64, at func(any) any) []any {
	out := []any{}
	for _, row := range list {
		created, ok := num(at(row))
		if !ok {
			panic("created_at is not a number")
		}
		if created+outboxTTL > now {
			out = append(out, row)
		}
	}
	return out
}

// processorExpire drops expired events, receipts and cancelled events from state, saving
// it when any changed, and returns the live events.
func processorExpire(path string, state map[string]any, now float64) ([]any, error) {
	eventAt := func(e any) any { return autosaveItem(autosaveItem(e, "event"), "created_at") }
	events := autosaveItem(state, "events").([]any)
	receiptsAll := autosaveItem(state, "receipts").([]any)
	live := processorLive(events, now, eventAt)
	receipts := processorLive(receiptsAll, now, func(r any) any { return autosaveItem(r, "created_at") })
	oldCancelled, has := state["cancelled"]
	if !has {
		oldCancelled = []any{}
	}
	cancelled := processorLive(oldCancelled.([]any), now, eventAt)
	if len(cancelled) != len(oldCancelled.([]any)) {
		state["cancelled"] = cancelled
		if err := processorSave(path, state); err != nil {
			return nil, err
		}
	}
	if len(live) != len(events) || len(receipts) != len(receiptsAll) {
		if len(live) != len(events) {
			state["binding"] = nil
		}
		state["events"], state["receipts"] = live, receipts
		if err := processorSave(path, state); err != nil {
			return nil, err
		}
	}
	return live, nil
}

// processorDrain processes each PC event file's queue in order (up to 20 events per file).
// Failures are recorded as metrics; only a failure to record one is returned.
func processorDrain(allOrigins bool) error {
	root := filepath.Join(capState, "pc-work-events")
	if os.Getenv("ARKISM_CLIENT") != "pc" || !exists(root) {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(globEscape(root), "*.json"))
	sort.Strings(paths)
	for _, path := range paths {
		if isSymlink(path) {
			continue
		}
		for range 20 {
			var entry map[string]any
			var binding any
			stop := false
			err := outboxLocked(root, func(string) (err error) {
				defer recovered(&err)
				state, err := processorLoad(path)
				if err != nil {
					return err
				}
				if !eq(autosaveItem(state, "scope"), capScope()) {
					stop = true
					return nil
				}
				now := timeNow()
				live, err := processorExpire(path, state, now)
				if err != nil {
					return err
				}
				if len(live) == 0 {
					stop = true
					return nil
				}
				entry = live[0].(map[string]any)
				if !allOrigins && autosaveItem(autosaveItem(entry, "event"), "origin") != capOrigin {
					stop = true
					return nil
				}
				job, has := entry["job"]
				if !has {
					job = map[string]any{}
				}
				if truthy(autosaveGet(job, "owner_recovery")) {
					stop = true
					return nil
				}
				if err := privateCapturePolicy("work_capture_policy_wait", str(or(evidenceField(evidenceField(entry, "event"), "cwd"), ""))); err != nil {
					return err
				}
				if processorAdvanceControl(state) {
					if err := processorSave(path, state); err != nil {
						return err
					}
					entry = state["events"].([]any)[0].(map[string]any)
				}
				if after, has := entry["retry_after"]; has {
					f, ok := num(after)
					if !ok {
						panic("retry_after is not a number")
					}
					if f > now {
						stop = true
						return nil
					}
				}
				binding = state["binding"]
				return nil
			})
			if err == nil && stop {
				break
			}
			if err == nil {
				err = processorProcess(path, entry, binding)
			}
			if err == nil {
				continue
			}
			if capturePolicyPending(err) || privateCapturePolicy("work_capture_policy_wait", str(or(evidenceField(evidenceField(entry, "event"), "cwd"), "")), privateCheckpointRepository(evidenceField(evidenceField(entry, "job"), "prepared"), false)) != nil {
				metric(kv("event", "pc_work_pending"), kv("error", errorCode(err)))
				break
			}
			code := handoffCode(err)
			metric(kv("event", "pc_work_failure"), kv("error", code))
			if entry != nil {
				err := outboxLocked(root, func(string) (err error) {
					defer recovered(&err)
					state, err := processorCurrent(path, entry, capScope())
					if err != nil {
						return err
					}
					head := state["events"].([]any)[0].(map[string]any)
					head["last_error"] = code
					head["retry_after"] = timeNow() + 60
					return processorSave(path, state)
				})
				if err != nil {
					// Python let an exception from its except handler escape drain.
					return err
				}
			}
			break
		}
	}
	return nil
}
