package crew

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func mem(id, kind, text string, day int, rev ...int) map[string]any {
	r := 2
	if len(rev) > 0 {
		r = rev[0]
	}
	return map[string]any{"id": id, "kind": kind, "text": text, "revision": r, "created_at": "2026-09-" + twoDigits(day) + "T00:00:00Z"}
}

func twoDigits(n int) string { return string(rune('0'+n/10)) + string(rune('0'+n%10)) }

func maintainItems() []map[string]any {
	return []map[string]any{mem("old", "user", "응답은 한국어로", 20), mem("new", "user", "답은 한국어로, 쉬운 말로 해 줘", 30),
		mem("d1", "decision", "Redis는 ADR 승인 전까지 쓰지 않는다", 10), mem("d2", "decision", "단기 상태는 Redis에 둔다", 25),
		mem("p1", "procedural", "응답은 한국어로", 1)}
}

func pairSet(pairs []memPair) map[string]bool {
	out := map[string]bool{}
	for _, p := range pairs {
		out[pairOf(p.older["id"], p.newer["id"])] = true
	}
	return out
}

func queryOf(t *testing.T, path string) url.Values {
	q, err := url.ParseQuery(strings.SplitN(path, "?", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// The night's run with its inputs faked: no search hits, metrics dropped.
func fakeNight(t *testing.T, items []map[string]any) {
	patch(t, &memoriesFn, func() ([]map[string]any, error) { return items, nil })
	patch(t, &knownPairsFn, func([]map[string]any) (map[string]bool, error) { return map[string]bool{}, nil })
	patch(t, &mSearch, func(string, int) ([]any, error) { return nil, nil })
	patch(t, &mMetric, func(map[string]any) {})
	patch(t, &signalsFn, func([]map[string]any) (map[string]map[string]any, error) { return nil, nil })
	patch(t, &host.CaptureState, t.TempDir())
}

func TestPairsStayWithinAKindAndSkipKnownPairs(t *testing.T) {
	items := maintainItems()
	search := func(q string, _ int) ([]any, error) {
		if strings.Contains(q, "Redis는") {
			return []any{map[string]any{"id": "d2"}}, nil
		}
		return nil, nil
	}
	pairs := candidatePairs(items, map[string]bool{pairOf("x", "y"): true}, search)
	keys := pairSet(pairs)
	if !keys[pairOf("old", "new")] || !keys[pairOf("d1", "d2")] || keys[pairOf("old", "p1")] {
		t.Fatalf("pairs %v", keys)
	}
	for _, p := range pairs {
		if when(p.newer).Before(when(p.older)) {
			t.Fatalf("older after newer: %v", p)
		}
	}
	if got := candidatePairs(items, map[string]bool{pairOf("old", "new"): true, pairOf("d1", "d2"): true}, search); len(got) != 0 {
		t.Fatalf("known pairs came back: %v", got)
	}
}

func TestAnOpenProposalForAnEditedMemoryDoesNotBlockThePair(t *testing.T) {
	prop := func(id, k, r string, kr, rr int) any {
		return map[string]any{"id": id, "maintenance": map[string]any{"keep": map[string]any{"id": k, "revision": kr}, "retire": map[string]any{"id": r, "revision": rr}}}
	}
	lists := map[string][]any{"open": {prop("p1", "new", "old", 2, 2), prop("p2", "d2", "d1", 2, 1)}, "dismissed": {prop("p3", "d2", "p1", 1, 1),
		map[string]any{"id": "p4", "maintenance": map[string]any{"memory": map[string]any{"id": "old", "revision": 1}}}}}
	patch(t, &mGet, func(path string, _ time.Duration) (map[string]any, error) {
		return map[string]any{"items": lists[queryOf(t, path).Get("status")]}, nil
	})
	seen, err := knownPairs(maintainItems())
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(seen), 3) // d1 is at revision 2 now; a decided signal proposal is never asked again
	if !seen[pairOf("new", "old")] || !seen[pairOf("d2", "p1")] || !seen[signalKey("old")] {
		t.Fatalf("seen %v", seen)
	}
}

func TestOrderIsByTimeNotByString(t *testing.T) {
	a := map[string]any{"created_at": "2026-09-30T12:00:00.100Z"}
	b := map[string]any{"created_at": "2026-09-30T12:00:00Z"}
	if !when(b).Before(when(a)) { // as strings, "…00.100Z" < "…00Z"
		t.Fatal("ordered as strings")
	}
	if !when(map[string]any{}).IsZero() {
		t.Fatal("no time is not the epoch")
	}
}

func TestPairsJudgedDifferentAreNotJudgedAgainUntilEdited(t *testing.T) {
	items := maintainItems()
	fakeNight(t, items)
	old, nw := items[0], items[1]
	if err := rememberDifferent([]memPair{{old, nw, 0.6}}, []any{map[string]any{"pair": 0, "verdict": "different"}, map[string]any{"pair": 9, "verdict": "different"}}); err != nil {
		t.Fatal(err)
	}
	if got := judgedPairs(items); len(got) != 1 || !got[pairOf("old", "new")] {
		t.Fatalf("judged %v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(host.CaptureState, "maintain_judged.json"))
	if strings.Contains(string(raw), "한국어") { // ids and revisions only
		t.Fatal("memory text in the judged file")
	}
	edited := append([]map[string]any{withKey(old, "revision", 3)}, items[1:]...)
	if got := judgedPairs(edited); len(got) != 0 { // an edited memory is looked at again
		t.Fatalf("edited pair still judged: %v", got)
	}
	var judged []memPair
	patch(t, &judgeFn, func(pairs []memPair) ([]any, error) { judged = append(judged, pairs...); return nil, nil })
	if _, _, err := maintainRun(false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if pairSet(judged)[pairOf("old", "new")] {
		t.Fatal("the pair was judged again")
	}
}

func TestMemoriesFollowTheCursor(t *testing.T) {
	pages := map[string]map[string]any{"": {"items": []any{mem("a", "user", "가", 1)}, "next_cursor": "c2"}, "c2": {"items": []any{mem("b", "user", "나", 2)}}}
	patch(t, &mGet, func(path string, _ time.Duration) (map[string]any, error) {
		q := queryOf(t, path)
		if q.Get("kind") != "user" {
			return map[string]any{"items": []any{}}, nil
		}
		if p, ok := pages[q.Get("cursor")]; ok {
			return p, nil
		}
		return map[string]any{}, nil
	})
	items, err := memories()
	if err != nil {
		t.Fatal(err)
	}
	ids := []any{}
	for _, m := range items {
		ids = append(ids, m["id"])
	}
	eq(t, ids, []any{"a", "b"})
}

func TestAContradictionAlwaysKeepsTheNewerMemory(t *testing.T) {
	items := maintainItems()
	pairs := []memPair{{items[2], items[3], 0.5}, {items[0], items[1], 0.6}}
	out := proposals(pairs, []any{map[string]any{"pair": 0, "verdict": "contradiction", "keep": "a", "reason": "same_meaning"},
		map[string]any{"pair": 1, "verdict": "duplicate", "keep": "b", "reason": "more_specific"},
		map[string]any{"pair": 7, "verdict": "duplicate", "keep": "a", "reason": "same_meaning"}})
	got := [][4]any{}
	for _, p := range out {
		got = append(got, [4]any{p.kind, p.keep["id"], p.retire["id"], p.reason})
	}
	eq(t, len(got), 2)
	if got[0] != [4]any{"contradiction", "d2", "d1", "newer_replaces_older"} || got[1] != [4]any{"duplicate", "new", "old", "more_specific"} {
		t.Fatalf("%v", got)
	}
	if out := proposals(pairs, []any{map[string]any{"pair": 1, "verdict": "different", "keep": "a", "reason": "same_meaning"}}); len(out) != 0 {
		t.Fatalf("%v", out)
	}
}

type postCall struct {
	path string
	body string
}

func fakePost(t *testing.T) *[]postCall {
	posted := &[]postCall{}
	patch(t, &mPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		*posted = append(*posted, postCall{path, host.MustDumps(body, host.DumpOpts{})})
		return map[string]any{}, nil
	})
	return posted
}

func TestRunPostsIdsAndReasonCodesOnly(t *testing.T) {
	fakeNight(t, maintainItems())
	patch(t, &judgeFn, func([]memPair) ([]any, error) {
		return []any{map[string]any{"pair": 0, "verdict": "duplicate", "keep": "b", "reason": "same_meaning"}}, nil
	})
	posted := fakePost(t)
	found, sent, err := maintainRun(false, io.Discard)
	if err != nil || found != 1 || sent != 1 {
		t.Fatalf("%d %d %v", found, sent, err)
	}
	eq(t, len(*posted), 1)
	p := (*posted)[0]
	eq(t, p.path, "/v1/maintenance_proposals")
	if strings.Contains(p.body, "한국어") { // never memory text
		t.Fatal("memory text posted")
	}
	body, _ := host.Loads(p.body)
	eq(t, obj(obj(body)["proposal"])["keep"], map[string]any{"collection": "memories", "id": "new", "revision": 2})
}

func TestWithoutAModelOnlyNearIdenticalWordingIsProposed(t *testing.T) {
	t.Setenv("ARKISM_MAINTAIN_LLM", "0")
	fakeNight(t, append(maintainItems(), mem("dup", "user", "응답은 한국어로.", 29)))
	posted := fakePost(t)
	if _, _, err := maintainRun(false, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := []any{}
	for _, p := range *posted {
		b, _ := host.Loads(p.body)
		pr := obj(obj(b)["proposal"])
		got = append(got, []any{obj(pr["keep"])["id"], obj(pr["retire"])["id"]})
	}
	eq(t, got, []any{[]any{"dup", "old"}})
}

func sig(samples, passed int) map[string]any {
	return map[string]any{"samples": samples, "passed": passed}
}

// ST-814 / FR-079 / TC-MAINT-010: rules and lessons that often fail on enough samples are proposed, worst first,
// with the signal's counts as evidence; too few samples, other kinds and memories proposed before are left out.
func TestSignalProposalsPostWeakRulesAndLessonsWithEvidence(t *testing.T) {
	items := []map[string]any{mem("r1", "procedural", "PR에 광고 문구 금지", 1), mem("r2", "user", "답은 짧게", 2),
		mem("l1", "lesson", "배포 전 조상 확인", 3), mem("few", "procedural", "표본 적음", 4), mem("ok", "decision", "잘 지킴", 5),
		mem("sem", "semantic", "종류 밖", 6), mem("seen", "lesson", "이미 제안", 7)}
	fakeNight(t, items)
	t.Setenv("ARKISM_MAINTAIN_LLM", "0")
	t.Setenv("ARKISM_MAINTAIN_SIGNAL_MIN_SAMPLES", "10")
	t.Setenv("ARKISM_MAINTAIN_SIGNAL_MAX_PASS_RATE", "0.5")
	patch(t, &signalsFn, func([]map[string]any) (map[string]map[string]any, error) {
		return map[string]map[string]any{"r1": sig(20, 10), "r2": sig(10, 1), "l1": sig(12, 3), "few": sig(9, 0), "ok": sig(10, 9),
			"sem": sig(30, 0), "seen": sig(30, 0)}, nil
	})
	patch(t, &knownPairsFn, func([]map[string]any) (map[string]bool, error) { return map[string]bool{signalKey("seen"): true}, nil })
	posted := fakePost(t)
	found, sent, err := maintainRun(false, io.Discard)
	if err != nil || found != 3 || sent != 3 {
		t.Fatalf("%d %d %v", found, sent, err)
	}
	got := []any{}
	for _, p := range *posted {
		eq(t, p.path, "/v1/maintenance_proposals")
		if strings.Contains(p.body, "광고") || strings.Contains(p.body, "keep") {
			t.Fatal("memory text or a pair in a signal proposal", p.body)
		}
		b, _ := host.Loads(p.body)
		pr := obj(obj(b)["proposal"])
		ev := obj(pr["evidence"])
		got = append(got, []any{pr["type"], obj(pr["memory"])["id"], ev["samples"], ev["passed"], pr["reason"]})
	}
	eq(t, got, []any{[]any{"rule_often_violated", "r2", 10, 1, "low_pass_rate"}, []any{"lesson_often_failing", "l1", 12, 3, "low_pass_rate"},
		[]any{"rule_often_violated", "r1", 20, 10, "low_pass_rate"}})

	// Signal proposals and pair proposals share one limit per morning; with no slot left the model is not asked.
	t.Setenv("ARKISM_MAINTAIN_MAX_PROPOSALS", "2")
	t.Setenv("ARKISM_MAINTAIN_LLM", "1")
	patch(t, &mSearch, func(string, int) ([]any, error) { return []any{map[string]any{"id": "few"}}, nil })
	patch(t, &judgeFn, func([]memPair) ([]any, error) { t.Fatal("judged with no slot left"); return nil, nil })
	*posted = nil
	if found, sent, _ = maintainRun(false, io.Discard); found != 2 || sent != 2 {
		t.Fatal("limit not shared", found, sent)
	}
	t.Setenv("ARKISM_MAINTAIN_LLM", "0")
	t.Setenv("ARKISM_MAINTAIN_MAX_PROPOSALS", "10")

	// A stricter setting keeps only the worst; a token that cannot read signals skips them and the night goes on.
	t.Setenv("ARKISM_MAINTAIN_SIGNAL_MAX_PASS_RATE", "0.2")
	*posted = nil
	if _, sent, _ = maintainRun(false, io.Discard); sent != 1 {
		t.Fatal("max pass rate setting ignored", sent)
	}
	patch(t, &signalsFn, func([]map[string]any) (map[string]map[string]any, error) { return nil, os.ErrPermission })
	if found, sent, err = maintainRun(false, io.Discard); err != nil || found != 0 || sent != 0 {
		t.Fatalf("unreadable signals: %d %d %v", found, sent, err)
	}
}

func TestSignalsAreReadInBatchesOfAHundred(t *testing.T) {
	items := []map[string]any{}
	for i := 0; i < 150; i++ {
		items = append(items, mem("m"+strconv.Itoa(i), "lesson", "x", 1))
	}
	patch(t, &mPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		eq(t, path, "/v1/memory-signals")
		b, _ := host.Loads(host.MustDumps(body, host.DumpOpts{}))
		ids := list(obj(b)["memory_ids"])
		if len(ids) > 100 {
			t.Fatal("batch over 100", len(ids))
		}
		return map[string]any{"items": []any{map[string]any{"memory_id": ids[0], "available": true, "signal": sig(10, 0)},
			map[string]any{"memory_id": ids[1], "available": false, "signal": nil}}}, nil
	})
	got, err := signals(items)
	if err != nil || len(got) != 2 || got["m0"] == nil || got["m100"] == nil {
		t.Fatal(len(got), err)
	}
}
