package memory

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Recall baseline (ST-810, ADR-0005): what recall hands out today, before any change.
// It replays the recall search inside one READ ONLY transaction and reports numbers only:
// no memory text, query text or id leaves this file.

// The recall tool and the per-turn hook both ask for 6 items within 6000 characters
// (internal/host/skills/router.go recall, internal/host/client.go Search).
const (
	baselineLimit    = 6
	baselineMaxChars = 6000
	// baselineSelfSample follows ADR-0005's 30-50 question evaluation set.
	baselineSelfSample = 50
)

// baselineNegatives are questions no personal memory should answer. Their hits are
// the noise a "nothing relevant" cut (ST-815) has to drop.
var baselineNegatives = []string{
	"파스타 토마토 소스 만드는 법",
	"오늘 서울 날씨 어때",
	"피보나치 수열이 뭐야",
	"What is the capital of Australia?",
	"고양이 털 빠짐 줄이는 방법",
	"주말 등산 코스 추천해 줘",
	"엑셀 VLOOKUP 사용법",
	"Translate good morning into French",
	"축구 오프사이드 규칙 설명",
	"How do volcanoes form?",
	"기타 C 코드 잡는 법",
	"감기 빨리 낫는 음식",
}

// BaselineQuery is one evaluation question. Relevant lists memory ids judged relevant;
// with none and Negative false the hits are unlabeled.
type BaselineQuery struct {
	Query    string   `json:"query"`
	Relevant []string `json:"relevant,omitempty"`
	Negative bool     `json:"negative,omitempty"`
	// known: a known-item question labels only its target; other hits may be relevant too.
	known bool
}

type baselineHit struct {
	score    float64
	lanes    []int
	chars    int
	label    int // +1 relevant, -1 not relevant, 0 unknown
	distance float64
	hasDist  bool
}

type baselineResult struct {
	hits           []baselineHit
	labeled        bool
	negative       bool
	vectorDisabled bool
}

// BaselineDistribution is a five-number summary; N=0 leaves the rest zero.
type BaselineDistribution struct {
	N   int     `json:"n"`
	Min float64 `json:"min"`
	P10 float64 `json:"p10"`
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	Max float64 `json:"max"`
}

// BaselineCut is a candidate "do not inject" threshold for ST-815, not a decision.
type BaselineCut struct {
	Signal       string  `json:"signal"` // rrf_score (keep >= value) or vector_distance (keep <= value)
	Rule         string  `json:"rule"`   // best_separation or keep_95_relevant
	Value        float64 `json:"value"`
	KeepRelevant float64 `json:"keep_relevant"`
	DropNegative float64 `json:"drop_negative"`
	Relevant     int     `json:"relevant"`
	Negative     int     `json:"negative"`
}

type BaselineOutcomes struct {
	Results        int                  `json:"results"`
	Passed         int                  `json:"passed"`
	PassRate       float64              `json:"pass_rate"`
	Memories       int                  `json:"memories"`
	Last30Results  int                  `json:"last_30d_results"`
	Last30PassRate float64              `json:"last_30d_pass_rate"`
	PerMemoryRate  BaselineDistribution `json:"per_memory_pass_rate_min3"`
}

type BaselineReport struct {
	Limit          int                         `json:"limit"`
	MaxChars       int                         `json:"max_chars"`
	VectorEnabled  bool                        `json:"vector_enabled"`
	Queries        map[string]int              `json:"queries"`
	Lanes          map[string]int              `json:"lanes"`
	Top1Score      BaselineDistribution        `json:"top1_rrf_score"`
	AllScore       BaselineDistribution        `json:"top6_rrf_score"`
	HitsPerQuery   BaselineDistribution        `json:"hits_per_query"`
	RecallChars    BaselineDistribution        `json:"recall_chars"`
	Relevance      map[string]float64          `json:"relevance,omitempty"`
	RelevantScore  BaselineDistribution        `json:"relevant_rrf_score"`
	NegativeScore  BaselineDistribution        `json:"negative_rrf_score"`
	RelevantDist   BaselineDistribution        `json:"relevant_vector_distance"`
	NegativeDist   BaselineDistribution        `json:"negative_vector_distance"`
	NegativeTop1   BaselineDistribution        `json:"negative_query_top1_rrf_score"`
	Cuts           []BaselineCut               `json:"cut_candidates"`
	Outcomes       map[string]BaselineOutcomes `json:"outcomes"`
	ObservedAt     time.Time                   `json:"observed_at"`
	ReadOnlyTxHeld bool                        `json:"read_only_transaction"`
}

var baselineLaneNames = []string{"exact", "fts", "vector"}

// RecallBaseline measures recall in scope from one READ ONLY snapshot. self adds a
// known-item set (each sampled memory's own summary must find it); the built-in
// negative questions are always added. A nil embedder measures the lexical lanes only.
func (p *Postgres) RecallBaseline(ctx context.Context, scope Scope, queries []BaselineQuery, self bool, embedder Embedder) (BaselineReport, error) {
	if p == nil || p.pool == nil || p.tx != nil || checkScope(scope) != nil {
		return BaselineReport{}, ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return BaselineReport{}, pgErr(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // read only: nothing to commit
	hash, bound := p.effectiveTokenHash(ctx)
	if bound {
		if _, err = tx.Exec(ctx, `SELECT set_config('arkism.token_hash',$1,true)`, hash); err != nil {
			return BaselineReport{}, pgErr(err)
		}
	}
	view := &Postgres{pool: p.pool, tx: tx, tokenHash: hash, vectorMaxDistance: p.vectorMaxDistance}
	if self {
		known, err := view.baselineKnownItems(ctx, scope)
		if err != nil {
			return BaselineReport{}, err
		}
		queries = append(queries, known...)
	}
	for _, q := range baselineNegatives {
		queries = append(queries, BaselineQuery{Query: q, Negative: true})
	}
	results := make([]baselineResult, 0, len(queries))
	for _, q := range queries {
		if strings.TrimSpace(q.Query) == "" || !utf8.ValidString(q.Query) {
			return BaselineReport{}, ErrInvalid
		}
		r, err := view.baselineSearch(ctx, scope, q, embedder)
		if err != nil {
			return BaselineReport{}, err
		}
		results = append(results, r)
	}
	evals, err := view.selectRecords(ctx, "evaluations", scope, "kind='memory_outcome'", nil, "id ASC", 100000)
	if err != nil {
		return BaselineReport{}, err
	}
	report := summarizeBaseline(results, embedder != nil)
	report.Outcomes = summarizeOutcomes(evals, time.Now().UTC())
	report.ObservedAt = time.Now().UTC().Truncate(time.Second)
	var readOnly string
	if err = tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly); err != nil {
		return BaselineReport{}, pgErr(err)
	}
	report.ReadOnlyTxHeld = readOnly == "on"
	return report, nil
}

func (p *Postgres) baselineKnownItems(ctx context.Context, scope Scope) ([]BaselineQuery, error) {
	rows, err := p.selectRecords(ctx, colMemories, scope, "status='active'", nil, "md5(id) ASC", baselineSelfSample*4)
	if err != nil {
		return nil, err
	}
	out := []BaselineQuery{}
	for _, r := range rows {
		if len(out) == baselineSelfSample {
			break
		}
		if !live(r) || r.ApprovedBy == "" || len(r.Sources) == 0 {
			continue
		}
		q := strings.TrimSpace(r.Summary)
		if q == "" {
			q = strings.TrimSpace(r.Text)
		}
		if runes := []rune(q); len(runes) > 200 {
			q = string(runes[:200])
		}
		if q != "" {
			out = append(out, BaselineQuery{Query: q, Relevant: []string{r.ID}, known: true})
		}
	}
	return out, nil
}

// baselineSearch is the recall path of Service.Search without its authorization and
// usage receipt: the same lanes, the same liveness checks and the same fusion.
func (p *Postgres) baselineSearch(ctx context.Context, scope Scope, q BaselineQuery, embedder Embedder) (baselineResult, error) {
	var vec []float64
	space := ""
	// Same gate as Service.Search: text that fails safeTransfer never reaches the provider.
	vectorDisabled := embedder != nil && !safeTransfer(q.Query)
	if embedder != nil && !vectorDisabled {
		batch, err := embedMeasured(ctx, embedder, []string{q.Query}, "query")
		if err != nil || len(batch.Items) != 1 {
			return baselineResult{}, ErrUnavailable
		}
		vec, space = batch.Items[0].Vector, embedder.Space()
	}
	criteria, err := normalizeSearchCriteria(SearchCriteria{})
	if err != nil {
		return baselineResult{}, err
	}
	lists, err := p.searchCollections(ctx, []string{colMemories}, scope, q.Query, vec, space, baselineLimit, criteria, "exact", "fts", "vector")
	if err != nil {
		return baselineResult{}, err
	}
	for i, list := range lists {
		clean := []Hit{}
		for _, h := range list {
			if h.Scope != scope || h.Status != "active" || len(h.Sources) == 0 {
				continue
			}
			r, err := p.Get(ctx, colMemories, scope, h.ID)
			if err != nil || !live(r) || r.ApprovedBy == "" || checkSources(ctx, p, r) != nil {
				continue
			}
			why := h.Reasons
			h = HitFromRecord(colMemories, r)
			h.Reasons = why
			clean = append(clean, h)
		}
		lists[i] = clean
	}
	fused := FuseScored(lists, baselineLimit)
	distances, err := p.baselineDistances(ctx, scope, fused, vec, space)
	if err != nil {
		return baselineResult{}, err
	}
	out := baselineHits(q, fused, distances)
	out.vectorDisabled = vectorDisabled
	return out, nil
}

// baselineDistances reads the stored vector distance of each fused hit.
func (p *Postgres) baselineDistances(ctx context.Context, scope Scope, fused []ScoredHit, vec []float64, space string) (map[string]float64, error) {
	distances := map[string]float64{}
	if len(vec) > 0 && len(fused) > 0 {
		ids := make([]string, len(fused))
		for i, h := range fused {
			ids[i] = h.ID
		}
		rows, err := p.q().Query(ctx, `SELECT id, embedding <=> $6::vector FROM memory_records WHERE collection='memories' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=ANY($5) AND embedding IS NOT NULL AND doc->'embedding'->>'space'=$7`,
			scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, ids, vectorSQL(vec), space)
		if err != nil {
			return nil, pgErr(err)
		}
		for rows.Next() {
			var id string
			var d float64
			if rows.Scan(&id, &d) == nil {
				distances[id] = d
			}
		}
		rows.Close()
		if rows.Err() != nil {
			return nil, pgErr(rows.Err())
		}
	}
	return distances, nil
}

// baselineHits labels the fused hits and keeps only what Service.Search would return
// within the character budget: once it is spent, later hits are dropped, not counted.
func baselineHits(q BaselineQuery, fused []ScoredHit, distances map[string]float64) baselineResult {
	out := baselineResult{labeled: len(q.Relevant) > 0, negative: q.Negative}
	left := baselineMaxChars
	for _, h := range fused {
		if left == 0 {
			break
		}
		chars := min(utf8.RuneCountInString(h.Text), left)
		left -= chars
		chars2 := min(utf8.RuneCountInString(h.Summary), left)
		left -= chars2
		bh := baselineHit{score: h.Score, lanes: h.Lanes, chars: chars + chars2}
		bh.distance, bh.hasDist = distances[h.ID]
		switch {
		case q.Negative:
			bh.label = -1
		case slices.Contains(q.Relevant, h.ID):
			bh.label = 1
		case out.labeled && !q.known:
			bh.label = -1
		}
		out.hits = append(out.hits, bh)
	}
	return out
}

func summarizeBaseline(results []baselineResult, vector bool) BaselineReport {
	rep := BaselineReport{Limit: baselineLimit, MaxChars: baselineMaxChars, VectorEnabled: vector, Queries: map[string]int{}, Lanes: map[string]int{}, Relevance: map[string]float64{}}
	var top1, all, perQuery, chars, relScore, negScore, relDist, negDist, negTop1 []float64
	var labeled, found float64
	var mrr, precision float64
	for _, r := range results {
		rep.Queries["total"]++
		if r.vectorDisabled {
			rep.Queries["vector_disabled"]++
		}
		switch {
		case r.negative:
			rep.Queries["negative"]++
		case r.labeled:
			rep.Queries["labeled"]++
		}
		if len(r.hits) == 0 {
			rep.Queries["empty"]++
			if r.negative {
				rep.Queries["negative_empty"]++
			}
		}
		perQuery = append(perQuery, float64(len(r.hits)))
		total := 0
		rank := 0
		relevantHits := 0
		for i, h := range r.hits {
			total += h.chars
			all = append(all, h.score)
			if i == 0 {
				top1 = append(top1, h.score)
				if r.negative {
					negTop1 = append(negTop1, h.score)
				}
			}
			names := []string{}
			for _, l := range h.lanes {
				if l < len(baselineLaneNames) {
					names = append(names, baselineLaneNames[l])
				}
			}
			sort.Strings(names)
			rep.Lanes[strings.Join(names, "+")]++
			switch h.label {
			case 1:
				relevantHits++
				if rank == 0 {
					rank = i + 1
				}
				relScore = append(relScore, h.score)
				if h.hasDist {
					relDist = append(relDist, h.distance)
				}
			case -1:
				negScore = append(negScore, h.score)
				if h.hasDist {
					negDist = append(negDist, h.distance)
				}
			}
		}
		chars = append(chars, float64(total))
		if r.labeled {
			labeled++
			if rank > 0 {
				found++
				mrr += 1 / float64(rank)
			}
			precision += float64(relevantHits) / baselineLimit
		}
	}
	if labeled > 0 {
		rep.Relevance["hit_at_6"] = round4(found / labeled)
		rep.Relevance["mrr"] = round4(mrr / labeled)
		rep.Relevance["precision_at_6"] = round4(precision / labeled)
	}
	rep.Top1Score, rep.AllScore = baselineSpread(top1), baselineSpread(all)
	rep.HitsPerQuery, rep.RecallChars = baselineSpread(perQuery), baselineSpread(chars)
	rep.RelevantScore, rep.NegativeScore = baselineSpread(relScore), baselineSpread(negScore)
	rep.RelevantDist, rep.NegativeDist = baselineSpread(relDist), baselineSpread(negDist)
	rep.NegativeTop1 = baselineSpread(negTop1)
	rep.Cuts = append(cutCandidates("rrf_score", relScore, negScore, true), cutCandidates("vector_distance", relDist, negDist, false)...)
	return rep
}

// cutCandidates proposes thresholds between relevant and negative values: the cut with
// the best separation (Youden J) and the strictest cut that still keeps 95% of relevant.
func cutCandidates(signal string, rel, neg []float64, higherBetter bool) []BaselineCut {
	if len(rel) == 0 || len(neg) == 0 {
		return nil
	}
	keeps := func(v, t float64) bool {
		if higherBetter {
			return v >= t
		}
		return v <= t
	}
	at := func(rule string, t float64) BaselineCut {
		k, d := 0, 0
		for _, v := range rel {
			if keeps(v, t) {
				k++
			}
		}
		for _, v := range neg {
			if !keeps(v, t) {
				d++
			}
		}
		return BaselineCut{Signal: signal, Rule: rule, Value: round6(t), KeepRelevant: round4(float64(k) / float64(len(rel))), DropNegative: round4(float64(d) / float64(len(neg))), Relevant: len(rel), Negative: len(neg)}
	}
	values := append(slices.Clone(rel), neg...)
	sort.Float64s(values)
	values = slices.Compact(values)
	best := at("best_separation", values[0])
	for _, t := range values[1:] {
		if c := at("best_separation", t); c.KeepRelevant+c.DropNegative > best.KeepRelevant+best.DropNegative {
			best = c
		}
	}
	sorted := slices.Clone(rel)
	sort.Float64s(sorted)
	idx := int(math.Floor(0.05 * float64(len(sorted)-1)))
	if !higherBetter {
		idx = len(sorted) - 1 - idx
	}
	return []BaselineCut{best, at("keep_95_relevant", sorted[idx])}
}

func summarizeOutcomes(evals []Record, now time.Time) map[string]BaselineOutcomes {
	out := map[string]BaselineOutcomes{}
	perMemory := map[string]map[string][2]int{}
	for _, r := range evals {
		if r.Kind != "memory_outcome" || r.Status != "active" || r.Target == nil {
			continue
		}
		dim := ""
		for _, t := range r.Tags {
			if strings.HasPrefix(t, "dimension:") {
				dim = strings.TrimPrefix(t, "dimension:")
			}
		}
		if dim == "" {
			continue
		}
		o := out[dim]
		o.Results++
		if perMemory[dim] == nil {
			perMemory[dim] = map[string][2]int{}
		}
		m := perMemory[dim][r.Target.ID]
		m[1]++
		if r.Passed {
			o.Passed++
			m[0]++
		}
		perMemory[dim][r.Target.ID] = m
		if now.Sub(r.CreatedAt) <= 30*24*time.Hour {
			o.Last30Results++
			if r.Passed {
				o.Last30PassRate++
			}
		}
		out[dim] = o
	}
	for dim, o := range out {
		o.PassRate = round4(float64(o.Passed) / float64(o.Results))
		if o.Last30Results > 0 {
			o.Last30PassRate = round4(o.Last30PassRate / float64(o.Last30Results))
		}
		o.Memories = len(perMemory[dim])
		rates := []float64{}
		for _, m := range perMemory[dim] {
			if m[1] >= 3 {
				rates = append(rates, float64(m[0])/float64(m[1]))
			}
		}
		o.PerMemoryRate = baselineSpread(rates)
		out[dim] = o
	}
	return out
}

func baselineSpread(values []float64) BaselineDistribution {
	if len(values) == 0 {
		return BaselineDistribution{}
	}
	v := slices.Clone(values)
	sort.Float64s(v)
	q := func(p float64) float64 { return round6(v[int(math.Round(p*float64(len(v)-1)))]) }
	return BaselineDistribution{N: len(v), Min: round6(v[0]), P10: q(0.1), P50: q(0.5), P90: q(0.9), Max: round6(v[len(v)-1])}
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }
