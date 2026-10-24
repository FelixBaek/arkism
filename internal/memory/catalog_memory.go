package memory

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"
)

func (s *Service) QueryMemoryCatalog(ctx context.Context, token string, in CatalogQueryInput, candidates bool) (MemoryCatalogPage, error) {
	out := MemoryCatalogPage{Items: []Record{}, Authority: AuthorityDataOnly, Facets: newCatalogFacets()}
	domain, action := "memories", "read:memories"
	if candidates {
		domain, action = "candidates", reviewAction
	}
	in, err := normalizeCatalog(in, domain)
	if err != nil {
		return out, err
	}
	ctx, actor, err := s.catalogActor(ctx, token, action, in.Scope)
	if err != nil {
		return out, err
	}
	binding := catalogBinding(in, domain, actor.PrincipalID+"/"+actor.Role)
	if _, err = decodeCatalogCursor(in, binding); err != nil {
		return out, err
	}
	rows, err := s.memoryCatalogRows(ctx, token, in, candidates)
	if err != nil {
		return out, err
	}
	usage, tools, err := s.memoryCatalogMetadata(ctx, token, in)
	if err != nil {
		return out, err
	}
	items := filterMemoryCatalog(rows, in, candidates, usage, tools, &out.Facets)
	sort.SliceStable(items, func(i, j int) bool { return memoryCatalogLess(items[i], items[j], in.Sort, usage) })
	out.Total = len(items)
	out.Items, out.NextCursor, err = catalogWindow(items, in, binding)
	return out, err
}
func (s *Service) memoryCatalogRows(ctx context.Context, token string, in CatalogQueryInput, candidates bool) ([]Record, error) {
	if candidates && in.State == "all" {
		active := in
		active.State = "candidate"
		first, err := s.memoryCatalogRows(ctx, token, active, true)
		if err != nil {
			return nil, err
		}
		active.State = "conflicted"
		second, err := s.memoryCatalogRows(ctx, token, active, true)
		return append(first, second...), err
	}
	return collectCatalogPages(ctx, func(cursor string) ([]Record, string, error) {
		if candidates {
			page, err := s.ListCandidates(ctx, token, ReviewInput{omitAdvice: true, Scope: in.Scope, Status: in.State, Limit: 100, Cursor: cursor})
			return page.Items, page.NextCursor, err
		}
		page, err := s.ListMemories(ctx, token, MemoryListInput{Scope: in.Scope, Status: statusActive, Query: in.Query, FoldQuery: true, Limit: 100, Cursor: cursor})
		return page.Items, page.NextCursor, err
	})
}

func catalogRepository(row Record) string {
	for _, tag := range row.Tags {
		if strings.HasPrefix(tag, "repo:") {
			return strings.TrimPrefix(tag, "repo:")
		}
	}
	return ""
}
func memoryCatalogState(row Record, in CatalogQueryInput, usage map[string]int, tools *AgentTools) bool {
	if in.State == "provenance" {
		r := row.Review
		return r == nil || r.ReceiptID == "" || r.ActorRole != roleHuman || r.Channel != "explicit_review_api" || r.By != row.ApprovedBy || !slices.Contains([]string{"approve", "edit_approve"}, r.Action)
	}
	if in.State != "idle" {
		return true
	}
	if row.ReviewDueAt != nil && !row.ReviewDueAt.After(time.Now()) {
		return true
	}
	if catalogReportedUnused(row.ID, in, tools) {
		return true
	}

	return usage[row.ID] == 0 && row.Kind != "user" && row.Kind != "procedural" && row.CreatedAt.Before(time.Now().Add(-7*24*time.Hour))
}
func memoryCatalogLess(a, b Record, mode string, usage map[string]int) bool {
	if mode == "risk" {
		ar, br := catalogRisk(a), catalogRisk(b)
		if ar != br {
			return ar < br
		}
	}
	if mode == "used" && usage[a.ID] != usage[b.ID] {
		return usage[a.ID] > usage[b.ID]
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		if mode == "old" {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID < b.ID
}
func catalogRisk(row Record) int {
	if row.Classification == nil {
		return 3
	}
	if rank, ok := candidateRisks[row.Classification.Risk]; ok {
		return rank
	}
	return 3
}

func (s *Service) memoryCatalogMetadata(ctx context.Context, token string, in CatalogQueryInput) (map[string]int, *AgentTools, error) {
	usage := map[string]int{}
	var tools *AgentTools
	if in.Sort == "used" || in.State == "idle" {
		page, e := s.catalogUsage(ctx, token, in)
		if e != nil {
			return nil, nil, e
		}
		for _, entry := range page.Items {
			usage[entry.MemoryID] = entry.Uses
		}
	}
	if in.State == "idle" {
		var err error
		tools, err = s.catalogTools(ctx, token, in.Scope)
		if err != nil {
			return nil, nil, err
		}
	}
	return usage, tools, nil
}
func filterMemoryCatalog(rows []Record, in CatalogQueryInput, candidates bool, usage map[string]int, tools *AgentTools, facets *CatalogFacets) []Record {
	items := []Record{}
	for _, row := range rows {
		if !strings.Contains(strings.ToLower(row.Text+" "+row.Summary), strings.ToLower(in.Query)) || !memoryCatalogState(row, in, usage, tools) {
			continue
		}
		category := memoryCatalogCategory(row)
		facets.States[category]++
		if candidates && in.Category != "" && category != in.Category {
			continue
		}
		facets.Kinds[row.Kind]++
		repo := catalogRepository(row)
		facets.Repositories[repo]++
		if in.Kind != "" && row.Kind != in.Kind || in.Repository != "" && repo != in.Repository && !(in.Repository == "shared" && repo == "") {
			continue
		}
		items = append(items, row)
	}
	return items
}

func catalogReportedUnused(id string, in CatalogQueryInput, tools *AgentTools) bool {
	if tools != nil && tools.MemoryUse != nil && (in.CollectUnused == nil || *in.CollectUnused) && !slices.Contains(in.KeptIDs, id) {
		for _, stat := range tools.MemoryUse.Items {
			if stat.ID == id && stat.Injected >= 3 && stat.Used == 0 {
				return true
			}
		}
	}
	return false
}

func memoryCatalogCategory(row Record) string {
	if row.ConflictID != "" || row.Status == "conflicted" || row.Classification != nil && row.Classification.DupHint != "" {
		return "confusing"
	}
	return "new"
}

// catalogUsageBudget bounds how long usage counts may hold up a list that only sorts by them.
var catalogUsageBudget = 3 * time.Second

// catalogUsage reads usage counts for the catalog. "idle" decides membership by usage, so it must fail
// with them; "used" only orders rows, so a slow or refused summary falls back to newest first instead of
// leaving the whole list loading until the request deadline (#298: memory_usage ran into the 30 s limit).
func (s *Service) catalogUsage(ctx context.Context, token string, in CatalogQueryInput) (UsageSummaryPage, error) {
	if in.State == "idle" {
		return s.MemoryUsageSummary(ctx, token, in.Scope)
	}
	short, cancel := context.WithTimeout(ctx, catalogUsageBudget)
	defer cancel()
	page, e := s.MemoryUsageSummary(short, token, in.Scope)
	if e != nil && ctx.Err() == nil && !catalogActorChanged(ctx) {
		return UsageSummaryPage{}, nil
	}
	return page, e
}
