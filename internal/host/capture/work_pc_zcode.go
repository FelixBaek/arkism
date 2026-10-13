package capture

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"sort"

	"github.com/FelixBaek/arkism/internal/host"
)

// Portable Zcode work boundaries validated against reconstructed PC snapshots.

var pcZcodeHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// pcZcodeValidate checks a version 2 Zcode boundary marker.
func pcZcodeValidate(marker any) error {
	m, ok := marker.(map[string]any)
	if o, isObj := marker.(host.Obj); isObj {
		m, ok = map[string]any{}, true
		for _, kv := range o {
			m[kv.Key] = kv.Value
		}
	}
	invalid := valueErr("work_pc_boundary_invalid")
	if !ok || len(m) != 6 {
		return invalid
	}
	for _, k := range []string{"version", "origin", "session_id", "cwd", "logical", "records"} {
		if _, has := m[k]; !has {
			return invalid
		}
	}
	version, isInt := host.Int(m["version"])
	sid, _ := m["session_id"].(string)
	if !isInt || version != 2 || m["origin"] != "zcode" || sid == "" || m["logical"] != "db-session" {
		return invalid
	}
	switch m["records"].(type) {
	case map[string]any, host.Obj:
	default:
		return invalid
	}
	records := wzcodeRecords(m["records"])
	if len(records) > wzcodeLimit {
		return invalid
	}
	for k, v := range records {
		s, isStr := v.(string)
		if !pcZcodeHash.MatchString(k) || !isStr || !pcZcodeHash.MatchString(s) {
			return invalid
		}
	}
	_, err := boundaryFolder(m["cwd"])
	return err
}

// pcZcodeCapture snapshots session sid of database and returns its portable boundary.
func pcZcodeCapture(database string, sid, cwd any) (map[string]any, error) {
	id, _ := sid.(string)
	var records host.Obj
	err := zcodeSnapshot(database, id, func(snapshot string, _ float64, _ any) error {
		var err error
		records, _, err = wzcodeWindow(snapshot, id, cwd, nil, false, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": int64(2), "origin": "zcode", "session_id": sid, "cwd": cwd, "logical": "db-session", "records": records}, nil
}

// pcZcodeVerify checks marker against snapshot and returns the window after it.
func pcZcodeVerify(snapshot string, marker any) (host.Obj, []string, error) {
	if err := pcZcodeValidate(marker); err != nil {
		return nil, nil, err
	}
	sid, _ := objGet(marker, "session_id")
	cwd, _ := objGet(marker, "cwd")
	records, _ := objGet(marker, "records")
	return wzcodeWindow(snapshot, sid.(string), cwd, records, true, true)
}

// pcZcodeEligible returns the sorted part keys that became eligible between start and end.
func pcZcodeEligible(snapshot string, start, end any) (keys []string, err error) {
	defer recovered(&err)
	_, eligible, err := pcZcodeVerify(snapshot, start)
	if err != nil {
		return nil, err
	}
	if _, _, err := pcZcodeVerify(snapshot, end); err != nil {
		return nil, err
	}
	// Both hashes were checked against the same snapshot, including known finalization.
	startV, _ := objGet(start, "records")
	endV, _ := objGet(end, "records")
	endRecords := wzcodeRecords(endV)
	for k := range wzcodeRecords(startV) {
		if _, has := endRecords[k]; !has {
			return nil, valueErr("work_pc_boundary_changed")
		}
	}
	endKeys := map[string]bool{}
	f, err := os.Open(snapshot)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		raw, rerr := r.ReadBytes('\n')
		if len(raw) > 0 {
			row, err := loadsBytes(raw)
			if err != nil {
				return nil, err
			}
			if autosaveGet(row, "table") == "part" {
				id := autosaveItem(autosaveItem(row, "record"), "id")
				if _, has := endRecords[wzcodeDigest([]any{"part", id})]; has {
					endKeys[wzcodePartKey(id.(string))] = true
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	keys = []string{}
	seen := map[string]bool{}
	for _, k := range eligible {
		if endKeys[k] && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
