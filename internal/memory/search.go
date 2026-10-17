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

// laneByReason names the lane each search reason comes from (ST-811).
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

// FuseScored is Fuse with each hit's score kept, for measurement (ST-810).
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

// Keep the search actor inside the service for multi-step consumers.
func (s *Service) searchWithActor(ctx context.Context, token string, in SearchInput) (ContextBundle, Decision, error) {
	if !validSearchRelation(in) {
		return ContextBundle{}, Decision{}, ErrInvalid
	}
	if in.RelatedTo != nil {
		ref := *in.RelatedTo
		in.RelatedTo = &ref
	}
	if in.IncludeInactive && (in.Purpose != "" && in.Purpose != "answer" || in.Destination != "" && in.Destination != "internal") {
		return ContextBundle{}, Decision{}, ErrInvalid
	}
	if !validSearchModes(in.Modes) {
		return ContextBundle{}, Decision{}, ErrInvalid
	}
	criteria, err := normalizeSearchCriteria(in.SearchCriteria)
	if err != nil {
		return ContextBundle{}, Decision{}, err
	}
	criteria.history = in.IncludeInactive
	in.SearchCriteria = criteria
	if !in.IncludeInactive && in.Filters != nil {
		for _, status := range in.Filters.Statuses {
			if status != "active" {
				return ContextBundle{}, Decision{}, ErrInvalid
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
	out := ContextBundle{Authority: "data_only_untrusted", Items: []Hit{}}
	if in.BudgetTokens < 0 || in.BudgetTokens > 64000 || (in.BudgetTokens == 0) != (in.TokenEncoding == "") || in.TokenEncoding != "" && in.TokenEncoding != "cl100k_base" && in.TokenEncoding != "o200k_base" {
		return out, Decision{}, ErrInvalid
	}
	if in.Destination != "" && in.Destination != "internal" && !destinationPattern.MatchString(in.Destination) {
		return out, Decision{}, ErrInvalid
	}
	if strings.TrimSpace(in.Query) == "" || !utf8.ValidString(in.Query) || utf8.RuneCountInString(in.Query) > 1000 || in.Limit < 0 || in.Limit > 50 || in.MaxChars < 0 || in.MaxChars > 64000 {
		return out, Decision{}, ErrInvalid
	}
	if in.Limit == 0 {
		in.Limit = 8
	}
	if in.MaxChars == 0 {
		in.MaxChars = 12000
	}
	collection := colMemories
	switch in.Purpose {
	case "", "answer", "knowledge":
	case "skills":
		collection = colSkillVersions
	case "self_improvement", "risk", "skill_validation":
		collection = colReflections
	default:
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
	var run *Ref
	if in.RunID != "" {
		if len(in.RunID) > 256 || strings.TrimSpace(in.RunID) != in.RunID || containsSecret(in.RunID) {
			return ContextBundle{}, Decision{}, ErrInvalid
		}
		rd, err := s.authorize(ctx, token, "read:agent_runs", in.Scope, in.RunID, "")
		if err != nil {
			return ContextBundle{}, Decision{}, err
		}
		if rd.PrincipalID != d.PrincipalID || rd.Role != d.Role {
			return ContextBundle{}, Decision{}, ErrDenied
		}
		row, err := s.Repo.Get(ctx, "agent_runs", in.Scope, in.RunID)
		if err != nil {
			return ContextBundle{}, Decision{}, err
		}
		if !live(row) {
			return ContextBundle{}, Decision{}, ErrConflict
		}
		if err = checkSources(ctx, s.Repo, row); err != nil {
			return ContextBundle{}, Decision{}, err
		}
		run = &Ref{Collection: "agent_runs", ID: row.ID, Revision: row.Revision}
	}
	var lists [][]Hit
	var memoryErr error
	var degraded []string
	var queryVector []float64
	var querySpace string
	func() {
		var vec []float64
		space := ""
		if searchModeEnabled(in.Modes, "vector") {
			if d.ExternalTransfer && d.ContentSafe && s.Embedder != nil && safeTransfer(searchContent) {
				reserved, e := s.beginProviderCall(ctx, in.Scope, "embedding_query", embeddingProvider(s.Embedder), s.Embedder.Space())
				if e != nil {
					memoryErr = e
					return
				}
				batch, e := embedMeasured(ctx, s.Embedder, []string{in.Query}, "query")
				if e != nil {
					_ = s.finishProviderCall(ctx, reserved, ProviderUsage{Provider: embeddingProvider(s.Embedder), Model: s.Embedder.Space(), Requests: 1}, false)
					degraded = append(degraded, "memory_vector_unavailable")
				} else if e = s.finishProviderCall(ctx, reserved, batch.Usage, true); e != nil {
					memoryErr = e
					return
				} else if len(batch.Items) != 1 {
					degraded = append(degraded, "memory_vector_unavailable")
				} else {
					vec = batch.Items[0].Vector
					space = s.Embedder.Space()
				}
			} else {
				degraded = append(degraded, "memory_vector_disabled")
			}
		}
		retrieval := in
		if in.retrievalQuery != "" {
			retrieval.Query = in.retrievalQuery
		}
		lists, memoryErr = s.Repo.SearchCollection(ctx, collection, retrieval, vec, space)
		queryVector, querySpace = vec, space
	}()
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
	for i, list := range lists {
		clean := []Hit{}
		for _, h := range list {
			if h.Scope != in.Scope || !in.IncludeInactive && h.Status != "active" || len(h.Sources) == 0 {
				continue
			}
			if h.Collection != collection {
				continue
			}
			r, e := s.Repo.Get(ctx, h.Collection, in.Scope, h.ID)
			if e == nil && i == relationLane && !relationMatches(in.RelatedTo.Collection, anchor, r) {
				continue
			}
			if e != nil || (!in.IncludeInactive && !live(r)) || (in.IncludeInactive && !historySearchable(r)) || !criteria.matchesRecord(r) || (h.Collection == colMemories && (r.ApprovedBy == "" || run != nil && !verifiedMemory(r))) || (!strings.HasPrefix(in.Destination, "external:") && checkSources(ctx, s.Repo, r) != nil) {
				continue
			}
			if h.Collection == colReflections && checkReflection(ctx, s.Repo, r) != nil {
				continue
			}
			if h.Collection == "skill_versions" {
				if checkActiveSkill(ctx, s.Repo, r) != nil || r.Analysis == nil {
					continue
				}
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
			if !criteria.matchesHit(h) {
				continue
			}
			var allowed Decision
			if _, historical := historyRecords[h.ID]; historical {
				allowed, e = s.authorize(ctx, token, "read:memories", in.Scope, h.ID, structuredText(historyRecords[h.ID]))
				if e != nil {
					if !errors.Is(e, ErrDenied) {
						return ContextBundle{}, Decision{}, e
					}
					continue
				}
			} else if strings.HasPrefix(in.Destination, "external:") {
				allowed, e = s.authorizeTransfer(ctx, token, "context:read", in.Scope, h.ID, h.Sensitivity, in.Destination, structuredText(h), h.Sources)
				if e != nil {
					if !slices.Contains(out.Degraded, "external_content_filtered") {
						out.Degraded = append(out.Degraded, "external_content_filtered")
					}
					continue
				}
			} else {
				allowed, e = s.authorize(ctx, token, "context:read", in.Scope, h.ID, structuredText(h))
				if e != nil {
					if !slices.Contains(out.Degraded, "context_content_filtered") {
						out.Degraded = append(out.Degraded, "context_content_filtered")
					}
					continue
				}
			}
			if allowed.PrincipalID != d.PrincipalID || allowed.Role != d.Role {
				return ContextBundle{}, Decision{}, ErrDenied
			}
			if slices.Contains(why, reasonVector) {
				h.VectorMatch = verifiedSemanticMatch(r, queryVector, querySpace, distanceLimit)
			}
			clean = append(clean, h)
		}
		lists[i] = clean
	}
	scored := FuseScored(lists, in.Limit)
	out.Items = make([]Hit, len(scored))
	for i, r := range scored {
		r.Hit.Score, r.Hit.Lanes = r.Score, hitLanes(r.Hit.Reasons)
		out.Items[i] = r.Hit
	}
	snapshots := map[Ref]string{}
	for _, h := range out.Items {
		snapshots[Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision}] = searchHitHash(h)
	}
	left := in.MaxChars
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
	if in.BudgetTokens > 0 {
		if e = packContext(ctx, &out, in.BudgetTokens, in.TokenEncoding); e != nil {
			return ContextBundle{}, Decision{}, e
		}
	}
	out.ContextID = ID()
	usage := pinnedUsage(run, in.Purpose, in.pinned)
	usage.ProjectKey = in.ProjectKey
	for i, h := range out.Items {
		if h.Collection == colMemories && h.Origin == "memory" && !usedRef(usage, h.ID) {
			usage.Items = append(usage.Items, MemoryUse{Memory: Ref{Collection: colMemories, ID: h.ID, Revision: h.Revision}, Rank: len(in.pinned) + i + 1, Score: h.Score, Lanes: h.Lanes})
		}
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		checked := searchReadTx{Tx: tx}
		if in.RelatedTo != nil {
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
			if err = checkRelationAnchor(ctx, checked, ref.Collection, r); err != nil {
				return err
			}
		}
		for _, h := range out.History {
			r, err := tx.Get(ctx, colMemories, in.Scope, h.ID)
			if err != nil {
				return err
			}
			if !historySearchable(r) || live(r) || !criteria.matchesRecord(r) || structuredText(r) != structuredText(historyRecords[h.ID]) || searchHitHash(HitFromRecord(colMemories, r)) != snapshots[Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision}] {
				return ErrConflict
			}
			if err = tx.FenceRelationMember(ctx, in.Scope, colMemories, r.ID); err != nil {
				return err
			}
			if err = checkSources(ctx, checked, r); err != nil {
				return err
			}
		}
		for _, h := range out.Items {
			r, err := checked.Get(ctx, h.Collection, in.Scope, h.ID)
			if err != nil {
				return err
			}
			if r.ContentErased || !live(r) || !criteria.matchesRecord(r) || r.Revision != h.Revision || h.Collection == colMemories && (r.ApprovedBy == "" || run != nil && !verifiedMemory(r)) || searchHitHash(HitFromRecord(h.Collection, r)) != snapshots[Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision}] {
				return ErrConflict
			}
			if h.Collection == colMemories && run != nil {
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
			if h.Collection == colSkillVersions {
				if err = checkActiveSkill(ctx, checked, r); err != nil {
					return err
				}
			}
			if h.Collection == colReflections {
				if err = checkReflection(ctx, checked, r); err != nil {
					return err
				}
			}
		}
		if run != nil {
			row, err := checked.Get(ctx, "agent_runs", in.Scope, run.ID)
			if err != nil {
				return err
			}
			if row.Revision != run.Revision || !live(row) {
				return ErrConflict
			}
			if err = checkSources(ctx, checked, row); err != nil {
				return err
			}
		}
		if err := insertUsageReceipt(ctx, tx, in.Scope, out.ContextID, d.PrincipalID, usage); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "search", ContentHash(in.Query))
	}); e != nil {
		return ContextBundle{}, Decision{}, e
	}
	return out, d, nil
}

func (s *Service) WorkOnce(ctx context.Context, workerToken string) (result error) {
	if pg, recovery := s.Repo.(*Postgres); !recovery || !pg.erasureOnly {
		if err := s.CheckRestorePublication(ctx); err != nil {
			return err
		}
	}
	// Give both queues one opportunity per worker iteration. Canonical privacy
	// phases must not wait for an embedding backlog to empty; a blocked privacy
	// phase must likewise not prevent its derived-erasure jobs from running.
	privacyErr := error(ErrNotFound)
	if privacy, ok := s.Repo.(privacyStore); ok {
		next, nextErr := privacy.NextPrivacyAdvance(ctx, time.Now().UTC())
		privacyErr = nextErr
		if nextErr == nil {
			_, privacyErr = s.AdvancePrivacyDeletion(ctx, workerToken, next.ID, PrivacyRevisionInput{Scope: next.Scope, ExpectedRevision: next.Revision})
		}
	}
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
	if job.Kind == sourceMemoryErasureKind || job.Kind == sourceRecordErasureKind || job.Kind == sourceSkillErasureKind || job.Kind == sourceRelationErasureKind || job.Kind == sourceMemoryDependentsErasureKind || isMemoryErasureKind(job.Kind) {
		e := s.eraseSourceMemories(ctx, workerToken, job)
		handled = e == nil
		return e
	}
	if job.Kind != "embedding" {
		return ErrInvalid
	}
	if job.Target == nil || (job.Target.Collection != "memories" && job.Target.Collection != "skill_versions" && job.Target.Collection != colReflections) {
		success = true
		return ErrInvalid
	}
	r, e := s.Repo.Get(ctx, job.Target.Collection, job.Scope, job.Target.ID)
	if e == ErrNotFound {
		success = true
		return nil
	}
	if e != nil {
		return e
	}
	if !embeddingEligible(job.Target.Collection, r) || r.Revision != job.Target.Revision {
		success = true
		return nil
	}
	if e = checkSources(ctx, s.Repo, r); e != nil {
		success = true
		return e
	}
	if job.Target.Collection == colSkillVersions {
		if e = checkActiveSkill(ctx, s.Repo, r); e != nil {
			return e
		}
	}
	if job.Target.Collection == colReflections && checkReflection(ctx, s.Repo, r) != nil {
		return ErrDenied
	}
	content := recordContent(r)
	if r.Sensitivity == "restricted" || !safeTransfer(content) {
		success = true // never embeddable; retrying only piles up failed jobs
		return ErrDenied
	}
	if s.Embedder != nil && r.Embedding != nil && r.Embedding.Space == s.Embedder.Space() && r.Embedding.Dimension == VectorDimension && len(r.Embedding.Vector) == VectorDimension && r.Embedding.ContentHash == ContentHash(content) {
		success = true
		return nil
	}
	if _, e = s.authorizeTransfer(ctx, workerToken, "embed:document", r.Scope, r.ID, r.Sensitivity, "external:embedding", content, r.Sources); e != nil {
		return e
	}
	if s.Embedder == nil {
		return ErrDenied
	}
	reserved, e := s.beginProviderCall(ctx, r.Scope, "embedding_document", embeddingProvider(s.Embedder), s.Embedder.Space())
	if e != nil {
		return e
	}
	batch, e := embedMeasured(ctx, s.Embedder, []string{content}, "document")
	if e != nil {
		_ = s.finishProviderCall(ctx, reserved, ProviderUsage{Provider: embeddingProvider(s.Embedder), Model: s.Embedder.Space(), Requests: 1}, false)
		return e
	}
	if e = s.finishProviderCall(ctx, reserved, batch.Usage, true); e != nil {
		return e
	}
	embedded := batch.Items
	if len(embedded) != 1 || len(embedded[0].Vector) != 1024 || embedded[0].Space != s.Embedder.Space() {
		return ErrInvalid
	}
	embedded[0].ContentHash = ContentHash(content)
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
		current.Embedding = &embedded[0]
		return tx.Replace(ctx, job.Target.Collection, current, current.Revision)
	})
	success = e == nil
	if success {
		job.Revision++
	}
	return e
}

// Approved future memories may be embedded in advance; readers enforce the start
// time. Sources, policy and lease/revision checks still run before publication.
func embeddingEligible(col string, r Record) bool {
	if col == colMemories {
		r.EffectiveAt = nil
	}
	return live(r)
}
