package crew

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type digestFake struct {
	plans   map[string]map[string]any
	posts   map[string][]host.Obj
	verdict string
	calls   []string
	judged  map[string]any
}

func installDigestFake(t *testing.T, f *digestFake) {
	t.Helper()
	p, a, post, metric, model := host.AccountsProfiles, host.AccountsAccount, mPost, mMetric, digestModelFn
	t.Cleanup(func() {
		host.AccountsProfiles, host.AccountsAccount, mPost, mMetric, digestModelFn = p, a, post, metric, model
	})
	host.AccountsProfiles = func(string, string, string) []map[string]any { return nil }
	mMetric = func(map[string]any) {}
	f.posts = map[string][]host.Obj{}
	mPost = func(path string, payload any, _ time.Duration) (map[string]any, error) {
		body := payload.(host.Obj)
		f.posts[path] = append(f.posts[path], body)
		switch path {
		case "/v1/digests/plan":
			kind, _ := body.Get("kind")
			return f.plans[kind.(string)], nil
		case "/v1/digests/user/judge":
			return f.judged, nil
		}
		return map[string]any{"id": "dg_x", "status": "candidate"}, nil
	}
	digestModelFn = func(model, system, schema, input string) (map[string]any, host.Obj, error) {
		f.calls = append(f.calls, model+":"+strings.SplitN(input, "\n", 2)[0])
		usage := host.Obj{{Key: "model", Value: "claude-" + model + "-5"}, {Key: "requests", Value: 1}, {Key: "input_tokens", Value: 100}, {Key: "output_tokens", Value: 20}}
		if strings.Contains(schema, "verdict") {
			return map[string]any{"verdict": f.verdict}, usage, nil
		}
		return map[string]any{"title": "배포", "body": "배포는 staging 먼저."}, usage, nil
	}
}

func digestMem(id string) map[string]any {
	return map[string]any{"ref": map[string]any{"collection": "memories", "id": id, "revision": 1}, "kind": "user", "text": "답은 한국어로", "created_at": "2026-10-01T00:00:00Z"}
}

func TestDigestsUserNoneWritesNothing(t *testing.T) {
	f := &digestFake{verdict: "none", judged: map[string]any{"regenerate": false}, plans: map[string]map[string]any{
		"user": {"new": []any{digestMem("m1")}, "members": []any{digestMem("m1")}},
	}}
	installDigestFake(t, f)
	var out, errOut bytes.Buffer
	if code := digestsMain([]string{"run", "--user"}, nil, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	if len(f.posts["/v1/digests/user/judge"]) != 1 || len(f.posts["/v1/digests/proposals"]) != 0 {
		t.Fatal("none posted a candidate", f.posts)
	}
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "haiku:") {
		t.Fatal("judge should be the only (cheap) model call", f.calls)
	}
}

func TestDigestsUserConflictProposesCandidate(t *testing.T) {
	f := &digestFake{verdict: "conflict", judged: map[string]any{"regenerate": true, "pending": "conflict"}, plans: map[string]map[string]any{
		"user": {"new": []any{digestMem("m2")}, "members": []any{digestMem("m1"), digestMem("m2")}, "current": map[string]any{"id": "dg_cur", "revision": 2, "body": "한국어"}},
	}}
	installDigestFake(t, f)
	var out, errOut bytes.Buffer
	if code := digestsMain([]string{"run", "--user"}, nil, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	posts := f.posts["/v1/digests/proposals"]
	if len(posts) != 1 {
		t.Fatal("no candidate", f.posts)
	}
	replaces, _ := posts[0].Get("replaces")
	members, _ := posts[0].Get("members")
	if len(replaces.([]any)) != 1 || len(members.([]any)) != 2 {
		t.Fatal("candidate must replace the current summary", replaces, members)
	}
	// Both model calls go to the ledger with what they made: the judge with its verdict, the writer with the proposal.
	for _, body := range []host.Obj{f.posts["/v1/digests/user/judge"][0], posts[0]} {
		if u, ok := body.Get("usage"); !ok || u.(host.Obj)[0].Value == "" {
			t.Fatal("model usage not sent", body)
		}
	}
	judge, _ := f.posts["/v1/digests/user/judge"][0].Get("usage")
	if m := judge.(host.Obj)[0].Value; m != "claude-haiku-5" {
		t.Fatal("judge usage model", m)
	}
}

func TestDigestsTopicsAndMerge(t *testing.T) {
	f := &digestFake{plans: map[string]map[string]any{
		"topic": {"clusters": []any{map[string]any{"action": "new", "members": []any{digestMem("a"), digestMem("b")}}},
			"merge": map[string]any{"digests": []any{map[string]any{"id": "d1", "revision": 1, "title": "x", "body": "y"}, map[string]any{"id": "d2", "revision": 1, "title": "z", "body": "w"}},
				"members": []any{digestMem("a"), digestMem("c")}}},
	}}
	installDigestFake(t, f)
	var out, errOut bytes.Buffer
	if code := digestsMain([]string{"run", "--topics"}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "posted 2") {
		t.Fatal(code, out.String(), errOut.String())
	}
	merge := f.posts["/v1/digests/proposals"][1]
	if r, _ := merge.Get("replaces"); len(r.([]any)) != 2 {
		t.Fatal("merge must replace both digests", r)
	}
	// dry run posts nothing.
	f.posts = map[string][]host.Obj{}
	out.Reset()
	digestsMain([]string{"run", "--topics", "--dry-run"}, nil, &out, &errOut)
	if len(f.posts["/v1/digests/proposals"]) != 0 || !strings.Contains(out.String(), "would propose") {
		t.Fatal("dry run posted", f.posts)
	}
}

func TestDigestsWaitWhenClaudeUsageIsHigh(t *testing.T) {
	f := &digestFake{plans: map[string]map[string]any{}}
	installDigestFake(t, f)
	t.Setenv("ARKISM_LLM_BUDGET_PCT", "")
	host.AccountsProfiles = func(string, string, string) []map[string]any {
		return []map[string]any{{"id": "claude", "cli": "claude"}}
	}
	host.AccountsAccount = func(map[string]any, string, string, bool) (map[string]any, error) {
		return map[string]any{"usage_status": "ok", "quotas": []any{map[string]any{"window": "5h", "left_pct": 5, "resets_at": "2999-01-01T00:00:00+00:00"}}}, nil
	}
	var out, errOut bytes.Buffer
	if code := digestsMain([]string{"run"}, nil, &out, &errOut); code != 0 || len(f.posts) != 0 || len(f.calls) != 0 || !strings.Contains(out.String(), "95%") {
		t.Fatal(code, f.posts, out.String())
	}
}
