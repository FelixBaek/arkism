package memory

import (
	"encoding/base64"
	"mime"
	"net/http"
	"strconv"
)

func registerSkillsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/skills/analyze", s.coreHTTPPostV1SkillsAnalyze)
	mux.HandleFunc("POST /v1/skills/comparisons/record", s.coreHTTPPostV1SkillsComparisonsRecord)
	mux.HandleFunc("POST /v1/skills/propose", s.coreHTTPPostV1SkillsPropose)
	mux.HandleFunc("POST /v1/skills/generate", s.coreHTTPPostV1SkillsGenerate)
	mux.HandleFunc("POST /v1/skills/import", s.coreHTTPPostV1SkillsImport)
	mux.HandleFunc("POST /v1/skills/resolve", s.coreHTTPPostV1SkillsResolve)
	mux.HandleFunc("GET /v1/skills", s.coreHTTPGetV1Skills)
	mux.HandleFunc("GET /v1/skills/trash", s.coreHTTPGetV1SkillsTrash)
	mux.HandleFunc("POST /v1/skills/route", s.coreHTTPPostV1SkillsRoute)
	mux.HandleFunc("POST /v1/skills/{id}/availability", s.coreHTTPPostV1SkillsIdAvailability)
	mux.HandleFunc("GET /v1/skill_versions/{id}/zip", s.coreHTTPGetV1SkillVersionsIdZip)
}

// coreHTTPGetV1SkillVersionsIdZip serves the zip; format=json wraps it as {filename, content_base64}
// for the desktop app, whose API bridge reads every answer as JSON text.
func (s *Service) coreHTTPGetV1SkillVersionsIdZip(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	q.Del("format")
	scope, err := parseSourceTraceQuery(q.Encode())
	if err == nil && format != "" && format != "json" {
		err = ErrInvalid
	}
	if err != nil {
		respond(w, nil, err)
		return
	}
	data, name, err := s.ExportSkillZip(r.Context(), bearer(r), scope, r.PathValue("id"))
	if err != nil || format == "json" {
		respond(w, SkillZipExport{Filename: name, ContentBase64: base64.StdEncoding.EncodeToString(data)}, err)
		return
	}
	w.Header().Set(headerContentType, "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func registerSkilltrialsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/skill-trials/list", s.coreHTTPPostV1SkillTrialsList)
	mux.HandleFunc("POST /v1/skill-trials/{id}/observe", s.coreHTTPPostV1SkillTrialsIdObserve)
	mux.HandleFunc("POST /v1/skill-trials/{id}/claim-check", s.coreHTTPPostV1SkillTrialsIdClaimCheck)
	mux.HandleFunc("POST /v1/skill-trials/{id}/finish-check", s.coreHTTPPostV1SkillTrialsIdFinishCheck)
	mux.HandleFunc("POST /v1/skill-trials/{id}/monitor", s.coreHTTPPostV1SkillTrialsIdMonitor)
	mux.HandleFunc("POST /v1/skill-trials/{id}/inspect", s.coreHTTPPostV1SkillTrialsIdInspect)
	mux.HandleFunc("POST /v1/skill-trials", s.coreHTTPPostV1SkillTrials)
	mux.HandleFunc("POST /v1/skill-trials/{id}/stop", s.coreHTTPPostV1SkillTrialsIdStop)
}

func registerSkilltrialoutcomesCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/skill-trial-outcomes", s.coreHTTPPostV1SkillTrialOutcomes)
}

func (s *Service) coreHTTPPostV1SkillsAnalyze(w http.ResponseWriter, r *http.Request) {
	var in AnalyzeSkillInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.AnalyzeSkill(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillsComparisonsRecord(w http.ResponseWriter, r *http.Request) {
	var in RecordSkillComparisonInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.RecordSkillComparison(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillsPropose(w http.ResponseWriter, r *http.Request) {
	var in ProposeSkillInput
	if !decodeLimit(w, r, &in, 8<<20) {
		return
	}
	v, e := s.ProposeSkill(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillsGenerate(w http.ResponseWriter, r *http.Request) {
	var in GenerateSkillInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.GenerateSkill(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillsImport(w http.ResponseWriter, r *http.Request) {
	var in ImportSkillInput
	if !decodeLimit(w, r, &in, 8<<20) {
		return
	}
	v, e := s.ImportSkill(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillsResolve(w http.ResponseWriter, r *http.Request) {
	var in SkillResolveInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.ResolveSkills(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPGetV1Skills(w http.ResponseWriter, r *http.Request) {
	scope, limit, cursor, e := parseSkillListQuery(r.URL.RawQuery)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.ListSkills(r.Context(), bearer(r), scope, limit, cursor)
	respond(w, v, e)
}

func (s *Service) coreHTTPGetV1SkillsTrash(w http.ResponseWriter, r *http.Request) {
	scope, limit, cursor, e := parseSkillListQuery(r.URL.RawQuery)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.ListTrashedSkills(r.Context(), bearer(r), scope, limit, cursor)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillsRoute(w http.ResponseWriter, r *http.Request) {
	var in SearchInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.RouteSkills(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillsIdAvailability(w http.ResponseWriter, r *http.Request) {
	var in SkillAvailabilityInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.SetSkillAvailability(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillTrialsList(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialListInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.ListSkillTrials(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillTrialsIdObserve(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialReadInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.ObserveSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillTrialsIdClaimCheck(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialReadInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.ClaimSkillTrialCheck(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillTrialsIdFinishCheck(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialCheckFinish
	if !decode(w, r, &in) {
		return
	}
	e := s.FinishSkillTrialCheck(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, map[string]bool{"ok": e == nil}, e)
}

func (s *Service) coreHTTPPostV1SkillTrialsIdMonitor(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialReadInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.MonitorSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillTrialsIdInspect(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialReadInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.InspectSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillTrials(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.StartSkillTrial(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillTrialsIdStop(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialStopInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.StopSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1SkillTrialOutcomes(w http.ResponseWriter, r *http.Request) {
	var in SkillTrialOutcomeInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.RecordSkillTrialOutcome(r.Context(), bearer(r), in)
	respond(w, v, e)
}
