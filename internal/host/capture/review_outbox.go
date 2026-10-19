package capture

// Classification outbox: the exact classify writes of one decided
// batch are stored before the first is sent, so a lost answer is replayed instead of asking the
// model again. Each write is fenced by expected_revision; the server re-checks authorization and
// revision on every replay. It carries risk calls only, never an approval. This is a protected
// local spool, not an audit or job body.

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const (
	reviewOutboxRetention = 7 * 24 * time.Hour
	reviewOutboxMaxBatch  = 20
)

var (
	reviewPathPattern = regexp.MustCompile(`^/v1/memory_candidates/[A-Za-z0-9_.:-]{1,256}/classify$`)
	errReviewChanged  = valueErr("review_request_changed")
	errReviewInvalid  = valueErr("invalid_review_request")
	errReviewAck      = valueErr("review_ack_invalid")
)

// reviewSend posts one classify write under the reviewer credential.
type reviewSend func(path string, body any) (map[string]any, error)

func reviewOutboxDir() string { return filepath.Join(capState, "review-outbox") }

// reviewBackoff is 60s doubled per failed attempt, at most an hour.
func reviewBackoff(attempts int) time.Duration {
	d := time.Minute
	for i := 1; i < attempts && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// reviewTerminal is a refusal that replaying the same request can never turn into success: the
// candidate is gone, decided or changed. A 403 is not: it stops the run and the write waits.
func reviewTerminal(err error) bool {
	switch host.HTTPCode(err) {
	case 400, 404, 409, 410, 422:
		return true
	}
	return false
}

// Refused credentials and unsafe prepared requests stop without backoff or rewriting.
func reviewFailClosed(err error) bool {
	return errors.Is(err, errReviewerCredential) || errors.Is(err, errReviewInvalid) || host.HTTPCode(err) == 403
}

func validReviewRequest(v any) bool {
	r, _ := v.(map[string]any)
	body, _ := r["body"].(map[string]any)
	path, _ := r["path"].(string)
	rev, ok := intField(body["expected_revision"])
	risk, _ := body["risk"].(string)
	_, known := map[string]bool{"high": true, "medium": true, "low": true}[risk]
	return body != nil && reviewPathPattern.MatchString(path) && ok && rev >= 1 && known
}

func reviewCandidateID(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-2]
}

func intField(v any) (int64, bool) {
	f, ok := num(v)
	if !ok || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}

// checkReviewAck accepts only the server's answer to exactly this write: the same candidate one
// revision on, still waiting, carrying the risk that was sent.
func checkReviewAck(req, res map[string]any) error {
	body := req["body"].(map[string]any)
	want, _ := intField(body["expected_revision"])
	rev, ok := intField(res["revision"])
	k, _ := res["classification"].(map[string]any)
	if !ok || rev != want+1 || strField(res["id"]) != reviewCandidateID(req["path"].(string)) ||
		res["status"] != "candidate" && res["status"] != "conflicted" || k == nil || k["risk"] != body["risk"] {
		return errReviewAck
	}
	return nil
}

// withReviewLock serializes every outbox reader and writer on this host.
func withReviewLock(fn func() error) error {
	dir := reviewOutboxDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "review.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := waitLock(f); err != nil {
		return err
	}
	return fn()
}

func storeReviewRecord(path string, record host.Obj) error {
	if err := atomicWrite(path, host.MustDumps(record, host.DumpOpts{NoASCII: true})); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func removeReviewRecord(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// dispatchReviews stores requests durably, then delivers them in order. A terminal refusal
// retires only that write; a transient failure keeps the rest for replay after a backoff.
func dispatchReviews(requests []map[string]any, send reviewSend) ([]map[string]any, error) {
	if len(requests) < 1 || len(requests) > reviewOutboxMaxBatch {
		return nil, errReviewInvalid
	}
	hashes := make([]any, len(requests))
	stored := make([]any, len(requests))
	for i, r := range requests {
		if !validReviewRequest(r) {
			return nil, errReviewInvalid
		}
		hashes[i], stored[i] = sdigest(r), r
	}
	var results []map[string]any
	err := withReviewLock(func() error {
		path := filepath.Join(reviewOutboxDir(), "batch-"+sdigest(hashes)+".json")
		record, err := readReviewRecord(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			record = host.Obj{{Key: "created_at", Value: timeNow()}, {Key: "requests", Value: stored},
				{Key: "request_hashes", Value: hashes}, {Key: "next_index", Value: 0}, {Key: "attempts", Value: 0}, {Key: "next_at", Value: 0}}
			if err := storeReviewRecord(path, record); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if got, _ := record.Get("request_hashes"); sdigest(got) != sdigest(hashes) {
				return errReviewChanged
			}
		}
		results, err = deliverReviews(path, record, send)
		return err
	})
	return results, err
}

func readReviewRecord(path string) (host.Obj, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := loadsOrdered(raw)
	if err != nil {
		return nil, valueErr("invalid_review_spool")
	}
	o, ok := v.(host.Obj)
	if !ok {
		return nil, valueErr("invalid_review_spool")
	}
	return o, nil
}

func deliverReviews(path string, record host.Obj, send reviewSend) ([]map[string]any, error) {
	get := func(k string) any { v, _ := record.Get(k); return v }
	requests, _ := get("requests").([]any)
	hashes, _ := get("request_hashes").([]any)
	index, ok := intField(get("next_index"))
	if !ok || len(requests) < 1 || len(requests) > reviewOutboxMaxBatch || len(hashes) != len(requests) || index < 0 || index > int64(len(requests)) ||
		filepath.Base(path) != "batch-"+sdigest(hashes)+".json" {
		return nil, valueErr("invalid_review_batch")
	}
	var results []map[string]any
	var terminal error
	for i := index; i < int64(len(requests)); i++ {
		req := plainJSON(requests[i])
		if !validReviewRequest(req) || sdigest(req) != hashes[i] {
			return nil, errReviewChanged
		}
		r := req.(map[string]any)
		res, err := send(r["path"].(string), r["body"])
		if err == nil {
			err = checkReviewAck(r, res)
		}
		if reviewFailClosed(err) {
			return nil, err // retain the prepared bytes until the refusal is resolved
		}
		if err != nil && !reviewTerminal(err) {
			attempts, _ := intField(get("attempts"))
			record.Set("attempts", attempts+1)
			record.Set("next_at", timeNow()+reviewBackoff(int(attempts+1)).Seconds())
			if serr := storeReviewRecord(path, record); serr != nil {
				return nil, serr
			}
			return nil, err
		}
		if err != nil { // denied or stale: retire this write only; later decisions stay independently authorized
			terminal = err
			metric(kv("event", "review_outbox_rejected"), kv("candidate_id", reviewCandidateID(r["path"].(string))), kv("error", errorCode(err)))
			res = nil
		}
		results = append(results, res)
		// Drop the acknowledged private body now, even if a later write needs a retry.
		requests[i] = nil
		record.Set("requests", requests)
		record.Set("next_index", i+1)
		if err := storeReviewRecord(path, record); err != nil {
			return nil, err
		}
	}
	if err := removeReviewRecord(path); err != nil {
		return nil, err
	}
	return results, terminal
}

// plainJSON turns ordered objects from the spool back into plain maps for sending and hashing.
func plainJSON(v any) any {
	switch x := v.(type) {
	case host.Obj:
		m := make(map[string]any, len(x))
		for _, kv := range x {
			m[kv.Key] = plainJSON(kv.Value)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plainJSON(e)
		}
		return out
	}
	return v
}

// replayReviews delivers due spooled writes before any new classification. It returns the candidate ids
// still waiting in the outbox, which the caller must not decide again.
func replayReviews(send reviewSend) (pending map[string]bool, err error) {
	pending = map[string]bool{}
	err = withReviewLock(func() error {
		dir := reviewOutboxDir()
		now := timeNow()
		orphans, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
		for _, o := range orphans { // an interrupted atomic write
			if fi, err := os.Stat(o); err == nil && now-float64(fi.ModTime().Unix()) > reviewOutboxRetention.Seconds() {
				_ = removeReviewRecord(o)
			}
		}
		paths, _ := filepath.Glob(filepath.Join(dir, "batch-*.json"))
		var first error
		for _, path := range paths {
			record, err := readReviewRecord(path)
			if err != nil {
				return err
			}
			created, _ := num(func() any { v, _ := record.Get("created_at"); return v }())
			if age := now - created; age < 0 || age > reviewOutboxRetention.Seconds() {
				metric(kv("event", "review_outbox_expired"))
				if err := removeReviewRecord(path); err != nil {
					return err
				}
				continue
			}
			nextAt, _ := num(func() any { v, _ := record.Get("next_at"); return v }())
			if first == nil && now >= nextAt {
				_, err = deliverReviews(path, record, send)
				if err == nil || reviewTerminal(err) {
					continue
				}
				if reviewFailClosed(err) {
					return err // no new classification until the refused replay is resolved
				}
				if ve := valueErr(""); errors.As(err, &ve) && !errors.Is(err, errReviewAck) {
					return err // a changed or corrupt spool is never sent
				}
				first = err
			}
			reqs, _ := record.Get("requests")
			for _, r := range listOf(plainJSON(reqs)) {
				if m, ok := r.(map[string]any); ok && validReviewRequest(m) {
					pending[reviewCandidateID(m["path"].(string))] = true
				}
			}
		}
		if first != nil {
			metric(kv("event", "review_outbox_retry"), kv("error", errorCode(first)))
		}
		return nil
	})
	return pending, err
}
