package crew

// Nightly memory maintenance proposals.
//
//	run [--dry-run]   find pairs of active memories that say the same thing, or where a newer one contradicts an
//	                  older one, and post proposals to Arkism. Nothing changes until a person decides in the desktop
//	                  할 일 (accept, swap or keep both).
//
// Pairs come from Arkism search (words + meaning) and a letter-pair overlap, within one kind. A model judges each
// pair; without one (ARKISM_MAINTAIN_LLM=0) only near-identical wording is proposed. A contradiction always keeps the
// newer memory. Proposals carry ids and a reason code, never memory text; the local log keeps counts only.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
)

const maintainDoc = `Nightly memory maintenance proposals.

  run [--dry-run]   find pairs of active memories that say the same thing, or where a newer one
                    contradicts an older one, and post proposals to Arkism. Nothing changes until
                    a person decides in the desktop 할 일 (accept, swap or keep both).`

var maintainKinds = []string{"user", "procedural", "decision", "lesson"}

func envInt(name string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(host.Env(name, strconv.Itoa(def))))
	if err != nil {
		return def
	}
	return n
}

func maxPerKind() int   { return envInt("ARKISM_MAINTAIN_MAX_PER_KIND", 1000) } // memories read per kind
func maxPairs() int     { return envInt("ARKISM_MAINTAIN_MAX_PAIRS", 30) }      // judged per night
func maxProposals() int { return envInt("ARKISM_MAINTAIN_MAX_PROPOSALS", 10) }  // what a person sees per morning
func useLLM() bool      { return host.Env("ARKISM_MAINTAIN_LLM", "1") == "1" }
func maintainModel() string {
	return host.Env("ARKISM_MAINTAIN_MODEL", host.HarnessCLIModel("consolidator", "sonnet"))
}

const (
	nearScore  = 0.85 // letter-pair overlap treated as the same sentence without a model
	looseScore = 0.5  // letter-pair overlap worth a model's look even when search missed it
	judgedMax  = 5000 // pairs kept in the local "judged different" list
)

const maintainPrompt = `You review pairs of saved memories for one person's AI agents. For each pair decide:
- "duplicate": both say the same thing (maybe in other words). Keep the clearer or more specific one.
- "contradiction": they cannot both be followed now; the newer one is the person's current view.
- "different": anything else, including related but separate rules, or one being a narrower case of the other
  that is still useful on its own. When unsure, answer "different".
reason: "same_meaning" for a duplicate kept for clarity, "more_specific" when the kept one adds detail the other
lacks, "newer_replaces_older" for a contradiction. keep: "a" or "b". Judge only from the texts and dates given.`

var maintainSchema = host.MustDumps(host.Obj{{Key: "type", Value: "object"}, {Key: "additionalProperties", Value: false},
	{Key: "required", Value: []any{"pairs"}}, {Key: "properties", Value: host.Obj{{Key: "pairs", Value: host.Obj{
		{Key: "type", Value: "array"}, {Key: "items", Value: host.Obj{{Key: "type", Value: "object"}, {Key: "additionalProperties", Value: false},
			{Key: "required", Value: []any{"pair", "verdict", "keep", "reason"}},
			{Key: "properties", Value: host.Obj{{Key: "pair", Value: host.Obj{{Key: "type", Value: "integer"}}},
				{Key: "verdict", Value: host.Obj{{Key: "type", Value: "string"}, {Key: "enum", Value: []any{"duplicate", "contradiction", "different"}}}},
				{Key: "keep", Value: host.Obj{{Key: "type", Value: "string"}, {Key: "enum", Value: []any{"a", "b"}}}},
				{Key: "reason", Value: host.Obj{{Key: "type", Value: "string"}, {Key: "enum", Value: []any{"same_meaning", "more_specific", "newer_replaces_older"}}}}}}}}}}}}},
	host.DumpOpts{})

// Arkism calls, search, metrics and the judge (fakes in tests).
var (
	mGet    = host.Get
	mPost   = host.Post
	mSearch = func(query string, limit int) ([]any, error) { return host.Search(query, limit, 60*time.Second) }
	mMetric = func(fields map[string]any) { _ = host.Metric(fields) }
	judgeFn = judge
	// the night's inputs (fakes in tests)
	memoriesFn   = memories
	knownPairsFn = knownPairs
)

var notWordish = regexp.MustCompile(`[^0-9a-z가-힣]`)

func bigrams(text string) map[string]int {
	s := []rune(notWordish.ReplaceAllString(strings.ToLower(text), ""))
	out := map[string]int{}
	for i := 0; i+1 < len(s); i++ {
		out[string(s[i:i+2])]++
	}
	return out
}

func dice(a, b map[string]int) float64 {
	total, common := 0, 0
	for _, v := range a {
		total += v
	}
	for _, v := range b {
		total += v
	}
	if total == 0 {
		return 0
	}
	for k, v := range a {
		common += min(v, b[k])
	}
	return 2 * float64(common) / float64(total)
}

var isoLayouts = []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999", "2006-01-02T15:04Z07:00", "2006-01-02T15:04", "2006-01-02"}

// when is the time a memory took effect: comparing RFC 3339 strings puts 12:00:00.100Z before 12:00:00Z.
func when(m map[string]any) time.Time {
	raw := sOr(m["effective_at"])
	if raw == "" {
		raw = sOr(m["created_at"])
	}
	raw = strings.Replace(raw, "Z", "+00:00", 1)
	for _, layout := range isoLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// query is the full scope, then extra pairs, URL-encoded in that order (empty scope fields included).
func query(extra ...string) string {
	parts := []string{}
	for _, kv := range scope() {
		parts = append(parts, quotePlus(kv.Key)+"="+quotePlus(pyStr(kv.Value)))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, quotePlus(extra[i])+"="+quotePlus(extra[i+1]))
	}
	return strings.Join(parts, "&")
}

// memories is the active memories of each kind, following next_cursor up to the per-kind limit.
func memories() ([]map[string]any, error) {
	items := []map[string]any{}
	limit := maxPerKind()
	for _, kind := range maintainKinds {
		cursor, got := "", 0
		for got < limit {
			extra := []string{"status", "active", "kind", kind, "limit", "100"}
			if cursor != "" {
				extra = append(extra, "cursor", cursor)
			}
			page, err := mGet("/v1/memories?"+query(extra...), 30*time.Second)
			if err != nil {
				return nil, err
			}
			batch := []map[string]any{}
			for _, m := range list(page["items"]) {
				if truthy(obj(m)["text"]) && truthy(obj(m)["id"]) {
					batch = append(batch, obj(m))
				}
			}
			items = append(items, batch[:min(len(batch), max(0, limit-got))]...)
			got += len(batch)
			cursor = sOr(page["next_cursor"])
			if cursor == "" {
				break
			}
		}
	}
	return items, nil
}

// pairOf is a frozenset of two ids.
func pairOf(a, b any) string {
	x, y := pyStr(a), pyStr(b)
	if y < x {
		x, y = y, x
	}
	return x + "\x00" + y
}

// knownPairs is the pairs already proposed in any state but stale: never asked twice. An open proposal for a
// memory edited since is not counted: proposing the pair again replaces it (the server marks the old one stale).
func knownPairs(items []map[string]any) (map[string]bool, error) {
	rev := map[string]any{}
	for _, m := range items {
		rev[pyStr(m["id"])] = m["revision"]
	}
	seen := map[string]bool{}
	for _, status := range []string{"open", "accepted", "swapped", "dismissed"} {
		page, err := mGet("/v1/maintenance_proposals?"+query("status", status), 30*time.Second)
		if err != nil {
			return nil, err
		}
		for _, p := range list(page["items"]) {
			m := obj(obj(p)["maintenance"])
			keep, retire := obj(m["keep"]), obj(m["retire"])
			stale := false
			for _, r := range []map[string]any{keep, retire} {
				if id, ok := r["id"].(string); ok {
					if have, ok := rev[id]; ok && !same(have, r["revision"]) {
						stale = true
					}
				}
			}
			if status == "open" && stale {
				continue
			}
			seen[pairOf(keep["id"], retire["id"])] = true
		}
	}
	return seen, nil
}

type scored struct {
	order []string
	score map[string]float64
	ids   map[string][2]string
}

func (s *scored) raise(a, b string, v float64) {
	k := pairOf(a, b)
	if _, ok := s.score[k]; !ok {
		s.order = append(s.order, k)
		s.ids[k] = [2]string{a, b}
	}
	s.score[k] = max(s.score[k], v)
}

type memPair struct {
	older, newer map[string]any
	score        float64
}

// candidatePairs is same-kind pairs, most alike first: search hits (words and meaning) plus letter-pair overlap.
func candidatePairs(items []map[string]any, seen map[string]bool, search func(string, int) ([]any, error)) []memPair {
	if search == nil {
		search = mSearch
	}
	byID := map[string]map[string]any{}
	grams := map[string]map[string]int{}
	for _, m := range items {
		id := pyStr(m["id"])
		byID[id], grams[id] = m, bigrams(sOr(m["text"]))
	}
	s := &scored{score: map[string]float64{}, ids: map[string][2]string{}}
	// Search links (words and meaning): at least looseScore, so the model sees them.
	for _, m := range items {
		id := pyStr(m["id"])
		hits, err := search(clipRunes(sOr(m["text"]), 1000), 6)
		if err != nil { // lexical overlap alone still finds the obvious pairs
			mMetric(map[string]any{"event": "maintain_error", "step": "search", "error": errName(err)})
			hits = nil
		}
		for _, h := range hits {
			other, ok := byID[pyStr(obj(h)["id"])]
			if ok && pyStr(other["id"]) != id && same(other["kind"], m["kind"]) {
				s.raise(id, pyStr(other["id"]), max(dice(grams[id], grams[pyStr(other["id"])]), looseScore))
			}
		}
	}
	for i, a := range items {
		for _, b := range items[i+1:] {
			if !same(a["kind"], b["kind"]) {
				continue
			}
			if v := dice(grams[pyStr(a["id"])], grams[pyStr(b["id"])]); v >= looseScore {
				s.raise(pyStr(a["id"]), pyStr(b["id"]), v)
			}
		}
	}
	pairs := []memPair{}
	for _, k := range s.order {
		if seen[k] {
			continue
		}
		a, b := byID[s.ids[k][0]], byID[s.ids[k][1]]
		// shortcut: Python breaks a tie in time by set order; here the id decides.
		if wa, wb := when(a), when(b); wb.Before(wa) || (wa.Equal(wb) && pyStr(b["id"]) < pyStr(a["id"])) {
			a, b = b, a
		}
		pairs = append(pairs, memPair{a, b, s.score[k]})
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })
	return pairs[:min(len(pairs), maxPairs())]
}

// judge is one model call for all pairs: [{pair, verdict, keep, reason}].
func judge(pairs []memPair) ([]any, error) {
	lines := []string{}
	for i, p := range pairs {
		lines = append(lines, fmt.Sprintf("PAIR %d (kind %s)\n a (%s): %s\n b (%s): %s", i, pyStr(p.older["kind"]),
			when(p.older).Format("2006-01-02"), clipRunes(sOr(p.older["text"]), 600), when(p.newer).Format("2006-01-02"), clipRunes(sOr(p.newer["text"]), 600)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, host.ClaudeBin, "-p", "--model", maintainModel(), "--output-format", "json", "--system-prompt", maintainPrompt,
		"--tools", "", "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence", "--json-schema", maintainSchema)
	env := osEnv()
	env["ARKISM_CAPTURE_CHILD"] = "1"
	cmd.Env, cmd.Dir, cmd.Stdin = envList(env), host.CaptureState, strings.NewReader(strings.Join(lines, "\n\n"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && ctx.Err() == nil {
			return nil, fmt.Errorf("judge_exit_%d", exitCode(exit.ProcessState))
		}
		return nil, err
	}
	v, err := host.Loads(out.String())
	if err != nil {
		return nil, err
	}
	res := obj(v)
	so, ok := res["structured_output"].(map[string]any)
	if truthy(res["is_error"]) || !ok {
		return nil, errors.New("judge_no_json")
	}
	return list(so["pairs"]), nil
}

// pairIndex is isinstance(v, int) and 0 <= v < n (a bool counts, as in Python).
func pairIndex(v any, n int) (int, bool) {
	var i int64
	switch x := v.(type) {
	case bool:
		if x {
			i = 1
		}
	case int:
		i = int64(x)
	case int64:
		i = x
	default:
		s, ok := v.(interface{ String() string })
		if !ok {
			return 0, false
		}
		parsed, err := strconv.ParseInt(s.String(), 10, 32)
		if err != nil {
			return 0, false
		}
		i = parsed
	}
	if i < 0 || i >= int64(n) {
		return 0, false
	}
	return int(i), true
}

type proposal struct {
	kind         string
	keep, retire map[string]any
	reason       string
}

// proposals is (type, keep, retire, reason) per pair the judge found worth a decision.
func proposals(pairs []memPair, verdicts []any) []proposal {
	out := []proposal{}
	for _, v := range verdicts {
		vm := obj(v)
		i, ok := pairIndex(vm["pair"], len(pairs))
		if !ok || (vm["verdict"] != "duplicate" && vm["verdict"] != "contradiction") {
			continue
		}
		p := pairs[i]
		if vm["verdict"] == "contradiction" { // the newer one is the person's current view, whatever the model said
			out = append(out, proposal{"contradiction", p.newer, p.older, "newer_replaces_older"})
			continue
		}
		keep, retire := p.newer, p.older
		if vm["keep"] == "a" {
			keep, retire = p.older, p.newer
		}
		reason := "same_meaning"
		if vm["reason"] == "same_meaning" || vm["reason"] == "more_specific" {
			reason = vm["reason"].(string)
		}
		out = append(out, proposal{"duplicate", keep, retire, reason})
	}
	return out
}

func judgedFile() string { return filepath.Join(host.CaptureState, "maintain_judged.json") }

// pairKey is ("idA|idB", "revA:revB") with the ids in a fixed order, so either order finds the same entry.
func pairKey(a, b map[string]any) (string, string) {
	if pyStr(b["id"]) < pyStr(a["id"]) {
		a, b = b, a
	}
	return pyStr(a["id"]) + "|" + pyStr(b["id"]), pyStr(a["revision"]) + ":" + pyStr(b["revision"])
}

// readJudged is the judged file's entries in file order.
func readJudged() ([]string, map[string]any) {
	raw, err := os.ReadFile(judgedFile())
	if err != nil {
		return nil, nil
	}
	v, err := strictJSON(raw)
	m, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, nil
	}
	return memberKeys(append(append([]byte(`{"x":`), raw...), '}'), "x"), m
}

// judgedPairs is the pairs a model already judged different at the memories' current revisions. Without this the
// same most-alike pairs would fill the nightly limit and pairs further down would never be looked at. The file holds
// ids and revision numbers only; an edited memory is judged again.
func judgedPairs(items []map[string]any) map[string]bool {
	keys, judged := readJudged()
	byID := map[string]map[string]any{}
	for _, m := range items {
		byID[pyStr(m["id"])] = m
	}
	out := map[string]bool{}
	for _, key := range keys {
		a, b, _ := strings.Cut(key, "|")
		ma, okA := byID[a]
		mb, okB := byID[b]
		if okA && okB {
			if _, revs := pairKey(ma, mb); same(revs, judged[key]) {
				out[pairOf(a, b)] = true
			}
		}
	}
	return out
}

// rememberDifferent adds the pairs the model answered "different" to the judged file (newest last, capped).
func rememberDifferent(pairs []memPair, verdicts []any) error {
	different := []memPair{}
	for _, v := range verdicts {
		vm := obj(v)
		if i, ok := pairIndex(vm["pair"], len(pairs)); ok && vm["verdict"] == "different" {
			different = append(different, pairs[i])
		}
	}
	if len(different) == 0 {
		return nil
	}
	keys, judged := readJudged()
	if judged == nil {
		keys, judged = nil, map[string]any{}
	}
	for _, p := range different {
		key, revs := pairKey(p.older, p.newer)
		if _, ok := judged[key]; ok {
			keys = slicesDelete(keys, key)
		}
		keys, judged[key] = append(keys, key), revs
	}
	keys = keys[max(0, len(keys)-judgedMax):]
	out := host.Obj{}
	for _, k := range keys {
		out = append(out, host.KV{Key: k, Value: judged[k]})
	}
	if err := os.MkdirAll(filepath.Dir(judgedFile()), 0o777); err != nil {
		return err
	}
	return os.WriteFile(judgedFile(), []byte(host.MustDumps(out, host.DumpOpts{})), 0o666)
}

func slicesDelete(keys []string, key string) []string {
	out := keys[:0]
	for _, k := range keys {
		if k != key {
			out = append(out, k)
		}
	}
	return out
}

func memRef(m map[string]any) host.Obj {
	return host.Obj{{Key: "collection", Value: "memories"}, {Key: "id", Value: m["id"]}, {Key: "revision", Value: m["revision"]}}
}

// maintainRun is (proposals found, proposals posted) for this night.
func maintainRun(dryRun bool, stdout io.Writer) (int, int, error) {
	items, err := memoriesFn()
	if err != nil {
		return 0, 0, err
	}
	seen, err := knownPairsFn(items)
	if err != nil {
		return 0, 0, err
	}
	for k := range judgedPairs(items) {
		seen[k] = true
	}
	pairs := candidatePairs(items, seen, nil)
	if len(pairs) == 0 {
		mMetric(map[string]any{"event": "maintain", "memories": len(items), "pairs": 0, "proposed": 0})
		return 0, 0, nil
	}
	var verdicts []any
	if useLLM() {
		if verdicts, err = judgeFn(pairs); err != nil {
			return 0, 0, err
		}
		if !dryRun {
			if err := rememberDifferent(pairs, verdicts); err != nil {
				return 0, 0, err
			}
		}
	} else {
		for i, p := range pairs {
			if p.score >= nearScore {
				verdicts = append(verdicts, map[string]any{"pair": i, "verdict": "duplicate", "keep": "b", "reason": "same_meaning"})
			}
		}
	}
	found := proposals(pairs, verdicts)
	found = found[:min(len(found), max(0, maxProposals()))]
	posted := 0
	for _, p := range found {
		if dryRun {
			fmt.Fprintf(stdout, "%s: keep %s retire %s (%s)\n", p.kind, pyStr(p.keep["id"]), pyStr(p.retire["id"]), p.reason)
			continue
		}
		res, err := mPost("/v1/maintenance_proposals", host.Obj{{Key: "scope", Value: scope()}, {Key: "proposal", Value: host.Obj{
			{Key: "type", Value: p.kind}, {Key: "keep", Value: memRef(p.keep)}, {Key: "retire", Value: memRef(p.retire)}, {Key: "reason", Value: p.reason}}}}, 30*time.Second)
		if err != nil { // a memory changed since it was read: next night sees the new one
			mMetric(map[string]any{"event": "maintain_error", "step": "post", "error": errName(err)})
			continue
		}
		if !truthy(res["duplicate"]) {
			posted++
		}
	}
	mMetric(map[string]any{"event": "maintain", "memories": len(items), "pairs": len(pairs), "proposed": posted, "dry_run": dryRun})
	return len(found), posted, nil
}

func maintainMain(argv []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(argv) == 0 || argv[0] != "run" {
		fmt.Fprintln(stderr, maintainDoc)
		return 1
	}
	dry := false
	for _, a := range argv[1:] {
		dry = dry || a == "--dry-run"
	}
	// The model judge spends the user's own Claude; near a limit it waits for another night.
	if useLLM() {
		if why := capture.LLMBudgetReason(maintainModel()); why != "" {
			fmt.Fprintln(stdout, why)
			return 0
		}
	}
	found, posted, err := maintainRun(dry, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %s\n", errName(err), strings.TrimFunc(err.Error(), unicode.IsSpace))
		return 1
	}
	fmt.Fprintf(stdout, "found %d, posted %d\n", found, posted)
	return 0
}

func init() { host.Register("maintain", maintainMain) }
