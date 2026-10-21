package memory

import (
	"context"
	"errors"
	"sort"
	"strings"
)

type KnowledgeCatalogPage struct {
	Items      []KnowledgeSourceSummary `json:"items"`
	Total      int                      `json:"total"`
	NextCursor string                   `json:"next_cursor"`
	Authority  string                   `json:"authority"`
	Facets     CatalogFacets            `json:"facets"`
}

func (s *Service) QueryKnowledgeCatalog(ctx context.Context, token string, in CatalogQueryInput) (KnowledgeCatalogPage, error) {
	out := KnowledgeCatalogPage{Items: []KnowledgeSourceSummary{}, Authority: AuthorityDataOnly, Facets: newCatalogFacets()}
	in, err := normalizeCatalog(in, "knowledge")
	if err != nil {
		return out, err
	}
	ctx, actor, err := s.catalogActor(ctx, token, "read:"+colKnowledgeSources, in.Scope)
	if err != nil {
		return out, err
	}
	if _, err := s.knowledgeAuthorization(ctx, token, "read:"+colKnowledgeSources, in.Scope, ""); err != nil {
		return out, err
	}
	rows, err := collectCatalogRecords(ctx, s.Repo, in.Scope, colKnowledgeSources)
	if err != nil {
		return out, err
	}
	bindings, err := s.catalogKnowledgeBindings(ctx, token, in.Scope)
	if err != nil {
		return out, err
	}
	items := []KnowledgeSourceSummary{}
	for _, row := range rows {
		if ctx.Err() != nil {
			return out, ErrUnavailable
		}
		source, e := s.GetKnowledgeSource(ctx, token, row.ID, in.Scope)
		if errors.Is(e, ErrNotFound) {
			continue
		}
		if e != nil {
			return out, e
		}
		item := knowledgeCatalogSummary(source, bindings)
		match, e := s.knowledgeCatalogText(ctx, token, in, item)
		if e != nil {
			return out, e
		}
		if !match {
			continue
		}
		out.Facets.Kinds[item.Format]++
		if in.Format != "" && item.Format != in.Format || in.State != "all" && in.State != knowledgeCatalogState(item) {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if in.Sort == "name" && a.DisplayName != b.DisplayName {
			return strings.ToLower(a.DisplayName) < strings.ToLower(b.DisplayName)
		}
		if in.Sort == "chunks" && a.ChunkCount != b.ChunkCount {
			return a.ChunkCount > b.ChunkCount
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
	out.Total = len(items)
	out.Items, out.NextCursor, err = catalogWindow(items, in, catalogBinding(in, "knowledge", actor.PrincipalID+"/"+actor.Role))
	return out, err
}
func knowledgeCatalogState(item KnowledgeSourceSummary) string {
	if item.LifecycleStatus == "failed" {
		return "실패"
	}
	if item.LifecycleStatus == "draft" || item.TrustLevel != "reviewed" {
		return "검토 대기"
	}
	return "사용 중"
}
func (s *Service) knowledgeCatalogText(ctx context.Context, token string, in CatalogQueryInput, item KnowledgeSourceSummary) (bool, error) {
	if strings.Contains(strings.ToLower(item.DisplayName), strings.ToLower(in.Query)) {
		return true, nil
	}
	// Search all authorized stored section paths, rather than just documents opened in the UI.
	page, err := s.ListKnowledgeDocuments(ctx, token, in.Scope, item.ID)
	if err != nil {
		return false, err
	}
	for _, doc := range page.Items {
		for _, section := range doc.Sections {
			if strings.Contains(strings.ToLower(section), strings.ToLower(in.Query)) {
				return true, nil
			}
		}
	}
	return false, nil
}
func (s *Service) catalogKnowledgeBindings(ctx context.Context, token string, scope Scope) (map[string][]KnowledgeBindingSummary, error) {
	out := map[string][]KnowledgeBindingSummary{}
	if _, err := s.knowledgeAuthorization(ctx, token, "read:"+colKnowledgeBindings, scope, ""); errors.Is(err, ErrDenied) {
		if catalogActorChanged(ctx) {
			return nil, ErrDenied
		}
		return out, nil
	} else if err != nil {
		return nil, err
	}
	rows, err := collectCatalogRecords(ctx, s.Repo, scope, colKnowledgeBindings)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if knowledgeLive(ctx, s.Repo, row) == nil && row.Knowledge != nil {
			out[row.Knowledge.KnowledgeSourceID] = append(out[row.Knowledge.KnowledgeSourceID], knowledgeBindingSummary(row))
		}
	}
	return out, nil
}
