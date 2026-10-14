package gateway

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// What 라우팅 설정 sets per model, enforced where requests pass:
//   - concurrency: at most that many requests out at once; a few more wait in a short queue,
//     and one more is answered 429 with Retry-After (model_busy);
//   - rpm_limit: a token bucket of that size refilled over a minute; an empty bucket is
//     answered 429 with Retry-After when its next token comes (model_rate_limited);
//   - rest: a quota, credit or 429 failure rests the model. While it rests a group tries it
//     last; once the rest is over the first request to reach it is its one probe, and the
//     others keep it last until that probe ends (success clears the rest).
// A group skips a member with no room now for the next one (no call is made, so none is
// reported); only its last candidate waits in the queue. Limits and rests live in this
// process: a restart forgets them.

const (
	laneQueue  = 4                // requests that may wait for a slot of one model
	laneWait   = 30 * time.Second // the longest one waits
	busyAfter  = 5 * time.Second  // Retry-After for a full queue
	rest429    = time.Minute      // a 429 without Retry-After
	restCredit = 30 * time.Minute // credit or quota gone, no Retry-After
	restMax    = 24 * time.Hour
)

var (
	errLaneFull = errors.New("model_busy")
	errLaneWait = errors.New("model_busy")
	errRPM      = errors.New("model_rate_limited")
)

// Test seams: the clock, and a model's limits from the 라우팅 listing (0 is no limit).
var (
	clock       = time.Now
	modelLimits = func(id string) (rpm, concurrency int) {
		for _, m := range routingModels() {
			if get(m, "id") != id {
				continue
			}
			return limitOf(get(m, "rpm_limit"), 1e6), limitOf(get(m, "concurrency"), 1e4)
		}
		return 0, 0
	}
)

func limitOf(v any, most int64) int {
	n, ok := limitInt(v)
	if !ok || n < 1 || n > most {
		return 0
	}
	return int(n)
}

func limitInt(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), x == math.Trunc(x)
	case nil:
		return 0, false
	}
	return host.Int(v)
}

// ------------------------------------------------------------------ concurrency

type lanes struct {
	mu sync.Mutex
	m  map[string]*lane
}

type lane struct {
	busy  int
	queue []chan struct{}
}

// take holds one of id's limit slots. queue is how many may wait (0: none, the request is
// turned away when no slot is free), each at most wait. The release is safe to call twice.
func (l *lanes) take(ctx context.Context, id string, limit, queue int, wait time.Duration) (func(), error) {
	if limit <= 0 {
		return func() { /* no limit: nothing was taken, nothing to release */ }, nil
	}
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*lane{}
	}
	ln := l.m[id]
	if ln == nil {
		ln = &lane{}
		l.m[id] = ln
	}
	l.grant(ln, limit)
	if ln.busy < limit && len(ln.queue) == 0 {
		ln.busy++
		l.mu.Unlock()
		return l.releaser(id, ln, limit), nil
	}
	if len(ln.queue) >= queue {
		l.mu.Unlock()
		return nil, errLaneFull
	}
	ch := make(chan struct{})
	ln.queue = append(ln.queue, ch)
	l.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var err error
	select {
	case <-ch:
		return l.releaser(id, ln, limit), nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = errLaneWait
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, c := range ln.queue {
		if c == ch {
			ln.queue = append(ln.queue[:i], ln.queue[i+1:]...)
			l.drop(id, ln)
			return nil, err
		}
	}
	// The slot came as it gave up: hand it on.
	ln.busy--
	l.grant(ln, limit)
	l.drop(id, ln)
	return nil, err
}

func (l *lanes) releaser(id string, ln *lane, limit int) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			ln.busy--
			l.grant(ln, limit)
			l.drop(id, ln)
		})
	}
}

func (l *lanes) grant(ln *lane, limit int) {
	for len(ln.queue) > 0 && ln.busy < limit {
		ln.busy++
		close(ln.queue[0])
		ln.queue = ln.queue[1:]
	}
}

func (l *lanes) drop(id string, ln *lane) {
	if ln.busy <= 0 && len(ln.queue) == 0 && l.m[id] == ln {
		delete(l.m, id)
	}
}

// ------------------------------------------------------------------ requests per minute

type buckets struct {
	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

// take spends one of id's tokens: rpm a minute, at most rpm saved up. When none is left it
// answers how long until the next one.
func (b *buckets) take(id string, rpm int, at time.Time) (bool, time.Duration) {
	if rpm <= 0 {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*bucket{}
	}
	k := b.m[id]
	if k == nil {
		k = &bucket{tokens: float64(rpm), at: at}
		b.m[id] = k
	}
	perSec := float64(rpm) / 60
	if at.After(k.at) {
		k.tokens += at.Sub(k.at).Seconds() * perSec
		k.at = at
	}
	k.tokens = min(k.tokens, float64(rpm))
	if k.tokens >= 1 {
		k.tokens--
		return true, 0
	}
	return false, time.Duration(math.Ceil((1 - k.tokens) / perSec * float64(time.Second)))
}

// ------------------------------------------------------------------ rest after failures

type rests struct {
	mu sync.Mutex
	m  map[string]*rest
}

type rest struct {
	until   time.Time
	probing bool
}

// restFor is how long a failure rests its model: credit or quota until Retry-After (else 30
// minutes), a 429 until Retry-After (else a minute). Other failures do not rest it.
func restFor(status int, code string, retryAfter time.Duration) (time.Duration, bool) {
	code = strings.ToLower(code)
	d := time.Duration(0)
	switch {
	case status == http.StatusPaymentRequired || strings.Contains(code, "credit") || strings.Contains(code, "quota"):
		d = restCredit
	case status == http.StatusTooManyRequests || code == "upstream_rate_limit" || code == "upstream_http_429":
		d = rest429
	default:
		return 0, false
	}
	if retryAfter > 0 {
		d = retryAfter
	}
	return min(d, restMax), true
}

// resting says whether id goes last now: inside its rest, or past it with its probe out.
func (r *rests) resting(id string, at time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.m[id]
	return s != nil && (at.Before(s.until) || s.probing)
}

// order keeps the candidates' order with the resting ones moved last.
func (r *rests) order(ids []string, at time.Time) []string {
	var ready, resting []string
	for _, id := range ids {
		if r.resting(id, at) {
			resting = append(resting, id)
		} else {
			ready = append(ready, id)
		}
	}
	return append(ready, resting...)
}

// claim marks the request about to reach id as its probe when id's rest is over.
func (r *rests) claim(id string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.m[id]; s != nil && !at.Before(s.until) {
		s.probing = true
	}
}

// settle records how a request to id ended: called says whether it reached the provider.
func (r *rests) settle(id string, called bool, status int, code string, retryAfter time.Duration, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*rest{}
	}
	s := r.m[id]
	if d, ok := restFor(status, code, retryAfter); called && ok {
		r.m[id] = &rest{until: at.Add(d)}
		return
	}
	if s == nil {
		return
	}
	s.probing = false
	if called && (status >= 200 && status < 300 && code == "" || !at.Before(s.until)) {
		delete(r.m, id) // it answered, or its probe failed for some other reason
	}
}

// retryAfterHeader reads a Retry-After value: seconds or an HTTP date; 0 when absent or bad.
func retryAfterHeader(v string, at time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			return 0
		}
		return min(time.Duration(n)*time.Second, restMax)
	}
	if t, err := http.ParseTime(v); err == nil && t.After(at) {
		return min(t.Sub(at), restMax)
	}
	return 0
}

func retrySeconds(d time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(d.Seconds()))))
}

// ------------------------------------------------------------------ the gateway's

var (
	modelLanes   lanes
	modelBuckets buckets
	modelRests   rests
)

// admit takes id's slot and token. wait says whether it may queue. On refusal it answers the
// error and the Retry-After to give.
func admit(ctx context.Context, id string, wait bool) (func(), time.Duration, error) {
	rpm, concurrency := modelLimits(id)
	queue := 0
	if wait {
		queue = laneQueue
	}
	release, err := modelLanes.take(ctx, id, concurrency, queue, laneWait)
	if err != nil {
		return nil, busyAfter, err
	}
	if ok, after := modelBuckets.take(id, rpm, clock()); !ok {
		release()
		return nil, after, errRPM
	}
	return release, 0, nil
}

// refuseLimit answers a request a model's limit turned away: 429 with Retry-After.
func (x *exchange) refuseLimit(err error, after time.Duration) error {
	code := errLaneFull.Error()
	if errors.Is(err, errRPM) {
		code = errRPM.Error()
	} else if !errors.Is(err, errLaneFull) && !errors.Is(err, errLaneWait) {
		return err // the caller left while it waited: drop the connection
	}
	x.w.Header().Set("Retry-After", retrySeconds(after))
	return x.inputError(http.StatusTooManyRequests, code)
}
