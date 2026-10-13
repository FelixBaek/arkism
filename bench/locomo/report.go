package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type catStat struct {
	N        int     `json:"n"`
	Correct  int     `json:"correct"`
	Accuracy float64 `json:"accuracy"`
}

type sysSummary struct {
	catStat
	ByCategory       map[string]*catStat `json:"by_category"`
	AvgRetrieved     float64             `json:"avg_retrieved"`
	AvgContextTokens float64             `json:"avg_context_tokens_est"`
	AvgAnswerInput   float64             `json:"avg_answer_input_tokens"`
}

type reportTimes struct {
	Ingest, QA, Total time.Duration
}

// addTimes adds this invocation's wall time to the run's total in path, so a resumed run
// reports the time all its invocations took.
func addTimes(path string, t reportTimes) (reportTimes, error) {
	var prev map[string]float64
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &prev)
	}
	sec := func(k string, d time.Duration) float64 { return prev[k] + d.Seconds() }
	cur := map[string]float64{"ingest": sec("ingest", t.Ingest), "qa": sec("qa", t.QA), "total": sec("total", t.Total)}
	b, _ := json.Marshal(cur)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return t, err
	}
	d := func(k string) time.Duration { return time.Duration(cur[k] * float64(time.Second)) }
	return reportTimes{Ingest: d("ingest"), QA: d("qa"), Total: d("total")}, nil
}

func kvList(m map[string]int) string {
	if len(m) == 0 {
		return "없음"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, ", ")
}

type report struct {
	Commit        string                 `json:"commit"`
	GoVersion     string                 `json:"go_version"`
	Generated     string                 `json:"generated"`
	Config        config                 `json:"config"`
	DatasetURL    string                 `json:"dataset_url"`
	DatasetSHA256 string                 `json:"dataset_sha256"`
	Conversations []string               `json:"conversations"`
	Sessions      int                    `json:"sessions_ingested"`
	Claims        int                    `json:"claims_extracted"`
	Approved      int                    `json:"candidates_approved"`
	CaptureStatus map[string]int         `json:"capture_statuses"`
	Questions     int                    `json:"questions"`
	Failed        int                    `json:"failed"`
	Calls         map[string]int         `json:"cli_calls"`
	CostUSD       float64                `json:"cli_reported_cost_usd_answer_judge"`
	WallSeconds   map[string]float64     `json:"wall_seconds"`
	Systems       []string               `json:"systems"`
	Summary       map[string]*sysSummary `json:"summary"`
	Rows          []Row                  `json:"rows"`
}

func gitCommit() string {
	dir, _ := os.Getwd()
	rev, err := exec.Command("git", "-C", dir, "-c", "safe.directory=*", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	c := strings.TrimSpace(string(rev))
	if st, err := exec.Command("git", "-C", dir, "-c", "safe.directory=*", "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(st))) > 0 {
		c += "-dirty"
	}
	return c
}

func buildReport(c config, rows []Row, convs []Conversation, t reportTimes, failed int) *report {
	r := &report{Commit: gitCommit(), GoVersion: runtime.Version(), Generated: time.Now().UTC().Format(time.RFC3339), Config: c,
		DatasetURL: datasetURL, DatasetSHA256: datasetSHA256, CaptureStatus: map[string]int{}, Calls: map[string]int{},
		Summary: map[string]*sysSummary{}, Failed: failed,
		WallSeconds: map[string]float64{"ingest": t.Ingest.Seconds(), "qa": t.QA.Seconds(), "total": t.Total.Seconds()}}
	for _, cv := range convs {
		r.Conversations = append(r.Conversations, cv.SampleID)
	}
	type acc struct{ retrieved, ctx, input float64 }
	sums := map[string]*acc{}
	qset := map[string]bool{}
	for _, row := range rows {
		for k, v := range row.Calls {
			r.Calls[k] += v
		}
		r.CostUSD += row.CostUSD
		if row.Type == "ingest" {
			r.Sessions++
			r.Claims += row.Claims
			r.Approved += row.Approved
			for k, v := range row.Statuses {
				r.CaptureStatus[k] += v
			}
			continue
		}
		r.Rows = append(r.Rows, row)
		qset[row.Conv+"|"+itoa(row.QIndex)] = true
		s := r.Summary[row.System]
		if s == nil {
			s = &sysSummary{ByCategory: map[string]*catStat{}}
			r.Summary[row.System] = s
			sums[row.System] = &acc{}
			r.Systems = append(r.Systems, row.System)
		}
		s.N++
		s.Correct += row.Score
		cs := s.ByCategory[row.CategoryName]
		if cs == nil {
			cs = &catStat{}
			s.ByCategory[row.CategoryName] = cs
		}
		cs.N++
		cs.Correct += row.Score
		sums[row.System].retrieved += float64(row.Retrieved)
		sums[row.System].ctx += float64(row.ContextTokensEst)
		sums[row.System].input += float64(row.AnswerInputTokens)
	}
	r.Questions = len(qset)
	order := map[string]int{"arkism": 0, "no-memory": 1, "full-context": 2}
	sort.Slice(r.Systems, func(i, j int) bool { return order[r.Systems[i]] < order[r.Systems[j]] })
	for name, s := range r.Summary {
		if s.N > 0 {
			s.Accuracy = float64(s.Correct) / float64(s.N)
			s.AvgRetrieved = sums[name].retrieved / float64(s.N)
			s.AvgContextTokens = sums[name].ctx / float64(s.N)
			s.AvgAnswerInput = sums[name].input / float64(s.N)
		}
		for _, cs := range s.ByCategory {
			cs.Accuracy = float64(cs.Correct) / float64(cs.N)
		}
	}
	return r
}

func writeReport(dir string, r *report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(markdown(r)), 0o644)
}

func pct(c catStat) string {
	if c.N == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%% (%d/%d)", 100*c.Accuracy, c.Correct, c.N)
}

func markdown(r *report) string {
	var b strings.Builder
	c := r.Config
	fmt.Fprintf(&b, "# LoCoMo 벤치마크 결과 — %s\n\n", c.RunID)
	fmt.Fprintf(&b, "- 커밋: `%s` (%s)\n", r.Commit, r.GoVersion)
	fmt.Fprintf(&b, "- 데이터셋: `%s`\n  - sha256 `%s` (CC BY-NC 4.0, 저장소에 포함하지 않음)\n", r.DatasetURL, r.DatasetSHA256)
	fmt.Fprintf(&b, "- 변형: `%s` · 추출 모델 `%s` · 답변 모델 `%s` · 채점 모델 `%s` (모두 Claude CLI)\n", c.Variant, c.ExtractModel, c.AnswerModel, c.JudgeModel)
	fmt.Fprintf(&b, "- 범위: workspace `%s`, project `%s` · 검색 limit %d · 동시 실행 %d · cue 필터 %v\n", c.Workspace, c.ProjectTemplate, c.SearchLimit, c.Concurrency, c.CueFilter)
	sessions := "전체"
	if c.Sessions > 0 {
		sessions = fmt.Sprintf("앞 %d개", c.Sessions)
	}
	fmt.Fprintf(&b, "- 대화 %d개 (%s), 대화당 세션 %s, 질문 %d개 (범주 1~4, 5번 adversarial 제외)\n", len(r.Conversations), strings.Join(r.Conversations, ", "), sessions, r.Questions)
	fmt.Fprintf(&b, "- 수집: 세션 %d개 → 주장 %d개, 승인 %d개, 서버 응답(status:reason) %s\n", r.Sessions, r.Claims, r.Approved, kvList(r.CaptureStatus))
	fmt.Fprintf(&b, "- CLI 호출: %s (CLI가 알린 답변·채점 비용 합계 $%.4f, 추출 비용은 집계 안 됨)\n", kvList(r.Calls), r.CostUSD)
	fmt.Fprintf(&b, "- 소요 시간: 수집 %.0f초, 질의 %.0f초, 전체 %.0f초\n", r.WallSeconds["ingest"], r.WallSeconds["qa"], r.WallSeconds["total"])
	if r.Failed > 0 {
		fmt.Fprintf(&b, "- **실패 %d건** (같은 --out으로 다시 실행하면 이어서 함)\n", r.Failed)
	}
	b.WriteString("\n## 정확도 (LLM 채점 0/1)\n\n| 시스템 | 전체 |")
	cats := []string{"single-hop", "multi-hop", "temporal", "open-domain"}
	for _, k := range cats {
		b.WriteString(" " + k + " |")
	}
	b.WriteString(" 평균 검색 수 | 평균 문맥 토큰(추정) |\n|---|---|---|---|---|---|---|---|\n")
	for _, name := range r.Systems {
		s := r.Summary[name]
		fmt.Fprintf(&b, "| %s | %s |", name, pct(s.catStat))
		for _, k := range cats {
			cs := catStat{}
			if v := s.ByCategory[k]; v != nil {
				cs = *v
			}
			b.WriteString(" " + pct(cs) + " |")
		}
		fmt.Fprintf(&b, " %.1f | %.0f |\n", s.AvgRetrieved, s.AvgContextTokens)
	}
	b.WriteString("\n문맥 토큰은 답변 모델에 넘긴 기억(또는 대화) 글자 수 ÷ 4 추정치다. 범주 이름은 공개 데이터의 번호를 따른다(1 multi-hop, 2 temporal, 3 open-domain, 4 single-hop).\n")
	b.WriteString("\n## 문항별\n\n| 대화 | # | 범주 | 시스템 | 점수 | 정답 | 답변 |\n|---|---|---|---|---|---|---|\n")
	for _, row := range r.Rows {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %d | %s | %s |\n", row.Conv, row.QIndex, row.CategoryName, row.System, row.Score, cell(row.Gold), cell(row.Answer))
	}
	return b.String()
}

func cell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "|", "\\|")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}
