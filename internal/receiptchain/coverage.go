package receiptchain

import "sort"

// Coverage reports available observations. A missing receipt never becomes a
// negative use or task verdict. Canonical audit storage is reported separately.
type Coverage struct {
	Paths              []PathCoverage `json:"paths"`
	CollectionComplete bool           `json:"collection_complete"`
	ActualUse          *bool          `json:"actual_use"`
	TaskSuccess        *bool          `json:"task_success"`
	CausalContribution *bool          `json:"causal_contribution"`
}
type PathCoverage struct {
	Path   string          `json:"path"`
	Stages []StageCoverage `json:"stages"`
}
type StageCoverage struct {
	Stage        string   `json:"stage"`
	State        string   `json:"state"`
	RecordSHA256 []string `json:"record_sha256"`
}

func Assess(snapshot Snapshot) Coverage {
	out := Coverage{Paths: []PathCoverage{}, CollectionComplete: snapshot.Complete}
	paths := map[string]map[string][]string{}
	for _, r := range snapshot.Records {
		if Validate(r) != nil {
			out.CollectionComplete = false
			continue
		}
		if paths[r.Path] == nil {
			paths[r.Path] = map[string][]string{}
		}
		if r.Status == "observed" {
			paths[r.Path][r.Stage] = append(paths[r.Path][r.Stage], r.RecordSHA256)
		}
	}
	names := []string{}
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	for _, path := range names {
		p := PathCoverage{Path: path, Stages: []StageCoverage{}}
		for _, stage := range []string{"server_return", "final_render", "transport_accepted", "reader_bytes_acknowledged", "source_read", "answer_observed"} {
			facts := paths[path][stage]
			if facts == nil {
				facts = []string{}
			}
			state := "unknown"
			if len(facts) > 0 {
				state = "observed"
			}
			p.Stages = append(p.Stages, StageCoverage{stage, state, facts})
		}
		out.Paths = append(out.Paths, p)
	}
	return out
}
