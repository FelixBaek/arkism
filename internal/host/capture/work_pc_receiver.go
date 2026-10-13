package capture

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Durably accept PC work signals over the existing authenticated SSH path.

const receiverLimit = 2 * 1024 * 1024

var (
	receiverFields  = []string{"version", "event_id", "sequence", "origin", "session_id", "cwd", "work_id", "action", "created_at", "boundary"}
	receiverEventID = regexp.MustCompile(`^pc-work-[a-f0-9]{32}$`)
)

// receiverDecode validates one raw PC work event.
func receiverDecode(raw []byte) (map[string]any, error) {
	invalid := valueErr("work_pc_event_invalid")
	if len(raw) > receiverLimit {
		return nil, invalid
	}
	v, err := loadsBytes(raw)
	if err != nil {
		return nil, err
	}
	event, ok := v.(map[string]any)
	if !ok || len(event) != len(receiverFields) {
		return nil, invalid
	}
	for _, k := range receiverFields {
		if _, has := event[k]; !has {
			return nil, invalid
		}
	}
	version, vok := host.Int(event["version"])
	id, idOK := event["event_id"].(string)
	seq, seqOK := host.Int(event["sequence"])
	origin, _ := event["origin"].(string)
	sid, sidOK := event["session_id"].(string)
	action, _ := event["action"].(string)
	_, isBool := event["created_at"].(bool)
	created, cok := num(event["created_at"])
	if !vok || version != 1 || !idOK || !receiverEventID.MatchString(id) || !seqOK || seq <= 0 ||
		(origin != "claude_code" && origin != "codex" && origin != "zcode") || !sidOK || sid == "" ||
		(action != "bind" && action != "stop" && action != "suspend") || isBool || !cok {
		return nil, invalid
	}
	if origin != "zcode" && len(raw) > 16*1024 {
		return nil, invalid
	}
	now := timeNow()
	if !(now-outboxTTL < created && created <= now+60) {
		return nil, valueErr("work_pc_event_expired")
	}
	folderKey, err := boundaryFolder(event["cwd"])
	if err != nil {
		return nil, err
	}
	if action == "suspend" {
		if event["work_id"] != "" || event["boundary"] != nil {
			return nil, invalid
		}
	} else {
		if w, ok := event["work_id"].(string); !ok || !outboxKey.MatchString(w) {
			return nil, invalid
		}
		if err := boundaryValidate(event["boundary"]); err != nil {
			return nil, err
		}
		marker := event["boundary"].(map[string]any)
		if marker["origin"] != origin || marker["session_id"] != sid {
			return nil, invalid
		}
		mk, err := boundaryFolder(marker["cwd"])
		if err != nil {
			return nil, err
		}
		if mk != folderKey {
			return nil, invalid
		}
	}
	return event, nil
}

// receiverCreated is e['created_at'] of a stored row (an event or a receipt).
func receiverCreated(row any, nested bool) float64 {
	m := row.(map[string]any)
	if nested {
		m = m["event"].(map[string]any)
	}
	f, ok := num(m["created_at"])
	if !ok {
		panic(fmt.Errorf("%w: created_at", errType))
	}
	return f
}

func receiverLive(rows []any, nested bool, now float64) []any {
	live := []any{}
	for _, r := range rows {
		if receiverCreated(r, nested)+outboxTTL > now {
			live = append(live, r)
		}
	}
	return live
}

// receiverReceive stores one event in the session inbox (idempotent per event) and
// returns the receipt the PC needs before it may delete its copy.
func receiverReceive(raw []byte) (reply host.Obj, err error) {
	defer recovered(&err)
	event, err := receiverDecode(raw)
	if err != nil {
		return nil, err
	}
	if os.Getenv("ARKISM_CLIENT") != "pc" || capOrigin != event["origin"] {
		return nil, valueErr("work_pc_runtime_invalid")
	}
	scope := capScope()
	folderKey, err := boundaryFolder(event["cwd"])
	if err != nil {
		return nil, err
	}
	key := sha256Hex([]byte(host.MustDumps([]any{scope, event["origin"], event["session_id"], folderKey}, host.DumpOpts{SortKeys: true})))
	sha := sha256Hex(raw)
	reply = host.Obj{{Key: "accepted", Value: true}, {Key: "event_id", Value: event["event_id"]},
		{Key: "sequence", Value: event["sequence"]}, {Key: "sha256", Value: sha}}
	err = outboxLocked(filepath.Join(capState, "pc-work-events"), func(root string) (err error) {
		defer recovered(&err)
		path := filepath.Join(root, key+".json")
		if isSymlink(path) {
			return valueErr("work_pc_state_alias")
		}
		state := map[string]any{"scope": scope, "high_sequence": int64(0), "events": []any{}, "receipts": []any{}}
		if exists(path) {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if state, err = loadObj(b); err != nil {
				return err
			}
		}
		if !eq(state["scope"], scope) {
			return valueErr("work_scope_changed")
		}
		var revision any
		if event["action"] != "suspend" {
			work, err := capPost("/v1/work-checkpoints/get", host.Obj{{Key: "scope", Value: scope}, {Key: "id", Value: event["work_id"]}}, 3*time.Second)
			if err != nil {
				return err
			}
			rev, ok := host.Int(work["revision"])
			if work["id"] != event["work_id"] || !ok || rev < 1 || work["authority"] != "data_only_untrusted" {
				return valueErr("work_pc_work_invalid")
			}
			revision = work["revision"]
		}
		now := timeNow()
		events := state["events"].([]any)
		live := receiverLive(events, true, now)
		if len(live) != len(events) {
			state["binding"] = nil
		}
		state["events"] = live
		if cancelled, has := state["cancelled"]; has {
			state["cancelled"] = receiverLive(cancelled.([]any), true, now)
		}
		receipts := receiverLive(state["receipts"].([]any), false, now)
		state["receipts"] = receipts
		for _, r := range receipts {
			receipt := r.(map[string]any)
			if eq(receipt["event_id"], event["event_id"]) || eq(receipt["sequence"], event["sequence"]) {
				for _, k := range []string{"event_id", "sequence", "sha256"} {
					want, _ := reply.Get(k)
					if !eq(receipt[k], want) {
						return valueErr("work_pc_event_conflict")
					}
				}
				return nil
			}
		}
		high, ok := host.Int(state["high_sequence"])
		seq, _ := host.Int(event["sequence"])
		if !ok {
			return fmt.Errorf("%w: high_sequence", errType)
		}
		if seq <= high {
			return valueErr("work_pc_event_stale")
		}
		if len(receipts) >= 1000 {
			return valueErr("work_pc_inbox_full")
		}
		state["high_sequence"] = event["sequence"]
		state["events"] = append(live, map[string]any{"event": event, "sha256": sha, "work_revision": revision})
		receipt := map[string]any{"created_at": event["created_at"]}
		for _, kv := range reply {
			receipt[kv.Key] = kv.Value
		}
		state["receipts"] = append(receipts, receipt)
		encoded := pcHookJSON(state)
		if len(encoded) > 8*1024*1024 {
			return valueErr("work_pc_inbox_full")
		}
		return outboxWrite(path, encoded)
	})
	if err != nil {
		return nil, err
	}
	return reply, nil
}

var receiverCode = regexp.MustCompile(`^work_[a-z_]+$`)

func receiverMain(_ []string, stdin io.Reader, stdout, _ io.Writer) int {
	raw, err := io.ReadAll(io.LimitReader(stdin, receiverLimit+1))
	var reply host.Obj
	if err == nil {
		reply, err = receiverReceive(raw)
	}
	if err != nil {
		code := "work_pc_receive_failed"
		var ve valueErr
		if errors.As(err, &ve) && receiverCode.MatchString(string(ve)) {
			code = string(ve)
		}
		fmt.Fprintln(stdout, host.MustDumps(host.Obj{{Key: "accepted", Value: false}, {Key: "error", Value: code}}, host.DumpOpts{}))
		return 1
	}
	fmt.Fprintln(stdout, host.MustDumps(reply, host.DumpOpts{}))
	spawnDrain() // the durable inbox stays for the periodic drain if this fails
	return 0
}

func init() { host.Register("work-pc-receiver", receiverMain) }
