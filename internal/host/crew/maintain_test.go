package crew

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
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
	lists := map[string][]any{"open": {prop("p1", "new", "old", 2, 2), prop("p2", "d2", "d1", 2, 1)}, "dismissed": {prop("p3", "d2", "p1", 1, 1)}}
	patch(t, &mGet, func(path string, _ time.Duration) (map[string]any, error) {
		return map[string]any{"items": lists[queryOf(t, path).Get("status")]}, nil
	})
	seen, err := knownPairs(maintainItems())
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(seen), 2) // d1 is at revision 2 now
	if !seen[pairOf("new", "old")] || !seen[pairOf("d2", "p1")] {
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
