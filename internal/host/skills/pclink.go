package skills

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// PC link: which agents on the owner's PC reached this server (the PC icon
// on the skill screen). Two traces, both written on the server by what the PC calls: the capture
// hook's state folder arkism-capture-pc-<agent> (metrics + retry spool), and the skill router's
// arkism-pc-link/<agent>.json mark on every call that carries ARKISM_CLIENT=pc. The mark says "the
// link worked", not "the tool succeeded": only transport failures and raised errors count as failing.

var (
	pcOrigins    = []string{"claude_code", "codex", "zcode", "gemini", "grok", "deepseek"}
	pcState      = host.Env("ARKISM_PC_STATE", filepath.Join(host.Home, ".local", "state"))
	pcLinkDir    = filepath.Join(pcState, "arkism-pc-link")
	pcSpoolStale = 15 * 60.0 // a write that waits longer than this is "pending", not just in flight
)

const (
	pcWriteEvery = 30.0 // the router marks at most this often
	pcTailBytes  = 64 * 1024
)

// unixISO is datetime.fromtimestamp(t, utc).isoformat(timespec="milliseconds") with a Z.
func unixISO(t float64) string {
	us := math.RoundToEven(t * 1e6)
	sec := math.Floor(us / 1e6)
	return time.Unix(int64(sec), int64(us-sec*1e6)*1000).UTC().Format("2006-01-02T15:04:05.000Z")
}

// parseISO is datetime.fromisoformat(...).timestamp(); ok is false for anything it cannot read.
func parseISO(v any) (float64, bool) {
	s := strings.Replace(pyStr(v), "Z", "+00:00", 1)
	for _, layout := range []string{"2006-01-02T15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return float64(t.UnixNano()) / 1e9, true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return float64(t.UnixNano()) / 1e9, true
		}
	}
	return 0, false
}

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// pcMark is called by the skill router for a PC client. It never fails.
func pcMark(origin string, isError bool, now float64) {
	if !containsStr(pcOrigins, origin) {
		return
	}
	path := filepath.Join(pcLinkDir, origin+".json")
	old := host.ReadJSONFile(path, 1<<20)
	ok, hasOK := parseISO(old["last_ok"])
	bad, hasBad := parseISO(old["last_error"])
	// A success right after a failure must be written even inside the throttle window, or the error would stick.
	if !isError && hasOK && ok != 0 && now-ok < pcWriteEvery && !(hasBad && bad != 0 && bad >= ok) {
		return
	}
	key := "last_ok"
	if isError {
		key = "last_error"
	}
	old[key] = unixISO(now)
	if os.MkdirAll(pcLinkDir, 0o700) != nil {
		return
	}
	// Two routers marking the same origin at once must not overwrite each other's temp file.
	tmp := filepath.Join(pcLinkDir, origin+"."+strconv.Itoa(os.Getpid())+".tmp")
	if os.WriteFile(tmp, []byte(host.MustDumps(old, host.DumpOpts{})), 0o666) == nil {
		os.Rename(tmp, path)
	}
}

// pcMetrics is (last event, last success, last failure) from the tail of metrics.jsonl; 0 is none.
func pcMetrics(folder string) (last, ok, bad float64) {
	f, err := os.Open(filepath.Join(folder, "metrics.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	end, _ := f.Seek(0, io.SeekEnd)
	f.Seek(max(end-pcTailBytes, 0), io.SeekStart)
	raw, err := io.ReadAll(f)
	if err != nil {
		return
	}
	for _, line := range splitLines(decodeReplace(raw)) {
		v, err := host.Loads(line)
		row, isObj := v.(map[string]any)
		if err != nil || !isObj {
			continue
		}
		t, has := parseISO(row["ts"])
		if !has {
			continue
		}
		event := ""
		if e, ok := row["event"]; ok {
			event = pyStr(e)
		}
		last = maxNonZero(last, t)
		switch {
		case strings.HasSuffix(event, "_failure") || strings.HasSuffix(event, "_error") || event == "recall_degraded":
			bad = maxNonZero(bad, t)
		case !(event == "session_budget" || (event == "turn_completed" && row["status"] == "skipped_no_cue")):
			// Only local work that never called the server (skips, budget notes) is left out.
			ok = maxNonZero(ok, t)
		}
	}
	return
}

func maxNonZero(cur, t float64) float64 {
	if cur == 0 {
		return t
	}
	return max(cur, t)
}

// pcQueuedAt is when a write first entered the retry spool: the file name starts with the
// enqueue time in nanoseconds (drain rewrites the file on every retry, so mtime is only the last retry).
func pcQueuedAt(path string) (float64, bool) {
	head, _, _ := strings.Cut(filepath.Base(path), "-")
	if n, err := strconv.ParseInt(strings.TrimSpace(head), 10, 64); err == nil {
		return float64(n) / 1e9, true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := host.Loads(string(raw))
	doc, isObj := v.(map[string]any)
	if err != nil || !isObj {
		return 0, false
	}
	if t, ok := parseISO(doc["queued_at"]); ok && t != 0 {
		return t, true
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return float64(st.ModTime().UnixNano()) / 1e9, true
}

func pcPending(folder string, now float64) int {
	entries, err := os.ReadDir(filepath.Join(folder, "spool"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if t, ok := pcQueuedAt(filepath.Join(folder, "spool", e.Name())); ok && now-t > pcSpoolStale {
			n++
		}
	}
	return n
}

// pcReport is one row per agent that ever reached this server from the PC: {agent, last_seen, failing, pending}.
func pcReport(now float64) []any {
	out := []any{}
	for _, origin := range pcOrigins {
		folder := filepath.Join(pcState, "arkism-capture-pc-"+origin)
		last, ok, bad := pcMetrics(folder)
		mark := host.ReadJSONFile(filepath.Join(pcLinkDir, origin+".json"), 1<<20)
		markOK, _ := parseISO(mark["last_ok"])
		markBad, _ := parseISO(mark["last_error"])
		seen := max(last, markOK, markBad)
		if last == 0 && markOK == 0 && markBad == 0 {
			continue
		}
		good, broke := max(ok, markOK), max(bad, markBad)
		row := host.Obj{{Key: "agent", Value: origin}, {Key: "last_seen", Value: unixISO(seen)}, {Key: "failing", Value: broke > 0 && broke >= good}}
		if pending := pcPending(folder, now); pending > 0 {
			row.Set("pending", pending)
		}
		out = append(out, row)
	}
	return out
}
