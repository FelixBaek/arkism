package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

type deliveryFixture struct {
	t     *testing.T
	root  string
	scope map[string]any
}

func newDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	f := &deliveryFixture{t: t, root: t.TempDir(), scope: map[string]any{"workspace_id": "w", "project_id": "p"}}
	s, sc := capState, capScope
	t.Cleanup(func() { capState, capScope = s, sc })
	capState = filepath.Join(f.root, "arkism-capture")
	capScope = func() map[string]any { return f.scope }
	return f
}

func (f *deliveryFixture) put(root, folder, name string, value any) string {
	path := filepath.Join(f.root, root, folder, name)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(host.MustDumps(value, host.DumpOpts{})), 0o600)
	return path
}

func (f *deliveryFixture) server(workID string, scope any, extra ...host.KV) map[string]any {
	if scope == nil {
		scope = f.scope
	}
	out := map[string]any{"scope": scope, "id": workID, "created_at": timeNow(),
		"prepared": map[string]any{"package": map[string]any{"goal": "PRIVATE queued text"}}}
	for _, kv := range extra {
		out[kv.Key] = kv.Value
	}
	return out
}

func (f *deliveryFixture) status() host.Obj {
	f.t.Helper()
	v, err := deliveryStatus("task")
	if err != nil {
		f.t.Fatal(err)
	}
	return v.(host.Obj)
}

func deliveryField(o host.Obj, k string) any { v, _ := o.Get(k); return v }

func TestDeliveryServerAndPcSameWorkOnlyNoDialogueOrPaths(t *testing.T) {
	f := newDeliveryFixture(t)
	path := f.put("arkism-capture", "work-handoff", strings.Repeat("a", 64)+".pending.json", f.server("task", nil))
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	f.put("arkism-capture", "work-handoff", strings.Repeat("b", 64)+".pending.json", f.server("other", nil))
	f.put("arkism-capture", "work-handoff", strings.Repeat("c", 64)+".pending.json", f.server("task", map[string]any{"workspace_id": "other"}))
	f.put("arkism-capture", "work-handoff", strings.Repeat("d", 64)+".cancelled.json", f.server("task", nil))
	f.put("arkism-capture-pc-zcode", "pc-work-events", strings.Repeat("e", 64)+".json", map[string]any{
		"scope": f.scope, "events": []any{map[string]any{"event": map[string]any{"work_id": "task", "created_at": timeNow()},
			"last_error": "work_revision_changed", "job": map[string]any{"prepared": map[string]any{"package": "PRIVATE"}}}},
		"cancelled": []any{map[string]any{"event": map[string]any{"work_id": "task"}}}})
	result := f.status()
	if deliveryField(result, "state") != "pending" || deliveryField(result, "pending") != 2 || deliveryField(result, "failed") != 1 {
		t.Fatal(result)
	}
	dumped := host.MustDumps(result, host.DumpOpts{})
	if strings.Contains(dumped, "PRIVATE") || strings.Contains(dumped, f.root) {
		t.Fatal(dumped)
	}
	after, _ := os.ReadFile(path)
	info2, _ := os.Stat(path)
	if string(after) != string(before) || !info2.ModTime().Equal(info.ModTime()) {
		t.Fatal("status changed a pending file")
	}
	capState = filepath.Join(f.root, "arkism-capture-pc-codex")
	if host.MustDumps(f.status(), host.DumpOpts{}) != dumped {
		t.Fatal("sibling root sees different status")
	}
}

func TestDeliveryAbsenceAndSavedReceiptDoNotClaimAllDialogueIsDelivered(t *testing.T) {
	f := newDeliveryFixture(t)
	f.put("arkism-capture", "work-handoff", strings.Repeat("a", 64)+".pending.json",
		f.server("task", nil, kv("receipt", map[string]any{"revision": 3})))
	result := f.status()
	if deliveryField(result, "state") != "no_pending_observed" || !strings.Contains(deliveryField(result, "note").(string), "미전송") {
		t.Fatal(result)
	}
	if host.MustDumps(result, host.DumpOpts{NoASCII: true}) != `{"state": "no_pending_observed", "pending": 0, "failed": 0, "note": "`+deliveryNote+`"}` {
		t.Fatal(result)
	}
}

func TestDeliveryIncompleteScanIsUnknownAndNeverFollowsLinks(t *testing.T) {
	f := newDeliveryFixture(t)
	path := f.put("arkism-capture", "work-handoff", strings.Repeat("a", 64)+".pending.json", f.server("task", nil))
	for _, raw := range []string{"{", "[]", strings.Repeat("x", int(deliveryMaxBytes)+1)} {
		os.WriteFile(path, []byte(raw), 0o600)
		if deliveryField(f.status(), "state") != "unknown" {
			t.Fatal(raw[:1])
		}
	}
	os.Remove(path)
	os.Symlink(filepath.Join(f.root, "private-missing-target"), path)
	if deliveryField(f.status(), "state") != "unknown" {
		t.Fatal("followed a file link")
	}
	os.Remove(path)
	os.Remove(filepath.Dir(path))
	os.Symlink(filepath.Join(f.root, "private-missing-directory"), filepath.Dir(path))
	if deliveryField(f.status(), "state") != "unknown" {
		t.Fatal("followed a folder link")
	}
}

func TestDeliveryScanLimitAndExpiredMetadataAreNotReportedAsReady(t *testing.T) {
	f := newDeliveryFixture(t)
	value := f.server("task", nil)
	value["created_at"] = timeNow() - 8*86400
	f.put("arkism-capture", "work-handoff", strings.Repeat("a", 64)+".pending.json", value)
	if deliveryField(f.status(), "state") != "unknown" {
		t.Fatal("expired metadata counted")
	}
	f.put("arkism-capture", "work-handoff", strings.Repeat("a", 64)+".pending.json", f.server("task", nil))
	files, total, seconds := deliveryMaxFiles, deliveryMaxTotalBytes, deliveryMaxSeconds
	t.Cleanup(func() { deliveryMaxFiles, deliveryMaxTotalBytes, deliveryMaxSeconds = files, total, seconds })
	for _, limit := range []func(){
		func() { deliveryMaxFiles = 0 },
		func() { deliveryMaxTotalBytes = 10 },
		func() { deliveryMaxSeconds = 0 },
	} {
		deliveryMaxFiles, deliveryMaxTotalBytes, deliveryMaxSeconds = files, total, seconds
		if deliveryField(f.status(), "state") != "pending" {
			t.Fatal("baseline not pending")
		}
		limit()
		if deliveryField(f.status(), "state") != "unknown" {
			t.Fatal("limit ignored")
		}
	}
}

func TestDeliveryCustomStateDirectoryDoesNotInspectDefaultSiblings(t *testing.T) {
	f := newDeliveryFixture(t)
	f.put("arkism-capture", "work-handoff", strings.Repeat("a", 64)+".pending.json", f.server("task", nil))
	capState = filepath.Join(f.root, "isolated-state")
	if deliveryField(f.status(), "pending") != 0 {
		t.Fatal("inspected a default sibling")
	}
}

func TestDeliveryBudgetReassemblyKeepsDeliveryMetadata(t *testing.T) {
	f := newDeliveryFixture(t)
	f.put("arkism-capture", "work-handoff", strings.Repeat("a", 64)+".pending.json", f.server("task", nil))
	status := f.status()
	pkg := map[string]any{"goal": "목표", "decisions": []any{strings.Repeat("결정", 2500)}, "constraints": []any{strings.Repeat("제약", 2000)}}
	rendered, err := host.WorkHookContext("task", func(string, map[string]any) (map[string]any, error) {
		return map[string]any{"id": "task", "revision": 2, "package": pkg}, nil
	}, f.scope, 8000, status)
	if err != nil {
		t.Fatal(err)
	}
	output, err := host.Assemble("", rendered, "", 2000)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := host.Loads(output[len(host.HookHeader):])
	if err != nil {
		t.Fatal(err)
	}
	if !eq(parsed.(map[string]any)["delivery"], status) || len([]rune(output)) > 2000 {
		t.Fatal(parsed)
	}
}
