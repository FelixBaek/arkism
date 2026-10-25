package capture

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Generate durable work events from local PC hooks without storing dialogue.

// pcHookRoots are the transcript roots per origin; Zcode's is its database. Tests replace it.
var pcHookRoots = map[string]string{
	"claude_code": capProjects,
	"codex":       capCodexSessions,
	"zcode":       filepath.Join(host.Home, ".zcode", "cli", "db", "db.sqlite"),
}

// pcHookMarker captures the current boundary of the session the event names.
func pcHookMarker(origin string, event map[string]any) (map[string]any, error) {
	sid, has := event["session_id"]
	cwd, hasCwd := event["cwd"]
	if !has || !hasCwd {
		return nil, fmt.Errorf("%w: session_id", errKey)
	}
	if origin == "zcode" {
		return pcZcodeCapture(pcHookRoots[origin], sid, cwd)
	}
	root := absPath(pcHookRoots[origin])
	var logical string
	if p := event["transcript_path"]; truthy(p) {
		s, ok := p.(string)
		if !ok {
			return nil, fmt.Errorf("%w: transcript_path", errType)
		}
		rel, ok := pcHookRelative(s, root)
		if !ok {
			return nil, valueErr("work_pc_path_invalid")
		}
		logical = rel
	} else {
		var matches []string
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error { // the callback never fails
			if err != nil || p == root {
				return nil
			}
			if ok, _ := filepath.Match("*.jsonl", d.Name()); !ok {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if _, err := boundarySource(root, rel); err != nil {
				return nil
			}
			if boundaryMetadata(p, origin, sid, cwd) != nil {
				return nil
			}
			matches = append(matches, rel)
			return nil
		})
		if len(matches) != 1 {
			if len(matches) > 0 {
				return nil, valueErr("work_pc_source_ambiguous")
			}
			return nil, valueErr("work_pc_source_unavailable")
		}
		logical = matches[0]
	}
	return boundaryCapture(root, logical, origin, sid, cwd)
}

// pcHookRelative is Path(p).absolute().relative_to(root).as_posix(); ".." parts stay
// literal (Python does not collapse them) so they still fail the logical-path check.
func pcHookRelative(p, root string) (string, bool) {
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		p = wd + string(filepath.Separator) + p
	}
	var parts []string
	for _, part := range strings.FieldsFunc(filepath.ToSlash(p), func(r rune) bool { return r == '/' }) {
		if part != "." {
			parts = append(parts, part)
		}
	}
	rootParts := strings.FieldsFunc(filepath.ToSlash(root), func(r rune) bool { return r == '/' })
	if len(parts) < len(rootParts) {
		return "", false
	}
	for i, r := range rootParts {
		if parts[i] != r && !(runtime.GOOS == "windows" && strings.EqualFold(parts[i], r)) {
			return "", false
		}
	}
	rel := strings.Join(parts[len(rootParts):], "/")
	if rel == "" {
		rel = "."
	}
	return rel, true
}

// pcHookJSON is json.dumps(v, ensure_ascii=False, sort_keys=True).
func pcHookJSON(v any) []byte {
	return []byte(host.MustDumps(pyNorm(v), host.DumpOpts{NoASCII: true, SortKeys: true}))
}

// pcHookRecoverState finishes a half-written transition: an unexpired pending event is
// published and its next state kept, an expired one is dropped.
func pcHookRecoverState(path, folder string) (state map[string]any, err error) {
	defer recovered(&err)
	if isSymlink(path) {
		return nil, valueErr("work_pc_state_alias")
	}
	state = map[string]any{}
	if exists(path) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		v, err := loads(raw)
		if err != nil {
			return nil, err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, valueErr("work_pc_state_invalid")
		}
		state = m
	}
	if truthy(state["pending"]) {
		pending := state["pending"].(map[string]any)
		created, ok := num(pending["created_at"])
		if !ok {
			return nil, fmt.Errorf("%w: created_at", errType)
		}
		if created+outboxTTL > timeNow() {
			if err := outboxPublishLocked(folder, pending); err != nil {
				return nil, err
			}
			next, has := state["next_state"]
			if !has {
				return nil, fmt.Errorf("%w: next_state", errKey)
			}
			state = next.(map[string]any)
		} else {
			state = map[string]any{}
		}
		if err := outboxWrite(path, pcHookJSON(state)); err != nil {
			return nil, err
		}
	}
	return state, nil
}

// pcHookUpdated is state.get('updated_at', 0).
func pcHookUpdated(state map[string]any) (float64, error) {
	v, has := state["updated_at"]
	if !has {
		return 0, nil
	}
	f, ok := num(v)
	if !ok {
		return 0, fmt.Errorf("%w: updated_at", errType)
	}
	return f, nil
}

// pcHookRecover completes pending transitions and removes expired session states.
func pcHookRecover(root string) (host.Obj, error) {
	recovered, expired := 0, 0
	err := outboxLocked(root, func(folder string) error {
		paths, _ := filepath.Glob(filepath.Join(globEscape(folder), "session-*.json"))
		for _, path := range paths {
			var before []byte
			if !isSymlink(path) {
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				before = b
			}
			state, err := pcHookRecoverState(path, folder)
			if err != nil {
				return err
			}
			updated, err := pcHookUpdated(state)
			if err != nil {
				return err
			}
			if updated+outboxTTL <= timeNow() {
				if err := os.Remove(path); err != nil {
					return err
				}
				expired++
			} else if after, err := os.ReadFile(path); err != nil {
				return err
			} else if string(after) != string(before) {
				recovered++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return host.Obj{{Key: "recovered", Value: recovered}, {Key: "expired_sessions", Value: expired}}, nil
}

// pcHookSame is Python == for JSON values that may hold large integers.
func pcHookSame(a, b any) bool { return string(pcHookJSON(a)) == string(pcHookJSON(b)) }

// pcHookRecord turns one hook event into a bind, stop or suspend work event when the
// session's selected work or its boundary changed.
func pcHookRecord(root, origin string, event map[string]any, workID any, stamp any) error {
	if _, ok := pcHookRoots[origin]; !ok {
		return nil
	}
	sid, cwd := event["session_id"], event["cwd"]
	if !truthy(sid) || !truthy(cwd) {
		if truthy(workID) {
			return valueErr("work_pc_session_invalid")
		}
		return nil
	}
	folderKey, err := boundaryFolder(cwd)
	if err != nil {
		return err
	}
	key := sha256Hex([]byte(host.MustDumps([]any{origin, sid, folderKey}, host.DumpOpts{})))
	path := filepath.Join(root, "session-"+key+".json")
	if !truthy(workID) && !exists(path) {
		return nil
	}
	return outboxLocked(root, func(folder string) error {
		state, err := pcHookRecoverState(path, folder)
		if err != nil {
			return err
		}
		updated, err := pcHookUpdated(state)
		if err != nil {
			return err
		}
		if updated+outboxTTL <= timeNow() {
			state = map[string]any{}
		}
		action := ""
		var end any
		switch {
		case !truthy(workID):
			if truthy(state["id"]) {
				action = "suspend"
			}
		case !pcHookSame(state["id"], workID) || !pcHookSame(state["selection_stamp"], stamp):
			action = "bind"
			m, err := pcHookMarker(origin, event)
			if err != nil {
				return err
			}
			end = m
		case event["hook_event_name"] == "Stop" || event["hook_event_name"] == "SessionEnd":
			m, err := pcHookMarker(origin, event)
			if err != nil {
				return err
			}
			end = m
			if !pcHookSame(end, state["boundary"]) {
				action = "stop"
			}
		}
		if action == "" {
			return nil
		}
		signal, err := outboxPrepareLocked(folder, origin, sid, cwd, workID, action, end)
		if err != nil {
			return err
		}
		next := map[string]any{"id": workID, "selection_stamp": stamp, "boundary": end, "updated_at": signal["created_at"]}
		if err := outboxWrite(path, pcHookJSON(map[string]any{"pending": signal, "next_state": next})); err != nil {
			return err
		}
		if err := outboxPublishLocked(folder, signal); err != nil {
			return err
		}
		return outboxWrite(path, pcHookJSON(next))
	})
}
