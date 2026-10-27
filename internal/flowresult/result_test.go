package flowresult

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTextPreservesLongEndResultSourcesAndLimitations(t *testing.T) {
	report := Research{Answer: strings.Repeat("근거를 확인한 내용. ", 900), Sources: []Source{{Title: "원문", URL: "https://example.com/paper"}}, Limitations: []string{"검증하지 못한 날짜"}}
	output, _ := json.Marshal(report)
	wrapped, _ := json.Marshal(map[string]any{"output": string(output)})
	raw, _ := json.Marshal(map[string]any{"status": "succeeded", "summary": "shortened", "definition": map[string]any{"graph": map[string]any{"nodes": []any{map[string]any{"id": "end", "type": "end"}}}}, "nodes": map[string]any{"end": map[string]any{"status": "done", "output": string(wrapped)}}})
	text := Text(raw)
	if !strings.Contains(text, report.Answer) || !strings.Contains(text, report.Sources[0].URL) || !strings.Contains(text, report.Limitations[0]) || strings.Contains(text, "shortened") {
		t.Fatal("full end result was truncated or replaced by summary")
	}
}

func TestTextRequiresTerminalRunAndUsesSummaryOnFailure(t *testing.T) {
	for _, status := range []string{"running", "waiting"} {
		raw, _ := json.Marshal(map[string]any{"status": status, "summary": "not final"})
		if Text(raw) != "" {
			t.Fatalf("%s returned a final answer", status)
		}
	}
	for _, status := range []string{"failed", "stopped"} {
		raw, _ := json.Marshal(map[string]any{"status": status, "summary": "search unavailable"})
		if Text(raw) != "search unavailable" {
			t.Fatalf("%s lost failure explanation", status)
		}
	}
	if Text(json.RawMessage(`{"status":`)) != "" {
		t.Fatal("malformed state produced result")
	}
}

func TestResearchValidationRejectsEmptyOrUnsafeReferences(t *testing.T) {
	for _, raw := range []string{
		`{"answer":"not researched","sources":[]}`,
		`{"answer":"x","sources":[{"title":"local","url":"file:///etc/passwd"}]}`,
		`{"answer":"x","sources":[{"title":"secret","url":"https://user:password@example.com/"}]}`,
		`{"answer":"x","sources":[{"title":"","url":"https://example.com/"}]}`,
		`{"answer":" ","sources":[{"title":"source","url":"https://example.com/"}]}`,
		"not JSON",
	} {
		if _, err := ValidateResearch(raw); err == nil {
			t.Fatalf("accepted invalid research %s", raw)
		}
	}
	if _, err := ValidateResearch(`{"answer":"본문","sources":[{"title":"원문","url":"https://example.com/source"}],"limitations":["미확인"]}`); err != nil {
		t.Fatal(err)
	}
}

func syntheticStructuredReport() string {
	return `{"title":"합성 비교 보고서","type":"기술 비교","confidence":"중간","summary":["합성 결론 [7]"],"sections":[{"heading":"비교 근거","body":"확인한 합성 본문 [7]"}],"table":"| 후보 | 결과 |\n| --- | --- |\n| A | 보류 |","gaps":["미확인 비용"],"sources":[{"n":7,"title":"합성 원문","url":"https://example.com/reference","kind":"공식","date":"2026-01-02"}],"council":{"agree":["공통 관찰"],"disagree":[{"point":"합성 쟁점","a":"가능","b":"보류","judge":"근거 부족","extra":"추가 조건"}],"verdict":"추가 확인","reviewer_note":"기타 검토 의견"}}`
}

func resultRun(t *testing.T, status string, nodes, edges []map[string]string, states map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"status": status, "summary": "실행 상태 설명", "definition": map[string]any{"graph": map[string]any{"nodes": nodes, "edges": edges}}, "nodes": states})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestStructuredReportPreservesAllFieldsFromTerminalLeaf(t *testing.T) {
	report := syntheticStructuredReport()
	wrapped, _ := json.Marshal(map[string]any{"output": "```json\n" + report + "\n```", "model": "synthetic"})
	raw := resultRun(t, "succeeded", []map[string]string{{"id": "start", "type": "start"}, {"id": "early-end", "type": "end"}, {"id": "final-leaf", "type": "ai"}}, []map[string]string{{"from": "start", "to": "final-leaf"}}, map[string]any{
		"start": map[string]any{"status": "done", "output": `"intermediate"`}, "early-end": map[string]any{"status": "skipped"}, "final-leaf": map[string]any{"status": "done", "output": string(wrapped)},
	})
	text := Text(raw)
	for _, want := range []string{"# 합성 비교 보고서", "기술 비교", "중간", "합성 결론 [7]", "## 비교 근거", "확인한 합성 본문 [7]", "| A | 보류 |", "미확인 비용", "[7] [합성 원문](<https://example.com/reference>)", "공식", "2026-01-02", "공통 관찰", "합성 쟁점", "의견 A: 가능", "의견 B: 보류", "원문 기준 판정: 근거 부족", "추가 조건", "추가 확인", "기타 검토 의견"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report lost %q: %s", want, text)
		}
	}
	if strings.Contains(text, "intermediate") || strings.Contains(text, "실행 상태 설명") {
		t.Fatal("nonterminal or shortened result replaced leaf output")
	}
	if _, err := ValidateResearch(string(wrapped)); err != nil {
		t.Fatal("structured report rejected", err)
	}
}

func TestReportParserOnlyAcceptsWholeBoundedJSON(t *testing.T) {
	report := syntheticStructuredReport()
	wrapped := report
	for i := 0; i < 3; i++ {
		b, _ := json.Marshal(map[string]string{"output": wrapped})
		wrapped = string(b)
	}
	if _, err := ValidateResearch(wrapped); err != nil {
		t.Fatal("three supported wrappers rejected", err)
	}
	tooDeep, _ := json.Marshal(map[string]string{"output": wrapped})
	for _, invalid := range []string{string(tooDeep), "before " + report, report + " trailing", "```json\n" + report + "\n```\nafter", "```python\n" + report + "\n```", report + report, `{"summary":["formatted only"],"sources":[]}`, `{"title":"title only","sources":[{"title":"source","url":"https://example.com"}]}`} {
		if _, err := ValidateResearch(invalid); err == nil {
			t.Fatal("parser or validation accepted malformed/unsubstantiated report")
		}
	}
}

func TestFailedAndStoppedRunsDoNotPublishCompletedSiblingReport(t *testing.T) {
	for _, status := range []string{"failed", "stopped"} {
		raw := resultRun(t, status, []map[string]string{{"id": "done", "type": "end"}, {"id": "leaf", "type": "ai"}}, nil, map[string]any{
			"done": map[string]any{"status": "done", "output": syntheticStructuredReport()}, "leaf": map[string]any{"status": "done", "output": syntheticStructuredReport()},
		})
		if got := Text(raw); got != "실행 상태 설명" {
			t.Fatalf("%s presented a successful report: %s", status, got)
		}
	}
}

func TestTerminalLeafSelectionIsPinnedAndDeterministic(t *testing.T) {
	nodes := []map[string]string{{"id": "first", "type": "ai"}, {"id": "second", "type": "ai"}}
	raw := resultRun(t, "succeeded", nodes, nil, map[string]any{"second": map[string]any{"status": "done", "output": `"두 번째"`}, "first": map[string]any{"status": "done", "output": `"첫 번째"`}, "injected": map[string]any{"status": "done", "output": `"not in definition"`}})
	if Text(raw) != "첫 번째\n\n두 번째" {
		t.Fatal("terminal leaves did not follow pinned definition order")
	}
	withEnd := append(nodes, map[string]string{"id": "end", "type": "end"})
	raw = resultRun(t, "succeeded", withEnd, nil, map[string]any{"first": map[string]any{"status": "done", "output": `"earlier"`}, "end": map[string]any{"status": "done", "output": `"최종 합성"`}})
	if Text(raw) != "최종 합성" {
		t.Fatal("completed end node did not take precedence")
	}
}

func TestResearchStorageLimitCountsEscapingAndFinalEnvelope(t *testing.T) {
	report := Research{Answer: strings.Repeat("\"", 9000), Sources: []Source{{Title: "합성 출처", URL: "https://example.com"}}}
	raw, _ := json.Marshal(report)
	if utf8.RuneCount(raw) >= 20000 {
		t.Fatal("test must cross limit only after envelope escaping")
	}
	if _, err := ValidateResearch(string(raw)); err == nil {
		t.Fatal("escaped oversized research would be silently truncated")
	}
	if !FitsNodeOutput(map[string]any{"output": strings.Repeat("가", 19000)}) {
		t.Fatal("valid Unicode payload rejected")
	}
	if FitsNodeOutput(map[string]any{"output": strings.Repeat("가", 19000), "rules": strings.Repeat("x", 2000)}) {
		t.Fatal("final envelope metadata escaped size check")
	}
	if FitsNodeOutput(make(chan int)) {
		t.Fatal("unserializable value accepted")
	}
}

func TestLegacyTruncatedReportIsExplicitlyIncomplete(t *testing.T) {
	raw := resultRun(t, "succeeded", []map[string]string{{"id": "leaf", "type": "ai"}}, nil, map[string]any{"leaf": map[string]any{"status": "done", "output": `{"truncated":true,"text":"partial legacy content"}`}})
	text := Text(raw)
	if !strings.Contains(text, "전체 보고서를 확인할 수 없습니다") || !strings.Contains(text, "partial legacy content") {
		t.Fatal("legacy truncation was silently presented as a complete report")
	}
}
