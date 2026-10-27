package crewrun

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/FelixBaek/arkism/internal/flowresult"
)

type researchReport struct {
	Title   string   `json:"title"`
	Summary []string `json:"summary"`
	Gaps    []string `json:"gaps"`
	Sources []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"sources"`
	NeedsMoreEvidence *bool `json:"needs_more_evidence"`
}

type researchVerdict struct {
	NeedsMoreEvidence bool   `json:"needs_more_evidence"`
	SourceCount       int    `json:"source_count"`
	GapCount          int    `json:"gap_count"`
	Assessment        string `json:"assessment"`
}

// researchRoute checks reported evidence structure. The model remains
// responsible for its claims and for classifying remaining gaps as nonpublic;
// this gate does not verify factual truth or use elapsed time as evidence.
func researchRoute(raw string, c map[string]string, ports []string) (string, researchVerdict, error) {
	report, err := decodeResearchReport(raw)
	if err != nil {
		return "", researchVerdict{}, err
	}
	needs := len(report.Gaps) > 0
	if report.NeedsMoreEvidence != nil {
		needs = *report.NeedsMoreEvidence
	}
	if len(report.Sources) == 0 {
		needs = true
	}
	verdict := researchVerdict{NeedsMoreEvidence: needs, SourceCount: len(report.Sources), GapCount: len(report.Gaps), Assessment: "structured_report_only_not_fact_verification"}
	if needs {
		if port := c["research_need_port"]; port != "" && slices.Contains(ports, port) {
			return port, verdict, nil
		}
		if len(report.Sources) == 0 {
			return "", verdict, failf("research_sources_missing", "실제로 읽은 출처가 없어 조사 완료로 처리할 수 없어요")
		}
		return "", verdict, failf("research_incomplete", "정해 둔 보완 횟수 후에도 확인할 공개 근거가 부족해요")
	}
	if slices.Contains(ports, "done") {
		return "done", verdict, nil
	}
	return "", verdict, nil
}

func decodeResearchReport(raw string) (researchReport, error) {
	var report researchReport
	var fields map[string]json.RawMessage
	decoded, err := flowresult.ParseResearchJSON(raw)
	if err != nil || json.Unmarshal(decoded, &report) != nil || json.Unmarshal(decoded, &fields) != nil || !validResearchReportShape(report, fields) || !validResearchSources(report) {
		return researchReport{}, failf("research_validation", "보고서 JSON의 본문·출처·미확인 항목 형식이 올바르지 않아요")
	}
	return report, nil
}

func validResearchReportShape(report researchReport, fields map[string]json.RawMessage) bool {
	if strings.TrimSpace(report.Title) == "" || len(report.Summary) == 0 || len(report.Sources) > 100 || len(report.Gaps) > 100 {
		return false
	}
	for _, key := range []string{"summary", "sources", "gaps"} {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return false
		}
	}
	for _, line := range append(append([]string{}, report.Summary...), report.Gaps...) {
		if strings.TrimSpace(line) == "" {
			return false
		}
	}
	return true
}

func validResearchSources(report researchReport) bool {
	for _, source := range report.Sources {
		u, err := url.Parse(source.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.TrimSpace(source.Title) == "" {
			return false
		}
	}
	return true
}

func researchOutputSize(output any) error {
	if !flowresult.FitsNodeOutput(output) {
		return failf("research_output_too_large", "조사 결과가 저장 한도 %d자를 넘어 잘라서 완료 처리하지 않았어요", maxStored)
	}
	return nil
}

// A graph may carry both plan→deep and approve→deep edges. The scheduler's OR
// join cannot make the latter mandatory, so the search checks the persisted
// human decision again immediately before it reaches the helper.
func (x *runExec) researchApproval(n Node) error {
	gate := n.Config["research_approval"]
	if gate == "" {
		return nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	decision, decided := x.run.State.Decisions[gate]
	state := x.state(gate)
	if !decided || !decision.Approve || state == nil || state.Status != "done" || state.Port != "approve" || x.nodes[gate].Type != "approval" {
		return failf("research_approval_required", "딥리서치 검색은 저장된 승인 결정이 있어야 실행할 수 있어요")
	}
	return nil
}
