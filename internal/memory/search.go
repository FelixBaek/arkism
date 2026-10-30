package memory

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type SearchInput struct {
	contextView   *ContextInvocationView
	finalRejected func(Hit, string, string)
	SearchCriteria
	RelatedTo       *Ref     `json:"related_to,omitempty"`
	IncludeInactive bool     `json:"include_inactive,omitempty"`
	Modes           []string `json:"modes,omitempty"`
	BudgetTokens    int      `json:"budget_tokens,omitempty"`
	TokenEncoding   string   `json:"token_encoding,omitempty"`
	RunID           string   `json:"run_id,omitempty"`
	Destination     string   `json:"destination,omitempty"`
	Purpose         string   `json:"purpose,omitempty"`
	// Runtime (route only) drops skills labelled unsupported on it.
	Runtime string `json:"runtime,omitempty"`
	// ExtraCandidates (route only): the caller's own MCP servers and plugins, judged beside the skills.
	ExtraCandidates []SkillCandidate `json:"extra_candidates,omitempty"`
	Scope           Scope            `json:"scope"`
	Query           string           `json:"query"`
	Limit           int              `json:"limit"`
	MaxChars        int              `json:"max_chars"`
	retrievalQuery  string
	// pinned: memories a context build already injected ahead of this search
	// (crew_pins.go); the receipt lists them first and the search hits after.
	pinned []Ref
	// client and session label the receipt (which coding agent, which of its sessions). They come
	// from request headers, not the body, so older servers that reject unknown fields keep working.
	client, session string
}

func searchModeEnabled(modes []string, mode string) bool {
	return len(modes) == 0 || slices.Contains(modes, mode)
}
func validSearchModes(modes []string) bool {
	if len(modes) > 4 {
		return false
	}
	for i, mode := range modes {
		if !slices.Contains([]string{"exact", "fts", "vector", "relation"}, mode) || slices.Contains(modes[:i], mode) {
			return false
		}
	}
	return true
}

// ScoredHit is a fused hit with its RRF score and the lane indexes that found it.
type ScoredHit struct {
	Hit
	Score float64
	Lanes []int
}

// laneByReason names the lane each search reason comes from (existing policy).
var laneByReason = map[string]string{reasonExactKeyword: "exact", reasonTextSearch: "keyword", reasonVector: "vector", "relation": "relation"}

// hitLanes lists the lanes that found a fused hit, in a fixed order.
func hitLanes(reasons []string) []string {
	lanes := []string{}
	for _, lane := range []string{"exact", "keyword", "vector", "relation"} {
		for _, r := range reasons {
			if laneByReason[r] == lane {
				lanes = append(lanes, lane)
				break
			}
		}
	}
	return lanes
}

// Fuse uses ordinal positions only. Provider similarities are never accepted.
func Fuse(lists [][]Hit, limit int) []Hit {
	scored := FuseScored(lists, limit)
	out := make([]Hit, len(scored))
	for i, r := range scored {
		out[i] = r.Hit
	}
	return out
}

// FuseScored is Fuse with each hit's score kept, for measurement (existing policy).
func FuseScored(lists [][]Hit, limit int) []ScoredHit {
	if limit <= 0 {
		return []ScoredHit{}
	}
	const rankOffset = 60
	const exactKeywordWeight = 2.0
	type ranked struct {
		ScoredHit
		key string
	}
	merged := map[string]*ranked{}
	for lane, list := range lists {
		seen := map[string]bool{}
		for position, hit := range list {
			key := hit.Origin + ":" + hit.Collection + ":" + hit.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			entry := merged[key]
			if entry == nil {
				hit.Reasons = slices.Clone(hit.Reasons)
				entry = &ranked{ScoredHit: ScoredHit{Hit: hit}, key: key}
				merged[key] = entry
			}
			entry.Lanes = append(entry.Lanes, lane)
			if entry.VectorMatch == nil && hit.VectorMatch != nil {
				entry.VectorMatch = hit.VectorMatch
			}
			weight := 1.0
			if slices.Contains(hit.Reasons, "exact_keyword") {
				weight = exactKeywordWeight
			}
			entry.Score += weight / float64(rankOffset+position+1)
			for _, reason := range hit.Reasons {
				if !slices.Contains(entry.Hit.Reasons, reason) {
					entry.Hit.Reasons = append(entry.Hit.Reasons, reason)
				}
			}
		}
	}
	rows := make([]*ranked, 0, len(merged))
	for _, r := range merged {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Hit.Trust != b.Hit.Trust {
			return a.Hit.Trust > b.Hit.Trust
		}
		if !a.Hit.UpdatedAt.Equal(b.Hit.UpdatedAt) {
			return a.Hit.UpdatedAt.After(b.Hit.UpdatedAt)
		}
		return a.key < b.key
	})
	out := make([]ScoredHit, 0, min(limit, len(rows)))
	for _, r := range rows {
		if len(out) >= limit {
			break
		}
		r.Hit.Authority = AuthorityDataOnly
		out = append(out, r.ScoredHit)
	}
	return out
}
func (s *Service) Search(ctx context.Context, token string, in SearchInput) (ContextBundle, error) {
	out, _, err := s.searchWithActor(ctx, token, in)
	return out, err
}

// The HTTP permission and the retrieval lane must resolve the same collection.
func searchCollection(purpose string) (string, bool) {
	switch purpose {
	case "", "answer", "knowledge":
		return colMemories, true
	case "skills":
		return colSkillVersions, true
	case "self_improvement", "risk", "skill_validation":
		return colReflections, true
	default:
		return "", false
	}
}

// Keep the search actor inside the service for multi-step consumers.
func (s *Service) searchWithActor(ctx context.Context, token string, in SearchInput) (ContextBundle, Decision, error) {
	searchContent, err := prepareSearchInput(&in)
	if err != nil {
		return ContextBundle{}, Decision{}, err
	}
	out := ContextBundle{Authority: "data_only_untrusted", Items: []Hit{}}
	if !boundSearchInput(&in) {
		return out, Decision{}, ErrInvalid
	}
	collection, ok := searchCollection(in.Purpose)
	if !ok {
		return out, Decision{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "search:"+collection, in.Scope, "", searchContent)
	if e != nil {
		return out, Decision{}, e
	}
	var anchor Record
	if in.RelatedTo != nil {
		anchor, e = s.readRelationAnchor(ctx, token, in, d)
		if e != nil {
			return ContextBundle{}, Decision{}, e
		}
	}
	run, err := s.searchRun(ctx, token, in, d)
	if err != nil {
		return ContextBundle{}, Decision{}, err
	}
	queryVector, querySpace, degraded, memoryErr := s.searchQueryVector(ctx, in, d, searchContent)
	if memoryErr != nil {
		return out, Decision{}, ErrUnavailable
	}
	retrieval := in
	if in.retrievalQuery != "" {
		retrieval.Query = in.retrievalQuery
	}
	lists, memoryErr := s.Repo.SearchCollection(ctx, collection, retrieval, queryVector, querySpace)
	if memoryErr != nil {
		return out, Decision{}, ErrUnavailable
	}
	relationLane := -1
	if in.RelatedTo != nil {
		hits, err := s.Repo.SearchRelatedMemories(ctx, in, anchor)
		if err != nil {
			return ContextBundle{}, Decision{}, err
		}
		relationLane = len(lists)
		lists = append(lists, hits)
	}
	out.Degraded = degraded
	// Recheck source liveness and scope after every search lane; search indexes
	// are not authorization authorities.
	historyRecords := map[string]Record{}
	if e = s.cleanSearchLanes(ctx, token, in, d, collection, anchor, run, relationLane, queryVector, querySpace, historyRecords, &out, lists); e != nil {
		return ContextBundle{}, Decision{}, e
	}
	filterContextViewLanes(lists, in.contextView)
	filterContextViewHistory(historyRecords, in.contextView)
	shadow := s.beginNativeSearchShadow(ctx, in, collection, lists, historyRecords)
	defer shadow.finish()
	shadow.bindFinalRejection(&in)
	scored := s.fuseRecallHits(ctx, lists, collection, in)
	out.Items = make([]Hit, len(scored))
	for i, r := range scored {
		r.Hit.Score, r.Hit.Lanes = r.Score, hitLanes(r.Hit.Reasons)
		out.Items[i] = r.Hit
	}
	snapshots := map[Ref]string{}
	for _, h := range out.Items {
		snapshots[Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision}] = searchHitHash(h)
	}
	boundSearchHits(&out, in.MaxChars, historyRecords)
	if in.BudgetTokens > 0 {
		if e = packContext(ctx, &out, in.BudgetTokens, in.TokenEncoding); e != nil {
			return ContextBundle{}, Decision{}, e
		}
	}
	out.ContextID = ID()
	shadow.compute(ctx, s, token, in, d, out)
	usage := searchUsage(run, in, out.Items)
	releaseCommit, err := s.searchCommits.acquire(ctx, in.Scope)
	if err != nil {
		return ContextBundle{}, Decision{}, ErrUnavailable
	}
	defer releaseCommit()
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		shadow.beginFinal()
		checked := searchReadTx{Tx: tx}
		if in.RelatedTo != nil {
			if err := verifySearchAnchor(ctx, tx, checked, in, anchor); err != nil {
				return err
			}
		}
		if err := verifySearchHistory(ctx, tx, checked, in, out.History, historyRecords, snapshots); err != nil {
			return err
		}
		if err := verifySearchItems(ctx, checked, in, run, out.Items, snapshots); err != nil {
			return err
		}
		if run != nil {
			if err := verifySearchRun(ctx, checked, in.Scope, run); err != nil {
				return err
			}
		}
		if err := insertUsageReceipt(ctx, tx, in.Scope, out.ContextID, d.PrincipalID, usage); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "search", ContentHash(in.Query))
	}); e != nil {
		shadow.failedFinal(e)
		return ContextBundle{}, Decision{}, e
	}
	shadow.returned(out)
	return out, d, nil
}

// prepareSearchInput validates the relation, history and criteria inputs,
// stores the normalized criteria in in, and returns the text to authorize.
func prepareSearchInput(in *SearchInput) (string, error) {
	view, viewErr := freezeContextView(in.Scope, in.contextView)
	if viewErr != nil {
		return "", viewErr
	}
	in.contextView = view
	if !validSearchRelation(*in) {
		return "", ErrInvalid
	}
	if in.RelatedTo != nil {
		ref := *in.RelatedTo
		in.RelatedTo = &ref
	}
	if in.IncludeInactive && (in.Purpose != "" && in.Purpose != "answer" || in.Destination != "" && in.Destination != "internal") {
		return "", ErrInvalid
	}
	if !validSearchModes(in.Modes) {
		return "", ErrInvalid
	}
	criteria, err := normalizeSearchCriteria(in.SearchCriteria)
	if err != nil {
		return "", err
	}
	criteria.history = in.IncludeInactive
	in.SearchCriteria = criteria
	if !in.IncludeInactive && in.Filters != nil {
		for _, status := range in.Filters.Statuses {
			if status != "active" {
				return "", ErrInvalid
			}
		}
	}
	searchContent := in.Query
	if in.RelatedTo != nil || in.IncludeInactive || len(in.Kinds) > 0 || in.Filters != nil {
		searchContent = structuredText(struct {
			Query           string `json:"query"`
			RelatedTo       *Ref   `json:"related_to,omitempty"`
			IncludeInactive bool   `json:"include_inactive,omitempty"`
			SearchCriteria
		}{in.Query, in.RelatedTo, in.IncludeInactive, criteria})
	}
	return searchContent, nil
}

// boundSearchInput validates the budget, destination and size bounds and
// fills in the default limit and character budget.
func boundSearchInput(in *SearchInput) bool {
	if in.BudgetTokens < 0 || in.BudgetTokens > 64000 || (in.BudgetTokens == 0) != (in.TokenEncoding == "") || in.TokenEncoding != "" && in.TokenEncoding != "cl100k_base" && in.TokenEncoding != "o200k_base" {
		return false
	}
	if in.Destination != "" && in.Destination != "internal" && !destinationPattern.MatchString(in.Destination) {
		return false
	}
	if strings.TrimSpace(in.Query) == "" || !utf8.ValidString(in.Query) || utf8.RuneCountInString(in.Query) > 1000 || in.Limit < 0 || in.Limit > 50 || in.MaxChars < 0 || in.MaxChars > 64000 {
		return false
	}
	if in.Limit == 0 {
		in.Limit = 8
	}
	if in.MaxChars == 0 {
		in.MaxChars = 12000
	}
	return true
}

func searchUsage(run *Ref, in SearchInput, items []Hit) *ContextUsage {
	usage := pinnedUsage(run, in.Purpose, in.pinned)
	usage.ProjectKey, usage.Client, usage.SessionID = in.ProjectKey, in.client, in.session
	for i, h := range items {
		if h.Collection == colMemories && h.Origin == "memory" && !usedRef(usage, h.ID) {
			usage.Items = append(usage.Items, MemoryUse{Memory: Ref{Collection: colMemories, ID: h.ID, Revision: h.Revision}, Rank: len(in.pinned) + i + 1, Score: h.Score, Lanes: h.Lanes})
		}
	}
	return usage
}

func (s *Service) cleanSearchLanes(ctx context.Context, token string, in SearchInput, d Decision, collection string, anchor Record, run *Ref, relationLane int, queryVector []float64, querySpace string, historyRecords map[string]Record, out *ContextBundle, lists [][]Hit) error {
	for i, list := range lists {
		clean := []Hit{}
		for _, h := range list {
			h, ok, e := s.cleanSearchHit(ctx, token, in, d, collection, anchor, run, i == relationLane, queryVector, querySpace, historyRecords, out, h)
			if e != nil {
				return e
			}
			if ok {
				clean = append(clean, h)
			}
		}
		lists[i] = clean
	}
	return nil
}

// searchRun resolves the optional run the search is recorded against.
func (s *Service) searchRun(ctx context.Context, token string, in SearchInput, d Decision) (*Ref, error) {
	if in.RunID == "" {
		return nil, nil
	}
	if len(in.RunID) > 256 || strings.TrimSpace(in.RunID) != in.RunID || containsSecret(in.RunID) {
		return nil, ErrInvalid
	}
	rd, err := s.authorize(ctx, token, "read:agent_runs", in.Scope, in.RunID, "")
	if err != nil {
		return nil, err
	}
	if rd.PrincipalID != d.PrincipalID || rd.Role != d.Role {
		return nil, ErrDenied
	}
	row, err := s.Repo.Get(ctx, "agent_runs", in.Scope, in.RunID)
	if err != nil {
		return nil, err
	}
	if !live(row) {
		return nil, ErrConflict
	}
	if err = checkSources(ctx, s.Repo, row); err != nil {
		return nil, err
	}
	return &Ref{Collection: "agent_runs", ID: row.ID, Revision: row.Revision}, nil
}

// searchQueryVector embeds the query for the vector lane. A provider failure
// only degrades the lane; a usage-ledger failure is returned.
func (s *Service) searchQueryVector(ctx context.Context, in SearchInput, d Decision, searchContent string) ([]float64, string, []string, error) {
	var degraded []string
	if !searchModeEnabled(in.Modes, "vector") {
		return nil, "", degraded, nil
	}
	if !(d.ExternalTransfer && d.ContentSafe && s.Embedder != nil && safeTransfer(searchContent)) {
		return nil, "", append(degraded, "memory_vector_disabled"), nil
	}
	reserved, e := s.beginProviderCall(ctx, in.Scope, "embedding_query", embeddingProvider(s.Embedder), s.Embedder.Space())
	if e != nil {
		return nil, "", degraded, e
	}
	batch, e := embedMeasured(ctx, s.Embedder, []string{in.Query}, "query")
	if e != nil {
		_ = s.finishProviderCall(ctx, reserved, ProviderUsage{Provider: embeddingProvider(s.Embedder), Model: s.Embedder.Space(), Requests: 1}, false)
		return nil, "", append(degraded, "memory_vector_unavailable"), nil
	}
	if e = s.finishProviderCall(ctx, reserved, batch.Usage, true); e != nil {
		return nil, "", degraded, e
	}
	if len(batch.Items) != 1 {
		return nil, "", append(degraded, "memory_vector_unavailable"), nil
	}
	return batch.Items[0].Vector, s.Embedder.Space(), degraded, nil
}

// cleanSearchHit rereads one lane hit and reports whether it stays in the
// lane. Historical records are remembered in historyRecords.
func (s *Service) cleanSearchHit(ctx context.Context, token string, in SearchInput, d Decision, collection string, anchor Record, run *Ref, relationLane bool, queryVector []float64, querySpace string, historyRecords map[string]Record, out *ContextBundle, h Hit) (Hit, bool, error) {
	r, ok := s.searchHitRecord(ctx, in, collection, anchor, run, relationLane, h)
	if !ok {
		return h, false, nil
	}
	why, distanceLimit := h.Reasons, h.vectorDistanceLimit
	h = HitFromRecord(h.Collection, r)
	if in.IncludeInactive && !live(r) {
		historyRecords[h.ID] = r
	}
	h.Reasons = why
	if h.Collection == colReflections {
		h.Reasons = append(h.Reasons, "reflection_search")
	}
	if !in.SearchCriteria.matchesHit(h) {
		return h, false, nil
	}
	history, historical := historyRecords[h.ID]
	allowed, ok, e := s.authorizeSearchHit(ctx, token, in, h, history, historical, out)
	if e != nil || !ok {
		return h, false, e
	}
	if allowed.PrincipalID != d.PrincipalID || allowed.Role != d.Role {
		return h, false, ErrDenied
	}
	if slices.Contains(why, reasonVector) {
		h.VectorMatch = verifiedSemanticMatch(r, queryVector, querySpace, distanceLimit)
	}
	return h, true, nil
}

// searchHitRecord reads the record behind a lane hit and reports whether it
// is still eligible for this search.
func (s *Service) searchHitRecord(ctx context.Context, in SearchInput, collection string, anchor Record, run *Ref, relationLane bool, h Hit) (Record, bool) {
	if h.Scope != in.Scope || !in.IncludeInactive && h.Status != "active" || len(h.Sources) == 0 {
		return Record{}, false
	}
	if h.Collection != collection {
		return Record{}, false
	}
	r, e := s.Repo.Get(ctx, h.Collection, in.Scope, h.ID)
	if e == nil && relationLane && !relationMatches(in.RelatedTo.Collection, anchor, r) {
		return Record{}, false
	}
	if e != nil || s.searchRecordFiltered(ctx, in, run, h.Collection, r) {
		return Record{}, false
	}
	if h.Collection == colReflections && checkReflection(ctx, s.Repo, r) != nil {
		return Record{}, false
	}
	if h.Collection == "skill_versions" {
		if checkActiveSkill(ctx, s.Repo, r) != nil || r.Analysis == nil {
			return Record{}, false
		}
	}
	return r, true
}

func (s *Service) searchRecordFiltered(ctx context.Context, in SearchInput, run *Ref, col string, r Record) bool {
	return (!in.IncludeInactive && !live(r)) || (in.IncludeInactive && !historySearchable(r)) || !in.SearchCriteria.matchesRecord(r) || (col == colMemories && (r.ApprovedBy == "" || run != nil && !verifiedMemory(r))) || (!strings.HasPrefix(in.Destination, "external:") && checkSources(ctx, s.Repo, r) != nil)
}

// authorizeSearchHit authorizes reading one hit. A filtered hit returns false
// without an error and records its degradation on out.
func (s *Service) authorizeSearchHit(ctx context.Context, token string, in SearchInput, h Hit, history Record, historical bool, out *ContextBundle) (Decision, bool, error) {
	if historical {
		allowed, e := s.authorize(ctx, token, "read:memories", in.Scope, h.ID, structuredText(history))
		if e != nil {
			if !errors.Is(e, ErrDenied) {
				return Decision{}, false, e
			}
			return Decision{}, false, nil
		}
		return allowed, true, nil
	}
	if strings.HasPrefix(in.Destination, "external:") {
		allowed, e := s.authorizeTransfer(ctx, token, "context:read", in.Scope, h.ID, h.Sensitivity, in.Destination, structuredText(h), h.Sources)
		if e != nil {
			if !slices.Contains(out.Degraded, "external_content_filtered") {
				out.Degraded = append(out.Degraded, "external_content_filtered")
			}
			return Decision{}, false, nil
		}
		return allowed, true, nil
	}
	allowed, e := s.authorize(ctx, token, "context:read", in.Scope, h.ID, structuredText(h))
	if e != nil {
		if !slices.Contains(out.Degraded, "context_content_filtered") {
			out.Degraded = append(out.Degraded, "context_content_filtered")
		}
		return Decision{}, false, nil
	}
	return allowed, true, nil
}

// boundSearchHits truncates hit text to the character budget and moves
// historical hits into out.History.
func boundSearchHits(out *ContextBundle, maxChars int, historyRecords map[string]Record) {
	left := maxChars
	bounded := []Hit{}
	for _, h := range out.Items {
		if left == 0 {
			break
		}
		text := []rune(h.Text)
		summary := []rune(h.Summary)
		if len(text) > left {
			text = text[:left]
			h.Reasons = append(h.Reasons, "context_text_truncated")
		}
		h.Text = string(text)
		left -= len(text)
		if len(summary) > left {
			summary = summary[:left]
		}
		h.Summary = string(summary)
		left -= len(summary)
		if r, historical := historyRecords[h.ID]; historical {
			out.History = append(out.History, HistoricalHit{Hit: h, ExpiresAt: r.ExpiresAt})
		} else {
			bounded = append(bounded, h)
		}
	}
	out.Items = bounded
}

func verifySearchAnchor(ctx context.Context, tx Tx, checked searchReadTx, in SearchInput, anchor Record) error {
	ref := in.RelatedTo
	r, err := tx.Get(ctx, ref.Collection, in.Scope, ref.ID)
	if err != nil {
		return err
	}
	if structuredText(r) != structuredText(anchor) {
		return ErrConflict
	}
	switch ref.Collection {
	case "sources":
		err = tx.FenceSource(ctx, in.Scope, ref.ID)
	case colMemories:
		err = tx.FenceRelationMember(ctx, in.Scope, colMemories, ref.ID)
	default:
		err = tx.FenceSearchRead(ctx, in.Scope, ref.Collection, ref.ID, ref.Revision)
	}
	if err != nil {
		return err
	}
	return checkRelationAnchor(ctx, checked, ref.Collection, r)
}

func verifySearchHistory(ctx context.Context, tx Tx, checked searchReadTx, in SearchInput, history []HistoricalHit, historyRecords map[string]Record, snapshots map[Ref]string) error {
	for _, h := range history {
		r, err := tx.Get(ctx, colMemories, in.Scope, h.ID)
		if err != nil {
			observeSearchFinalRejection(in, h.Hit, "history", nativeFinalCode(err))
			return err
		}
		if !historySearchable(r) || live(r) || !in.SearchCriteria.matchesRecord(r) || structuredText(r) != structuredText(historyRecords[h.ID]) || searchHitHash(HitFromRecord(colMemories, r)) != snapshots[Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision}] {
			observeSearchFinalRejection(in, h.Hit, "history", "history_changed")
			return ErrConflict
		}
		if err = tx.FenceRelationMember(ctx, in.Scope, colMemories, r.ID); err != nil {
			observeSearchFinalRejection(in, h.Hit, "history", nativeFinalCode(err))
			return err
		}
		if err = checkSources(ctx, checked, r); err != nil {
			observeSearchFinalRejection(in, h.Hit, "history", nativeFinalCode(err))
			return err
		}
	}
	return nil
}

func verifySearchItems(ctx context.Context, checked searchReadTx, in SearchInput, run *Ref, items []Hit, snapshots map[Ref]string) error {
	for _, h := range items {
		r, err := checked.Get(ctx, h.Collection, in.Scope, h.ID)
		if err != nil {
			observeSearchFinalRejection(in, h, "current", nativeFinalCode(err))
			return err
		}
		if r.ContentErased || !live(r) || !in.SearchCriteria.matchesRecord(r) || r.Revision != h.Revision || h.Collection == colMemories && (r.ApprovedBy == "" || run != nil && !verifiedMemory(r)) || searchHitHash(HitFromRecord(h.Collection, r)) != snapshots[Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision}] {
			observeSearchFinalRejection(in, h, "current", "item_changed")
			return ErrConflict
		}
		if err = verifySearchItemSources(ctx, checked, in, run, h.Collection, r); err != nil {
			observeSearchFinalRejection(in, h, "current", nativeFinalCode(err))
			return err
		}
	}
	return nil
}

func verifySearchItemSources(ctx context.Context, checked searchReadTx, in SearchInput, run *Ref, col string, r Record) error {
	var err error
	if col == colMemories && run != nil {
		if err = memoryProvenance(ctx, checked, r); err != nil {
			return err
		}
	}
	if strings.HasPrefix(in.Destination, "external:") {
		err = checkTransferSources(ctx, checked, r.Scope, r.Sensitivity, r.Sources)
	} else {
		err = checkSources(ctx, checked, r)
	}
	if err != nil {
		return err
	}
	if col == colSkillVersions {
		if err = checkActiveSkill(ctx, checked, r); err != nil {
			return err
		}
	}
	if col == colReflections {
		if err = checkReflection(ctx, checked, r); err != nil {
			return err
		}
	}
	return nil
}

func verifySearchRun(ctx context.Context, checked searchReadTx, scope Scope, run *Ref) error {
	row, err := checked.Get(ctx, "agent_runs", scope, run.ID)
	if err != nil {
		return err
	}
	if row.Revision != run.Revision || !live(row) {
		return ErrConflict
	}
	return checkSources(ctx, checked, row)
}

func (s *Service) WorkOnce(ctx context.Context, workerToken string) (result error) {
	// The claimed job's deferred FinishJob runs before this recovery. A panic
	// consumes its attempt and cannot terminate the worker or leak raw content.
	defer func() {
		if recover() != nil {
			result = ErrUnavailable
		}
	}()
	if pg, recovery := s.Repo.(*Postgres); !recovery || !pg.erasureOnly {
		if err := s.CheckRestorePublication(ctx); err != nil {
			return err
		}
	}
	// Give both queues one opportunity per worker iteration. Canonical privacy
	// phases must not wait for an embedding backlog to empty; a blocked privacy
	// phase must likewise not prevent its derived-erasure jobs from running.
	privacyErr := s.advancePrivacyOnce(ctx, workerToken)
	job, e := s.Repo.ClaimJob(ctx, time.Now().UTC(), 2*time.Minute)
	if errors.Is(e, ErrNotFound) {
		return privacyErr // nil when the privacy phase made progress
	}
	defer func() {
		if privacyErr != nil && !errors.Is(privacyErr, ErrNotFound) {
			result = errors.Join(result, privacyErr)
		}
	}()
	if e != nil {
		return e
	}
	success := false
	handled := false
	defer func() {
		if handled {
			return
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.Repo.FinishJob(finishCtx, job, success); result == nil && err != nil {
			result = err
		}
	}()
	if job.Attempts > maxJobAttempts {
		return ErrUnavailable
	}
	if sourceErasureJob(job.Kind) {
		e := s.eraseSourceMemories(ctx, workerToken, job)
		handled = e == nil
		return e
	}
	if job.Kind != "embedding" {
		return ErrInvalid
	}
	success, e = s.embedJob(ctx, workerToken, &job)
	return e
}

// advancePrivacyOnce advances the next due privacy deletion, if any. It
// returns ErrNotFound when no privacy store or phase is waiting.
func (s *Service) advancePrivacyOnce(ctx context.Context, workerToken string) error {
	privacyErr := error(ErrNotFound)
	if privacy, ok := s.Repo.(privacyStore); ok {
		next, nextErr := privacy.NextPrivacyAdvance(ctx, time.Now().UTC())
		privacyErr = nextErr
		if nextErr == nil {
			_, privacyErr = s.AdvancePrivacyDeletion(ctx, workerToken, next.ID, PrivacyRevisionInput{Scope: next.Scope, ExpectedRevision: next.Revision})
		}
	}
	return privacyErr
}

func sourceErasureJob(kind string) bool {
	return kind == sourceMemoryErasureKind || kind == sourceRecordErasureKind || kind == sourceSkillErasureKind || kind == sourceRelationErasureKind || kind == sourceMemoryDependentsErasureKind || isMemoryErasureKind(kind)
}

// embedJob runs one embedding job. The bool reports whether the attempt
// counts as finished; a successful write also advances job.Revision.
func (s *Service) embedJob(ctx context.Context, workerToken string, job *Record) (bool, error) {
	if job.Target == nil || (job.Target.Collection != "memories" && job.Target.Collection != "skill_versions" && job.Target.Collection != colReflections) {
		return true, ErrInvalid
	}
	r, e := s.Repo.Get(ctx, job.Target.Collection, job.Scope, job.Target.ID)
	if e == ErrNotFound {
		return true, nil
	}
	if e != nil {
		return false, e
	}
	if !embeddingEligible(job.Target.Collection, r) || r.Revision != job.Target.Revision {
		return true, nil
	}
	if e = checkSources(ctx, s.Repo, r); e != nil {
		return true, e
	}
	if job.Target.Collection == colSkillVersions {
		if e = checkActiveSkill(ctx, s.Repo, r); e != nil {
			return false, e
		}
	}
	if job.Target.Collection == colReflections && checkReflection(ctx, s.Repo, r) != nil {
		return false, ErrDenied
	}
	content := recordContent(r)
	if r.Sensitivity == "restricted" || !safeTransfer(content) {
		return true, ErrDenied // never embeddable; retrying only piles up failed jobs
	}
	if embeddingCurrent(s.Embedder, r, content) {
		return true, nil
	}
	embedded, e := s.embedDocument(ctx, workerToken, r, content)
	if e != nil {
		return false, e
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		lease, e := tx.Get(ctx, "jobs", job.Scope, job.ID)
		if e != nil {
			return e
		}
		if lease.LeaseToken != job.LeaseToken || !lease.LeaseUntil.After(time.Now()) {
			return ErrConflict
		}
		current, e := tx.Get(ctx, job.Target.Collection, r.Scope, r.ID)
		if e != nil {
			return e
		}
		if !embeddingEligible(job.Target.Collection, current) || current.Revision != job.Target.Revision || recordContent(current) != content {
			return ErrConflict
		}
		if e = checkSources(ctx, tx, current); e != nil {
			return e
		}
		// Write the lease as well as the target: merely reading a lease in a
		// snapshot transaction cannot conflict with concurrent reclamation.
		previous := lease.Revision
		lease.Revision++
		if e = tx.Replace(ctx, "jobs", lease, previous); e != nil {
			return e
		}
		if job.Target.Collection == colSkillVersions {
			if e = checkActiveSkill(ctx, tx, current); e != nil {
				return e
			}
		}
		if job.Target.Collection == colReflections && checkReflection(ctx, tx, current) != nil {
			return ErrDenied
		}
		current.Embedding = &embedded
		return tx.Replace(ctx, job.Target.Collection, current, current.Revision)
	})
	success := e == nil
	if success {
		job.Revision++
	}
	return success, e
}

func embeddingCurrent(embedder Embedder, r Record, content string) bool {
	return embedder != nil && r.Embedding != nil && r.Embedding.Space == embedder.Space() && r.Embedding.Dimension == VectorDimension && len(r.Embedding.Vector) == VectorDimension && r.Embedding.ContentHash == ContentHash(content)
}

// embedDocument authorizes the transfer and embeds content through the
// metered provider call.
func (s *Service) embedDocument(ctx context.Context, workerToken string, r Record, content string) (Embedding, error) {
	if _, e := s.authorizeTransfer(ctx, workerToken, "embed:document", r.Scope, r.ID, r.Sensitivity, "external:embedding", content, r.Sources); e != nil {
		return Embedding{}, e
	}
	if s.Embedder == nil {
		return Embedding{}, ErrDenied
	}
	reserved, e := s.beginProviderCall(ctx, r.Scope, "embedding_document", embeddingProvider(s.Embedder), s.Embedder.Space())
	if e != nil {
		return Embedding{}, e
	}
	batch, e := embedMeasured(ctx, s.Embedder, []string{content}, "document")
	if e != nil {
		_ = s.finishProviderCall(ctx, reserved, ProviderUsage{Provider: embeddingProvider(s.Embedder), Model: s.Embedder.Space(), Requests: 1}, false)
		return Embedding{}, e
	}
	if e = s.finishProviderCall(ctx, reserved, batch.Usage, true); e != nil {
		return Embedding{}, e
	}
	embedded := batch.Items
	if len(embedded) != 1 || len(embedded[0].Vector) != 1024 || embedded[0].Space != s.Embedder.Space() {
		return Embedding{}, ErrInvalid
	}
	embedded[0].ContentHash = ContentHash(content)
	return embedded[0], nil
}

// Approved future memories may be embedded in advance; readers enforce the start
// time. Sources, policy and lease/revision checks still run before publication.
func embeddingEligible(col string, r Record) bool {
	if col == colMemories {
		r.EffectiveAt = nil
	}
	return live(r)
}
