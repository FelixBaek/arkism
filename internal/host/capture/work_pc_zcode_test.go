package capture

import (
	"sort"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func pcZcodeMessage(role, origin string, completed bool) host.Obj {
	t := host.Obj{}
	if completed {
		t = host.Obj{{Key: "completed", Value: 1}}
	}
	return host.Obj{{Key: "role", Value: role}, {Key: "semantics", Value: host.Obj{{Key: "origin", Value: origin}}}, {Key: "time", Value: t}}
}

func newPcZcodeFixture(t *testing.T) *wzcodeFixture {
	f := newWzcodeFixture(t, "C:/repo")
	f.add("old", pcZcodeMessage("user", "real_user", true))
	return f
}

func pcZcodeCapt(t *testing.T, f *wzcodeFixture) map[string]any {
	t.Helper()
	m, err := pcZcodeCapture(f.path, "s", `C:\repo`)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func pcZcodeWith(f *wzcodeFixture, fn func(snapshot string) error) error {
	return zcodeSnapshot(f.path, "s", func(snapshot string, _ float64, _ any) error { return fn(snapshot) })
}

func pcZcodeEligibleNow(f *wzcodeFixture, start, end any) (keys []string, err error) {
	err = pcZcodeWith(f, func(snapshot string) error {
		var err error
		keys, err = pcZcodeEligible(snapshot, start, end)
		return err
	})
	return keys, err
}

func pcZcodeCopy(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{}
	for key, value := range m {
		out[key] = value
	}
	out[k] = v
	return out
}

func TestPcZcodeExactWindowExcludesOldRuntimeAndAfterStop(t *testing.T) {
	f := newPcZcodeFixture(t)
	start := pcZcodeCapt(t, f)
	f.add("new", pcZcodeMessage("user", "real_user", true))
	f.add("answer", pcZcodeMessage("assistant", "real_user", true))
	f.add("runtime", pcZcodeMessage("user", "agent_runtime", true))
	end := pcZcodeCapt(t, f)
	f.add("after-stop", pcZcodeMessage("user", "real_user", true))
	f.add("pending-after-stop", pcZcodeMessage("assistant", "real_user", false))
	keys, err := pcZcodeEligibleNow(f, start, end)
	want := []string{wzcodePartKey("new-part"), wzcodePartKey("answer-part")}
	sort.Strings(want)
	if err != nil || strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatal(keys, err)
	}
	if strings.Contains(host.MustDumps(end, host.DumpOpts{}), "answer") {
		t.Fatal("boundary holds dialogue")
	}
	if err := boundaryValidate(end); err != nil {
		t.Fatal(err)
	}
}

func TestPcZcodeStopAnchorFinalizationPreservesOldBoundariesButRejectsChangedContent(t *testing.T) {
	f := newPcZcodeFixture(t)
	start := pcZcodeCapt(t, f)
	f.add("answer", pcZcodeMessage("assistant", "real_user", true))
	compact := host.DumpOpts{NoASCII: true, Compact: true}
	original := host.Obj{{Key: "role", Value: "assistant"}, {Key: "time", Value: host.Obj{{Key: "completed", Value: 1}}},
		{Key: "anchor", Value: host.Obj{{Key: "turnId", Value: "turn"}}}}
	f.setMessage("answer", original, compact)
	stop := pcZcodeCapt(t, f)
	anchor := host.Obj{{Key: "turnId", Value: "turn"}, {Key: "historyRoundCount", Value: 1},
		{Key: "orderedMessageIds", Value: []any{"answer"}}, {Key: "boundaryMessageId", Value: "answer"},
		{Key: "goalBoundary", Value: host.Obj{{Key: "kind", Value: "none"}}}}
	final := func(k string, v any) host.Obj {
		o := host.Obj{{Key: "role", Value: "assistant"}, {Key: "time", Value: host.Obj{{Key: "completed", Value: 1}}}, {Key: "anchor", Value: anchor}}
		if k != "" {
			o.Set(k, v)
		}
		return o
	}
	f.setMessage("answer", final("", nil), compact)
	finalized := pcZcodeCapt(t, f)
	if keys, err := pcZcodeEligibleNow(f, start, stop); err != nil || len(keys) != 1 {
		t.Fatal(keys, err)
	}
	if keys, err := pcZcodeEligibleNow(f, stop, finalized); err != nil || len(keys) != 0 {
		t.Fatal(keys, err)
	}
	other := append(host.Obj{}, anchor...)
	other.Set("turnId", "other")
	extra := append(host.Obj{}, anchor...)
	extra.Set("extra", "unknown")
	for _, changed := range []host.Obj{
		final("role", "user"), final("time", host.Obj{{Key: "completed", Value: 2}}),
		final("anchor", other), final("anchor", extra),
		final("semantics", host.Obj{{Key: "origin", Value: "agent_runtime"}}),
	} {
		f.setMessage("answer", changed, compact)
		_, err := pcZcodeEligibleNow(f, start, stop)
		wzcodeWantErr(t, err, "history_changed")
	}
	f.setMessage("answer", final("", nil), compact)
	f.exec(`UPDATE part SET data='{"type": "text", "text": "changed"}' WHERE id='answer-part'`)
	_, err := pcZcodeEligibleNow(f, start, stop)
	wzcodeWantErr(t, err, "history_changed")
}

func TestPcZcodeHistoryChangesDeletionsAndReversedWindowsAreRejected(t *testing.T) {
	f := newPcZcodeFixture(t)
	start := pcZcodeCapt(t, f)
	f.add("new", pcZcodeMessage("user", "real_user", true))
	end := pcZcodeCapt(t, f)
	_, err := pcZcodeEligibleNow(f, end, start)
	wzcodeWantErr(t, err, "boundary_changed")
	verify := func() error {
		return pcZcodeWith(f, func(snapshot string) error { _, _, err := pcZcodeVerify(snapshot, start); return err })
	}
	f.exec(`UPDATE part SET data='{"type": "text", "text": "rewritten"}' WHERE id='old-part'`)
	wzcodeWantErr(t, verify(), "history_changed")
	f.exec(`DELETE FROM part WHERE id='old-part'`)
	wzcodeWantErr(t, verify(), "history_changed")
}

func TestPcZcodePendingWrongSessionFolderAndInvalidMarkers(t *testing.T) {
	f := newPcZcodeFixture(t)
	marker := pcZcodeCapt(t, f)
	f.add("pending", pcZcodeMessage("assistant", "real_user", false))
	_, err := pcZcodeCapture(f.path, "s", `C:\repo`)
	wzcodeWantErr(t, err, "pending")
	for _, c := range [][2]any{{"session_id", "other"}, {"cwd", "D:/repo"}} {
		err := pcZcodeWith(f, func(snapshot string) error {
			_, _, err := pcZcodeVerify(snapshot, pcZcodeCopy(marker, c[0].(string), c[1]))
			return err
		})
		wzcodeWantErr(t, err, "session_invalid")
	}
	for _, c := range [][2]any{
		{"records", map[string]any{"invalid": strings.Repeat("a", 64)}},
		{"records", map[string]any{strings.Repeat("a", 64): false}},
		{"version", true}, {"extra", 0},
	} {
		wzcodeWantErr(t, boundaryValidate(pcZcodeCopy(marker, c[0].(string), c[1])), "boundary_invalid")
	}
	keep := wzcodeLimit
	t.Cleanup(func() { wzcodeLimit = keep })
	wzcodeLimit = 1
	wzcodeWantErr(t, boundaryValidate(marker), "boundary_invalid")
}
