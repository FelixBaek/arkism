package memory

import (
	"net/http"
	"strconv"
	"time"
)

func registerEntitiesCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/entities/extract", s.coreHTTPPostV1EntitiesExtract)
	mux.HandleFunc("GET /v1/entities", s.coreHTTPGetV1Entities)
	mux.HandleFunc("GET /v1/entities/graph", s.coreHTTPGetV1EntitiesGraph)
	mux.HandleFunc("GET /v1/entities/{id}", s.coreHTTPGetV1EntitiesId)
	mux.HandleFunc("PATCH /v1/entities/{id}", s.coreHTTPPatchV1EntitiesId)
	mux.HandleFunc("POST /v1/entities/{id}/merge", s.coreHTTPPostV1EntitiesIdMerge)
	mux.HandleFunc("POST /v1/entities/{id}/summary", s.coreHTTPPostV1EntitiesIdSummary)
	mux.HandleFunc("DELETE /v1/entities/{id}", s.coreHTTPDeleteV1EntitiesId)
}

func (s *Service) coreHTTPPostV1EntitiesExtract(w http.ResponseWriter, r *http.Request) {
	var in EntityExtractInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.ExtractEntities(r.Context(), bearer(r), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPGetV1Entities(w http.ResponseWriter, r *http.Request) {
	scope, q, err := parseWikiQuery(r.URL.RawQuery, "q", "type", "limit", "cursor")
	if err != nil {
		respond(w, nil, err)
		return
	}
	limit, err := optionalInt(q.Get("limit"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := s.ListEntities(r.Context(), bearer(r), EntityListInput{Scope: scope, Query: q.Get("q"), Type: q.Get("type"), Limit: limit, Cursor: q.Get("cursor")})
	respond(w, out, err)
}

func (s *Service) coreHTTPGetV1EntitiesGraph(w http.ResponseWriter, r *http.Request) {
	scope, q, err := parseWikiQuery(r.URL.RawQuery, "type", "from", "to", "limit")
	if err != nil {
		respond(w, nil, err)
		return
	}
	in := EntityGraphInput{Scope: scope, Type: q.Get("type")}
	if in.Limit, err = optionalInt(q.Get("limit")); err == nil {
		if in.From, err = optionalTime(q.Get("from")); err == nil {
			in.To, err = optionalTime(q.Get("to"))
		}
	}
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := s.EntityGraphView(r.Context(), bearer(r), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPGetV1EntitiesId(w http.ResponseWriter, r *http.Request) {
	scope, _, err := parseWikiQuery(r.URL.RawQuery)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := s.GetEntity(r.Context(), bearer(r), scope, r.PathValue("id"))
	respond(w, out, err)
}

func (s *Service) coreHTTPPatchV1EntitiesId(w http.ResponseWriter, r *http.Request) {
	var in EntityPatchInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.PatchEntity(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1EntitiesIdMerge(w http.ResponseWriter, r *http.Request) {
	var in EntityMergeInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.MergeEntity(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1EntitiesIdSummary(w http.ResponseWriter, r *http.Request) {
	var in EntitySummaryInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.DraftEntitySummary(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPDeleteV1EntitiesId(w http.ResponseWriter, r *http.Request) {
	var in EntityScopeInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.DeleteEntity(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, out, err)
}

func optionalInt(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, ErrInvalid
	}
	return n, nil
}

func optionalTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, ErrInvalid
	}
	return &t, nil
}
