package memory

import (
	"net/http"
)

func registerSkillsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/skills/analyze", func(w http.ResponseWriter, r *http.Request) {
		var in AnalyzeSkillInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.AnalyzeSkill(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/comparisons/record", func(w http.ResponseWriter, r *http.Request) {
		var in RecordSkillComparisonInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RecordSkillComparison(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/propose", func(w http.ResponseWriter, r *http.Request) {
		var in ProposeSkillInput
		if !decodeLimit(w, r, &in, 8<<20) {
			return
		}
		v, e := s.ProposeSkill(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/generate", func(w http.ResponseWriter, r *http.Request) {
		var in GenerateSkillInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.GenerateSkill(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/import", func(w http.ResponseWriter, r *http.Request) {
		var in ImportSkillInput
		if !decodeLimit(w, r, &in, 8<<20) {
			return
		}
		v, e := s.ImportSkill(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/resolve", func(w http.ResponseWriter, r *http.Request) {
		var in SkillResolveInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ResolveSkills(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/skills", func(w http.ResponseWriter, r *http.Request) {
		scope, limit, cursor, e := parseSkillListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ListSkills(r.Context(), bearer(r), scope, limit, cursor)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/skills/trash", func(w http.ResponseWriter, r *http.Request) {
		scope, limit, cursor, e := parseSkillListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ListTrashedSkills(r.Context(), bearer(r), scope, limit, cursor)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/route", func(w http.ResponseWriter, r *http.Request) {
		var in SearchInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RouteSkills(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/{id}/availability", func(w http.ResponseWriter, r *http.Request) {
		var in SkillAvailabilityInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.SetSkillAvailability(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
}

func registerSkilltrialsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/skill-trials/list", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialListInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ListSkillTrials(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/observe", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ObserveSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/claim-check", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ClaimSkillTrialCheck(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/finish-check", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialCheckFinish
		if !decode(w, r, &in) {
			return
		}
		e := s.FinishSkillTrialCheck(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, map[string]bool{"ok": e == nil}, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/monitor", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.MonitorSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/inspect", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.InspectSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.StartSkillTrial(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialStopInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.StopSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
}

func registerSkilltrialoutcomesCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/skill-trial-outcomes", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialOutcomeInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RecordSkillTrialOutcome(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
}
