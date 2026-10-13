package gateway

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"
)

// resetLimitState forgets every lane, bucket and rest (they are process-wide).
func resetLimitState() {
	modelLanes.mu.Lock()
	modelLanes.m = nil
	modelLanes.mu.Unlock()
	modelBuckets.mu.Lock()
	modelBuckets.m = nil
	modelBuckets.mu.Unlock()
	modelRests.mu.Lock()
	modelRests.m = nil
	modelRests.mu.Unlock()
}

func resetLimits(t *testing.T) {
	resetLimitState()
	t.Cleanup(resetLimitState)
}

func TestLaneConcurrencyQueueAndRefusal(t *testing.T) {
	for _, tc := range []struct {
		name         string
		limit, queue int
		held         int   // slots taken first, never released
		waiting      int   // requests left waiting in the queue
		want         error // the next request's answer, nil = a slot at once
	}{
		{"no limit", 0, 0, 5, 0, nil},
		{"room left", 2, 0, 1, 0, nil},
		{"full, no queue", 2, 0, 2, 0, errLaneFull},
		{"full, queue full", 1, 2, 1, 2, errLaneFull},
		{"full, waits out its turn", 1, 2, 1, 0, errLaneWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var l lanes
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			for range tc.held {
				if _, err := l.take(ctx, "m", tc.limit, tc.queue, time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			for range tc.waiting {
				go l.take(ctx, "m", tc.limit, tc.queue, time.Hour)
			}
			waitFor(t, func() bool {
				l.mu.Lock()
				defer l.mu.Unlock()
				return tc.limit == 0 || len(l.m["m"].queue) == tc.waiting
			})
			_, err := l.take(ctx, "m", tc.limit, tc.queue, 20*time.Millisecond)
			if !errors.Is(err, tc.want) || (err == nil) != (tc.want == nil) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLaneQueueGetsReleasedSlotInOrder(t *testing.T) {
	var l lanes
	ctx := context.Background()
	release, err := l.take(ctx, "m", 1, 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan int, 2)
	for i := range 2 {
		go func() {
			r, err := l.take(ctx, "m", 1, 2, time.Second)
			if err != nil {
				got <- -1
				return
			}
			got <- i
			r()
		}()
		waitFor(t, func() bool { l.mu.Lock(); defer l.mu.Unlock(); return len(l.m["m"].queue) == i+1 })
	}
	release()
	release() // twice is once
	if a, b := <-got, <-got; a != 0 || b != 1 {
		t.Fatalf("order %d %d", a, b)
	}
	waitFor(t, func() bool { l.mu.Lock(); defer l.mu.Unlock(); return len(l.m) == 0 })
}

func TestRPMBucketRefills(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	type step struct {
		at    time.Duration // after t0
		ok    bool
		after time.Duration // the Retry-After when refused
	}
	for _, tc := range []struct {
		name  string
		rpm   int
		steps []step
	}{
		{"no limit", 0, []step{{0, true, 0}, {0, true, 0}, {0, true, 0}}},
		{"burst then wait a token", 2, []step{{0, true, 0}, {0, true, 0}, {0, false, 30 * time.Second},
			{29 * time.Second, false, time.Second}, {30 * time.Second, true, 0}, {30 * time.Second, false, 30 * time.Second}}},
		{"refill caps at the limit", 2, []step{{0, true, 0}, {0, true, 0}, {10 * time.Minute, true, 0}, {10 * time.Minute, true, 0},
			{10 * time.Minute, false, 30 * time.Second}}},
		{"60 a minute is one a second", 60, append(slices.Repeat([]step{{0, true, 0}}, 60), step{0, false, time.Second},
			step{500 * time.Millisecond, false, 500 * time.Millisecond}, step{time.Second, true, 0})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b buckets
			for i, s := range tc.steps {
				ok, after := b.take("m", tc.rpm, t0.Add(s.at))
				if ok != s.ok || after != s.after {
					t.Fatalf("step %d: got %v %v, want %v %v", i, ok, after, s.ok, s.after)
				}
			}
		})
	}
}

func TestRestOrderAndProbeAfterExpiry(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	ids := []string{"a/pick", "b/next", "c/last"}
	type op struct {
		kind   string // fail (429), credit (402), ok, other (500), claim, order
		id     string
		at     time.Duration
		after  time.Duration // Retry-After for fail
		expect []string      // for order
	}
	for _, tc := range []struct {
		name string
		ops  []op
	}{
		{"429 rests a minute", []op{
			{kind: "fail", id: "a/pick"},
			{kind: "order", at: 59 * time.Second, expect: []string{"b/next", "c/last", "a/pick"}},
			{kind: "order", at: time.Minute, expect: ids},
		}},
		{"Retry-After sets the rest", []op{
			{kind: "fail", id: "b/next", after: 10 * time.Second},
			{kind: "order", at: 9 * time.Second, expect: []string{"a/pick", "c/last", "b/next"}},
			{kind: "order", at: 10 * time.Second, expect: ids},
		}},
		{"credit rests 30 minutes", []op{
			{kind: "credit", id: "a/pick"},
			{kind: "order", at: 29 * time.Minute, expect: []string{"b/next", "c/last", "a/pick"}},
			{kind: "order", at: 30 * time.Minute, expect: ids},
		}},
		{"one probe at a time, success returns it", []op{
			{kind: "fail", id: "a/pick"},
			{kind: "claim", id: "a/pick", at: 2 * time.Minute},
			{kind: "order", at: 2 * time.Minute, expect: []string{"b/next", "c/last", "a/pick"}},
			{kind: "ok", id: "a/pick", at: 2 * time.Minute},
			{kind: "order", at: 2 * time.Minute, expect: ids},
		}},
		{"a failed probe rests again", []op{
			{kind: "fail", id: "a/pick"},
			{kind: "claim", id: "a/pick", at: 2 * time.Minute},
			{kind: "fail", id: "a/pick", at: 2 * time.Minute},
			{kind: "order", at: 2*time.Minute + 59*time.Second, expect: []string{"b/next", "c/last", "a/pick"}},
			{kind: "order", at: 3 * time.Minute, expect: ids},
		}},
		{"a probe ending otherwise clears the rest", []op{
			{kind: "fail", id: "a/pick"},
			{kind: "claim", id: "a/pick", at: 2 * time.Minute},
			{kind: "other", id: "a/pick", at: 2 * time.Minute},
			{kind: "order", at: 2 * time.Minute, expect: ids},
		}},
		{"a claim inside the rest is no probe", []op{
			{kind: "fail", id: "a/pick"},
			{kind: "claim", id: "a/pick", at: 30 * time.Second},
			{kind: "order", at: time.Minute, expect: ids},
		}},
		{"other failures do not rest", []op{
			{kind: "other", id: "a/pick"},
			{kind: "order", expect: ids},
		}},
		{"several resting keep their order", []op{
			{kind: "fail", id: "a/pick"},
			{kind: "fail", id: "b/next"},
			{kind: "order", at: time.Second, expect: []string{"c/last", "a/pick", "b/next"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r rests
			for i, o := range tc.ops {
				at := t0.Add(o.at)
				switch o.kind {
				case "fail":
					r.settle(o.id, true, 429, "upstream_http_429", o.after, at)
				case "credit":
					r.settle(o.id, true, 402, "upstream_http_402", 0, at)
				case "ok":
					r.settle(o.id, true, 200, "", 0, at)
				case "other":
					r.settle(o.id, true, 500, "upstream_http_500", 0, at)
				case "claim":
					r.claim(o.id, at)
				case "order":
					if got := r.order(ids, at); !slices.Equal(got, o.expect) {
						t.Fatalf("op %d: %v, want %v", i, got, o.expect)
					}
				}
			}
		})
	}
}

func TestRetryAfterHeader(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	for _, tc := range []struct {
		v    string
		want time.Duration
	}{
		{"", 0}, {"0", 0}, {"-3", 0}, {"junk", 0}, {"12", 12 * time.Second}, {"999999999", restMax},
		{at.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second}, {at.Add(-time.Hour).Format(http.TimeFormat), 0},
	} {
		if got := retryAfterHeader(tc.v, at); got != tc.want {
			t.Fatalf("%q: %v, want %v", tc.v, got, tc.want)
		}
	}
}

// Through the gateway: a lone model's RPM answers 429 with Retry-After; a group skips a full member.
func TestGatewayEnforcesModelLimits(t *testing.T) {
	f := newFixture(t)
	t0 := time.Unix(1_800_000_000, 0)
	swap(t, &clock, func() time.Time { return t0 })
	swap(t, &modelLimits, func(id string) (int, int) {
		switch id {
		case "custom-test/first":
			return 1, 0
		}
		return 0, 0
	})
	if r := f.post(""); r.status != 200 {
		t.Fatalf("first: %d %s", r.status, r.body)
	}
	r := f.post("")
	if r.status != 429 || r.header.Get("Retry-After") != "60" || at(parsed(t, r.body), "error", "code") != "model_rate_limited" || f.seenCount() != 1 {
		t.Fatalf("second: %d %q %s seen %d", r.status, r.header.Get("Retry-After"), r.body, f.seenCount())
	}
	// The group's pick has no token left: its backup answers, and no call is reported for the pick.
	forget(f)
	r = f.post(`{"model":"group/test","messages":[{"role":"user","content":"안녕"}]}`)
	if r.status != 200 || f.seenCount() != 1 || f.reportCount() != 1 || f.reports[0]["model"] != "custom-test/backup" ||
		f.reports[0]["decision"] != "failover" || f.reports[0]["replaced"] != "" {
		t.Fatalf("group: %d %s reports %v", r.status, r.body, f.reports)
	}
	// Every member limited: 429 with the soonest Retry-After.
	swap(t, &modelLimits, func(string) (int, int) { return 1, 0 })
	if r := f.post(`{"model":"group/test","messages":[{"role":"user","content":"안녕"}]}`); r.status != 200 {
		t.Fatalf("backup's own token: %d %s", r.status, r.body)
	}
	forget(f)
	if r := f.post(`{"model":"group/test","messages":[{"role":"user","content":"안녕"}]}`); r.status != 429 || r.header.Get("Retry-After") != "60" || f.seenCount() != 0 {
		t.Fatalf("all limited: %d %q %s", r.status, r.header.Get("Retry-After"), r.body)
	}
}

// Through the gateway: a 429 rests the pick, so the next group request starts at the backup.
func TestGatewayRestsAModelAfter429(t *testing.T) {
	f := newFixture(t)
	t0 := time.Unix(1_800_000_000, 0)
	clockAt := t0
	swap(t, &clock, func() time.Time { return clockAt })
	f.replies["first"] = upstreamReply{429, "application/json", []byte(`{"error":"slow down"}`)}
	group := `{"model":"group/test","messages":[{"role":"user","content":"안녕"}]}`
	if r := f.post(group); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	forget(f)
	delete(f.replies, "first")
	if r := f.post(group); r.status != 200 || f.seenCount() != 1 || f.reports[0]["model"] != "custom-test/backup" {
		t.Fatalf("resting pick tried first: %d reports %v", r.status, f.reports)
	}
	clockAt = t0.Add(rest429)
	forget(f)
	if r := f.post(group); r.status != 200 || f.seenCount() != 1 || f.reports[0]["model"] != "custom-test/first" || f.reports[0]["decision"] != "fixed" {
		t.Fatalf("probe after the rest: %d reports %v", r.status, f.reports)
	}
	if modelRests.resting("custom-test/first", clockAt) {
		t.Fatal("a good probe keeps the rest")
	}
}

// forget clears what the fixture saw but, unlike clear, keeps the limits and rests.
func forget(f *fixture) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen, f.reports = nil, nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out")
}
