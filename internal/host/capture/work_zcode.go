package capture

import (
	"bufio"
	"io"
	"os"
	"path/filepath"

	"github.com/FelixBaek/arkism/internal/host"
)

// Stable, content-free work boundaries for consistent Zcode snapshots.

// wzcodeLimit caps the records one window may hold; tests lower it.
var wzcodeLimit = 10000

// wzcodeDigest is sha256 of json.dumps(value, sort_keys=True, ensure_ascii=False).
func wzcodeDigest(value any) string {
	return sha256Hex([]byte(host.MustDumps(value, host.DumpOpts{SortKeys: true, NoASCII: true})))
}

// wzcodePartKey is the content-free key of one Zcode part.
func wzcodePartKey(id string) string { return sha256Hex([]byte("arkism-zcode-part-v1\n" + id)) }

// wzcodeCompleted is Python `(data.get('time') or {}).get('completed')`.
func wzcodeCompleted(data any) bool {
	t := autosaveGet(data, "time")
	if !truthy(t) {
		return false
	}
	return truthy(autosaveGet(t, "completed"))
}

// wzcodeBeforeFinalAnchor recognizes the exact pre-Stop representation of known final
// index metadata: the row's digest before the anchor gained its final fields.
func wzcodeBeforeFinalAnchor(row map[string]any, data host.Obj) map[string]bool {
	if row["table"] != "message" || autosaveGet(data, "role") != "assistant" || !wzcodeCompleted(data) {
		return nil
	}
	anchorV, _ := data.Get("anchor")
	anchor, ok := anchorV.(host.Obj)
	if !ok || len(anchor) != 5 || !evidenceKeys(anchor, "turnId", "historyRoundCount", "orderedMessageIds", "boundaryMessageId", "goalBoundary") {
		return nil
	}
	id := autosaveItem(autosaveItem(row, "record"), "id")
	turn, _ := autosaveGet(anchor, "turnId").(string)
	rounds, isInt := host.Int(autosaveGet(anchor, "historyRoundCount"))
	ids, isList := autosaveGet(anchor, "orderedMessageIds").([]any)
	_, goalDict := autosaveGet(anchor, "goalBoundary").(host.Obj)
	if turn == "" || !isInt || rounds < 0 || !isList || len(ids) == 0 || !goalDict ||
		!eq(ids[len(ids)-1], id) || !eq(autosaveGet(anchor, "boundaryMessageId"), id) {
		return nil
	}
	for _, mid := range ids {
		if _, isStr := mid.(string); !isStr {
			return nil
		}
	}
	earlier := append(host.Obj{}, data...)
	earlier.Set("anchor", host.Obj{{Key: "turnId", Value: turn}})
	out := map[string]bool{}
	// Native JSON.stringify uses compact JSON; Python snapshot fixtures may use spaces.
	for _, compact := range []bool{true, false} {
		record := map[string]any{}
		for k, v := range row["record"].(map[string]any) {
			record[k] = v
		}
		record["data"] = host.MustDumps(earlier, host.DumpOpts{NoASCII: true, Compact: compact})
		copied := map[string]any{}
		for k, v := range row {
			copied[k] = v
		}
		copied["record"] = record
		out[wzcodeDigest(copied)] = true
	}
	return out
}

// wzcodeRecords reads a window's records (a dict of hash -> hash) as a plain map.
func wzcodeRecords(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case host.Obj:
		out := make(map[string]any, len(x))
		for _, kv := range x {
			out[kv.Key] = kv.Value
		}
		return out
	}
	return map[string]any{}
}

// wzcodePart is one part row of a Zcode snapshot window.
type wzcodePart struct {
	key    string
	record map[string]any
	data   host.Obj
}

// wzcodeWindow hashes every message and part of session sid in snapshot (records, in
// file order) and returns the keys of new real-user and completed-assistant text parts
// not in previous. History that changed since previous is an error.
func wzcodeWindow(snapshot, sid string, cwd any, previous any, allowPending, portableCwd bool) (records host.Obj, eligible []string, err error) {
	defer recovered(&err)
	f, err := os.Open(snapshot)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	first, err := r.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	head, err := loadsBytes(first)
	if err != nil {
		return nil, nil, err
	}
	autosaveGet(head, "session") // a non-dict header is an error, as in Python
	session, has := objGet(head, "session")
	if !has {
		session = map[string]any{}
	}
	same, err := wzcodeSameFolder(autosaveGet(session, "directory"), cwd, portableCwd)
	if err != nil {
		return nil, nil, err
	}
	if autosaveGet(session, "id") != sid || truthy(autosaveGet(session, "parent_id")) || !same {
		return nil, nil, valueErr("work_zcode_session_invalid")
	}
	index := map[string]string{}
	compatible := map[string]map[string]bool{}
	messages := map[string]host.Obj{}
	var parts []wzcodePart
	records = host.Obj{}
	for {
		raw, rerr := r.ReadBytes('\n')
		if len(raw) == 0 {
			if rerr != nil && rerr != io.EOF {
				return nil, nil, rerr
			}
			break
		}
		part, err := wzcodeRow(raw, sid, index, compatible, messages, &records)
		if err != nil {
			return nil, nil, err
		}
		if part != nil {
			parts = append(parts, *part)
		}
		if rerr != nil {
			break
		}
	}
	prev := wzcodeRecords(previous)
	if err := wzcodeCheckHistory(prev, index, compatible, messages, allowPending); err != nil {
		return nil, nil, err
	}
	eligible, err = wzcodeEligible(parts, messages, prev)
	if err != nil {
		return nil, nil, err
	}
	return records, eligible, nil
}

// wzcodeSameFolder reports whether the snapshot's session directory is the cwd.
func wzcodeSameFolder(directory, cwd any, portableCwd bool) (bool, error) {
	var same bool
	if portableCwd {
		a, err := boundaryFolder(directory)
		if err != nil {
			return false, err
		}
		b, err := boundaryFolder(cwd)
		if err != nil {
			return false, err
		}
		same = a == b
	} else if dir, ok := directory.(string); ok {
		a, err := filepath.EvalSymlinks(absPath(dir))
		if err != nil {
			return false, err
		}
		b, err := filepath.EvalSymlinks(absPath(cwd.(string)))
		if err != nil {
			return false, err
		}
		same = a == b
	}
	return same, nil
}

// wzcodeRow hashes one snapshot line into index and records, and returns its part
// (nil for a message or another table).
func wzcodeRow(raw []byte, sid string, index map[string]string, compatible map[string]map[string]bool, messages map[string]host.Obj, records *host.Obj) (*wzcodePart, error) {
	v, err := loadsBytes(raw)
	if err != nil {
		return nil, err
	}
	row := pyNorm(v).(map[string]any)
	table := autosaveGet(row, "table")
	if table != "message" && table != "part" {
		return nil, nil
	}
	recordV, has := row["record"]
	if !has {
		recordV = map[string]any{}
	}
	record := recordV.(map[string]any)
	id, ok := record["id"].(string)
	if !ok || id == "" {
		return nil, valueErr("work_zcode_record_invalid")
	}
	key := wzcodeDigest([]any{table, id})
	if _, dup := index[key]; dup {
		return nil, valueErr("work_zcode_record_duplicate")
	}
	index[key] = wzcodeDigest(row)
	*records = append(*records, host.KV{Key: key, Value: index[key]})
	if len(index) > wzcodeLimit {
		return nil, valueErr("work_zcode_too_large")
	}
	parsed, err := loadsOrdered([]byte(autosaveItem(record, "data").(string)))
	if err != nil {
		return nil, err
	}
	data, isObj := pyNorm(parsed).(host.Obj)
	if !isObj {
		panic("record data is not a dict")
	}
	compatible[key] = wzcodeBeforeFinalAnchor(row, data)
	if table == "message" {
		if record["session_id"] != sid {
			return nil, valueErr("work_zcode_session_invalid")
		}
		messages[id] = data
		return nil, nil
	}
	if s, has := record["session_id"]; has && s != sid {
		return nil, valueErr("work_zcode_session_invalid")
	}
	return &wzcodePart{key, record, data}, nil
}

// wzcodeCheckHistory rejects history that changed since prev and, unless allowPending,
// an assistant message that is not completed yet.
func wzcodeCheckHistory(prev map[string]any, index map[string]string, compatible map[string]map[string]bool, messages map[string]host.Obj, allowPending bool) error {
	for key, value := range prev {
		if got, has := index[key]; (!has || got != value) && !compatible[key][sstr(value)] {
			return valueErr("work_zcode_history_changed")
		}
	}
	if !allowPending {
		for _, m := range messages {
			if autosaveGet(m, "role") == "assistant" && !wzcodeCompleted(m) {
				return valueErr("work_zcode_pending")
			}
		}
	}
	return nil
}

// wzcodeEligible returns the keys of new real-user and completed-assistant text parts.
func wzcodeEligible(parts []wzcodePart, messages map[string]host.Obj, prev map[string]any) ([]string, error) {
	eligible := []string{}
	for _, p := range parts {
		mid, _ := p.record["message_id"].(string)
		message, found := messages[mid]
		if !found {
			return nil, valueErr("work_zcode_message_missing")
		}
		if _, old := prev[p.key]; old || autosaveGet(p.data, "type") != "text" || truthy(autosaveGet(p.data, "synthetic")) {
			continue
		}
		role := autosaveGet(message, "role")
		semantics := autosaveGet(message, "semantics")
		realUser := truthy(semantics) && autosaveGet(semantics, "origin") == "real_user"
		if (role == "user" && realUser) || (role == "assistant" && wzcodeCompleted(message)) {
			eligible = append(eligible, wzcodePartKey(p.record["id"].(string)))
		}
	}
	return eligible, nil
}
