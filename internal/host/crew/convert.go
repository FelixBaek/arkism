package crew

// arkism crew convert: once, wrap each named agent and team in a workflow of the same name ($이름) whose one step
// runs it, so Flow lists workflows only. The agent or team stays as the step's part and nothing is deleted; "@이름"
// then reaches the wrapping workflow (the server answers with both). Without --apply it only lists the changes;
// --apply first writes every crew record to a backup file. Running it again skips what is already wrapped.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

var convertNow = time.Now

func convertKey(h string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimLeft(strings.TrimSpace(h), "@$"), " ", ""))
}

// planConvert is one change per named agent or team: "wrap" with the workflow to create, or "skip" with why
// ("already": a workflow of that name runs it; "rename": another workflow has the name, rename one of them;
// "works_by_workflow": the agent already runs as its linked workflow, which Flow lists).
func planConvert(items []any) []map[string]any {
	flows := []map[string]any{}
	for _, r := range items {
		if c := obj(obj(r)["crew"]); c["type"] == "workflow" {
			flows = append(flows, c)
		}
	}
	out := []map[string]any{}
	for _, r := range items {
		id, c := pyStr(obj(r)["id"]), obj(obj(r)["crew"])
		typ, handle := pyStr(c["type"]), pyStr(c["handle"])
		if typ != "agent" && typ != "team" || !truthy(c["handle"]) {
			continue
		}
		change := map[string]any{"kind": typ, "id": id, "handle": handle, "action": "skip"}
		out = append(out, change)
		if typ == "agent" && truthy(c["workflow"]) {
			change["reason"] = "works_by_workflow"
			continue
		}
		if f := convertNamed(flows, handle); f != nil {
			change["reason"] = map[bool]string{true: "already", false: "rename"}[convertWraps(f, id)]
			continue
		}
		change["action"], change["crew"] = "wrap", wrapperCrew(id, c)
	}
	return out
}

func convertNamed(flows []map[string]any, handle string) map[string]any {
	for _, f := range flows {
		if convertKey(pyStr(f["handle"])) == convertKey(handle) {
			return f
		}
	}
	return nil
}

// convertWraps mirrors the server's wrapsCrew: a step of the workflow runs the agent or team id.
func convertWraps(f map[string]any, id string) bool {
	for _, n := range list(obj(f["graph"])["nodes"]) {
		node := obj(n)
		cfg := obj(node["config"])
		if node["type"] == "expert" && pyStr(cfg["expert"]) == id || node["type"] == "team" && pyStr(cfg["team"]) == id {
			return true
		}
	}
	return false
}

// wrapperCrew is the one-step graph the server already runs an agent or team as (flowGraphFor), saved as a workflow.
func wrapperCrew(id string, c map[string]any) map[string]any {
	work := map[string]any{"id": "work", "type": "expert", "title": c["name"], "x": 240, "y": 0,
		"config": map[string]any{"expert": id, "access": "read", "goal": "{{input}}"}}
	if c["type"] == "team" {
		work["type"], work["config"] = "team", map[string]any{"team": id, "access": "read", "topic": "{{input}}"}
	}
	out := map[string]any{"type": "workflow", "name": c["name"], "handle": c["handle"], "graph": map[string]any{
		"nodes": []any{map[string]any{"id": "start", "type": "start", "x": 0, "y": 0}, work, map[string]any{"id": "end", "type": "end", "x": 480, "y": 0}},
		"edges": []any{map[string]any{"from": "start", "to": "work"}, map[string]any{"from": "work", "to": "end"}}}}
	for _, k := range []string{"role", "category", "avatar", "cover"} {
		if truthy(c[k]) {
			out[k] = c[k]
		}
	}
	return out
}

func convertBackupPath() string {
	return filepath.Join(host.Home, ".config", "arkism", "backups", "crew-before-convert-"+convertNow().UTC().Format("20060102T150405Z")+".json")
}

// runConvert lists the plan, and with apply backs up every crew record and then creates the wrapping workflows.
// It stops at the first refused write; running it again picks up where it stopped.
func runConvert(apply bool, backup string) (map[string]any, error) {
	page, err := apiGet("/v1/crew?"+scopeQuery(), 15*time.Second)
	if err != nil {
		return nil, apiErr(err)
	}
	items := list(getOr(page, "items", []any{}))
	plan := planConvert(items)
	out := map[string]any{"apply": apply, "changes": plan}
	if !apply {
		return out, nil
	}
	if backup == "" {
		backup = convertBackupPath()
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(backup, []byte(dumpIndent(page)), 0o600); err != nil {
		return nil, err
	}
	out["backup"] = backup
	for _, ch := range plan {
		if ch["action"] != "wrap" {
			continue
		}
		rec, err := apiPost("/v1/crew", host.Obj{{Key: "scope", Value: scope()}, {Key: "crew", Value: ch["crew"]}}, 15*time.Second)
		if err != nil {
			ch["error"] = errName(err)
			return out, crewErr(fmt.Sprintf("$%s 워크플로우를 만들지 못했어요. 백업: %s", pyStr(ch["handle"]), backup))
		}
		ch["created"] = rec["id"]
	}
	return out, nil
}
