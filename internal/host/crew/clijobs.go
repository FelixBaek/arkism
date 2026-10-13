package crew

// Background CLI status and restart-safe update receipts.

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

var errUpdateBusy = errors.New("update busy")

// updatePerform is performUpdate (a fake in tests).
var updatePerform = performUpdate

var (
	statusMu   sync.Mutex
	statusBusy = map[string]bool{}
	statusPool = make(chan struct{}, 2) // two background inspections at a time
	background sync.WaitGroup           // one-shot commands wait for these before exiting
)

func cliIdentity(executable string) []any {
	path, err := filepath.Abs(executable)
	if err != nil {
		return []any{}
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	st, err := os.Stat(path)
	if err != nil {
		return []any{}
	}
	return []any{path, st.ModTime().UnixNano(), st.Size(), int64(inode(st))}
}

// nowSeconds is time.time() written the way Python writes a float.
func nowSeconds() json.Number { return pyFloat(float64(time.Now().UnixNano()) / 1e9) }

// cliInfo is the cached inspection of a claude/codex binary; refresh starts a background update
// when the cache is missing, stale (an hour) or from another binary.
func cliInfo(cli, executable, state string, refresh bool) map[string]any {
	if cli != "claude" && cli != "codex" {
		return map[string]any{}
	}
	id := cliIdentity(executable)
	key := digest([]byte(cli + executable))
	path := filepath.Join(state, "cli-status", key+".json")
	cached := host.ReadJSONFile(path, 131072)
	valid := same(cached["identity"], id)
	age := math.Inf(1)
	if at, ok := cached["at"]; valid && ok {
		if _, isBool := at.(bool); !isBool {
			if f, ok := num(at); ok {
				age = float64(time.Now().UnixNano())/1e9 - f
			}
		}
	}
	out := map[string]any{}
	if valid && age >= 0 && age < 7200 {
		if m, ok := getOr(cached, "info", map[string]any{}).(map[string]any); ok {
			out = m
		}
	}
	if refresh && len(id) > 0 && (!valid || age >= 3600 || age < 0) {
		statusMu.Lock()
		if !statusBusy[key] {
			statusBusy[key] = true
			background.Add(1)
			go refreshCLI(cli, executable, path, key)
		}
		statusMu.Unlock()
	}
	return out
}

func refreshCLI(cli, executable, path, key string) {
	defer background.Done()
	statusPool <- struct{}{}
	defer func() {
		<-statusPool
		statusMu.Lock()
		delete(statusBusy, key)
		statusMu.Unlock()
	}()
	id := cliIdentity(executable)
	result := inspectCLI(cli, executable, true)
	if same(cliIdentity(executable), id) {
		_ = host.WriteJSONFile(path, map[string]any{"identity": id, "at": nowSeconds(), "info": result})
	}
}

var qidRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

// executeUpdate does one accepted update at most once, even after a failed report or restart.
func executeUpdate(request map[string]any, executable, state string) (map[string]any, error) {
	qid := getOr(request, "id", "")
	result := map[string]any{"id": qid, "status": "failed", "error": "invalid_request"}
	cli, _ := request["cli"].(string)
	q, ok := qid.(string)
	if !ok || !qidRe.MatchString(q) || (cli != "claude" && cli != "codex") {
		return result, nil
	}
	if request["provider"] != map[string]string{"claude": "anthropic", "codex": "openai"}[cli] {
		return result, nil
	}
	expected := map[string]any{}
	for _, k := range []string{"provider", "cli", "expected_version", "expected_via"} {
		expected[k] = request[k]
	}
	folder := filepath.Join(state, "cli-updates")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(folder, q+".json")
	lock, err := os.OpenFile(filepath.Join(folder, q+".lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	held, err := flock(lock, false)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, errUpdateBusy
	}
	failed := func(code string) map[string]any {
		return map[string]any{"id": qid, "status": "failed", "error": code}
	}
	if exists(path) {
		saved := host.ReadJSONFile(path, 131072)
		if !same(saved["request"], expected) {
			return failed("receipt_conflict"), nil
		}
		if r, ok := saved["result"].(map[string]any); ok {
			return r, nil
		}
		return failed("update_uncertain"), nil
	}
	if err := host.WriteJSONFile(path, map[string]any{"request": expected, "started": true}); err != nil {
		return nil, err
	}
	answer := updatePerform(cli, executable, expected["expected_version"], expected["expected_via"], folder)
	if answer["ok"] == true {
		note := "설치된 버전을 확인했어요. 버전은 같아요."
		if info, _ := answer["info"].(map[string]any); info["updated"] == true {
			note = "새 버전을 확인했어요."
		}
		result = map[string]any{"id": qid, "status": "done", "note": note}
	} else {
		result["error"] = getOr(answer, "error", "update_failed")
	}
	if err := host.WriteJSONFile(path, map[string]any{"request": expected, "result": result}); err != nil {
		return nil, err
	}
	// Installation identity changes invalidate the previously displayed version.
	cliInfoFn(cli, executable, state, true)
	return result, nil
}

// cliInfoFn is cliInfo (a fake in tests).
var cliInfoFn = cliInfo
