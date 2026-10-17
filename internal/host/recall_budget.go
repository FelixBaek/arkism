package host

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// NoRelatedMemory is what recall reports when nothing passes the score floor and kind budget (ST-815).
const NoRelatedMemory = "관련 기억 없음"

// KindBudget caps how many memories of one kind a recall keeps and their total text length (runes).
type KindBudget struct {
	Count int
	Chars int
}

// RecallBudget is the one recall filter the hooks and the MCP recall tool share (ST-815).
type RecallBudget struct {
	MinScore float64               // fused search score (ST-811); a hit below it is not injected; 0 turns the floor off
	Kinds    map[string]KindBudget // a kind not listed here is judged by the floor only
}

// First values from the ST-810 baseline (2026-10-17): every relevant hit scored ≥ 0.032787 (2/61) and
// every unrelated-query hit ≤ 0.016393 (1/61). The four kind caps add up to the search's 6000 chars and
// the largest baseline recall was 2,056 chars, so the caps only trim one-kind floods. A person approves
// the values; later tuning is a dreaming proposal (ST-821), not automatic.
const (
	defaultRecallMinScore = 0.0327
	defaultRecallBudget   = "user=2:1500,procedural=3:1500,lesson=2:1500,decision=2:1500"
)

// LoadRecallBudget reads ARKISM_RECALL_MIN_SCORE and ARKISM_RECALL_BUDGET ("kind=count:chars,...");
// a malformed value keeps the default for that part.
func LoadRecallBudget() RecallBudget {
	b := parseRecallBudget(defaultRecallBudget)
	b.MinScore = defaultRecallMinScore
	if v, ok := os.LookupEnv("ARKISM_RECALL_MIN_SCORE"); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f >= 0 {
			b.MinScore = f
		}
	}
	if v, ok := os.LookupEnv("ARKISM_RECALL_BUDGET"); ok {
		override := parseRecallBudget(v)
		for k, kb := range override.Kinds {
			b.Kinds[k] = kb
		}
	}
	return b
}

func parseRecallBudget(spec string) RecallBudget {
	b := RecallBudget{Kinds: map[string]KindBudget{}}
	for _, part := range strings.Split(spec, ",") {
		kind, caps, ok := strings.Cut(strings.TrimSpace(part), "=")
		count, chars, ok2 := strings.Cut(caps, ":")
		n, err1 := strconv.Atoi(strings.TrimSpace(count))
		c, err2 := strconv.Atoi(strings.TrimSpace(chars))
		kind = strings.TrimSpace(kind)
		if !ok || !ok2 || err1 != nil || err2 != nil || n < 0 || c < 0 || kind == "" {
			continue
		}
		b.Kinds[kind] = KindBudget{n, c}
	}
	return b
}

// Apply keeps search hits in rank order that reach MinScore and fit their kind's budget. A hit
// without a score (an older server) is not judged by the floor. droppedScore/droppedBudget are counts.
func (b RecallBudget) Apply(items []any) (kept []any, droppedScore, droppedBudget int) {
	kept = []any{}
	used := map[string]KindBudget{}
	for _, raw := range items {
		m, _ := raw.(map[string]any)
		if m == nil { // not judged here; callers keep their own shape checks
			kept = append(kept, raw)
			continue
		}
		if s, has := score(m["score"]); has && b.MinScore > 0 && s < b.MinScore {
			droppedScore++
			continue
		}
		kind, _ := m["kind"].(string)
		limit, listed := b.Kinds[kind]
		if !listed {
			kept = append(kept, raw)
			continue
		}
		text, _ := m["text"].(string)
		u := used[kind]
		u.Count++
		u.Chars += utf8.RuneCountInString(text)
		// The kind's best hit goes in however long it is (the search's max_chars bounds it); the char cap trims the rest.
		if u.Count > limit.Count || u.Count > 1 && u.Chars > limit.Chars {
			droppedBudget++
			continue
		}
		used[kind] = u
		kept = append(kept, raw)
	}
	return kept, droppedScore, droppedBudget
}

// score reads a decoded JSON number (Decode keeps json.Number).
func score(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}
