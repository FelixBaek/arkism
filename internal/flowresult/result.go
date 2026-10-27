// Package flowresult renders immutable workflow results identically for MCP
// and messengers. It never fetches a current definition or follows result URLs.
package flowresult

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const storedNodeLimit = 20000

// Research accepts the compact report and the structured research report.
// Formatting these fields does not establish that their claims were verified.
type Research struct {
	Answer      string          `json:"answer,omitempty"`
	Title       string          `json:"title,omitempty"`
	Type        string          `json:"type,omitempty"`
	Confidence  json.RawMessage `json:"confidence,omitempty"`
	Summary     []string        `json:"summary,omitempty"`
	Sections    []Section       `json:"sections,omitempty"`
	Table       json.RawMessage `json:"table,omitempty"`
	Sources     []Source        `json:"sources"`
	Limitations []string        `json:"limitations,omitempty"`
	Gaps        []string        `json:"gaps,omitempty"`
	Council     json.RawMessage `json:"council,omitempty"`
}

type Section struct {
	Heading string `json:"heading"`
	Body    string `json:"body"`
}
type Source struct {
	N     int    `json:"n,omitempty"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Kind  string `json:"kind,omitempty"`
	Date  string `json:"date,omitempty"`
}

// FitsNodeOutput must also be checked against the final runner envelope: its
// escaped JSON and metadata count towards the engine's persisted-node limit.
func FitsNodeOutput(value any) bool {
	raw, err := json.Marshal(value)
	return err == nil && utf8.RuneCount(raw) <= storedNodeLimit
}

// ValidateResearch checks shape, reference URLs and storage size. It does not
// check source contents or whether a source supports a claim. Formatting alone
// never calls this validation, and a report without references is rejected.
func ValidateResearch(raw string) (Research, error) {
	var report Research
	if !FitsNodeOutput(map[string]any{"output": raw}) {
		return report, fmt.Errorf("research result exceeds persisted node limit")
	}
	decoded, err := unwrapJSON(raw)
	if err != nil || json.Unmarshal(decoded, &report) != nil || !report.hasBody() || len(report.Sources) == 0 || len(report.Sources) > 100 {
		return report, fmt.Errorf("research result needs a report body and source references")
	}
	numbers := map[int]bool{}
	for _, source := range report.Sources {
		u, e := url.Parse(source.URL)
		if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || strings.TrimSpace(source.Title) == "" || source.N < 0 || source.N > 0 && numbers[source.N] {
			return report, fmt.Errorf("invalid research source")
		}
		if source.N > 0 {
			numbers[source.N] = true
		}
	}
	return report, nil
}

func (r Research) hasBody() bool {
	if strings.TrimSpace(r.Answer) != "" {
		return true
	}
	for _, text := range r.Summary {
		if strings.TrimSpace(text) != "" {
			return true
		}
	}
	for _, section := range r.Sections {
		if strings.TrimSpace(section.Body) != "" {
			return true
		}
	}
	return false
}

// Text uses only the pinned graph and persisted execution output. Failed or
// stopped runs retain their failure summary, never a completed sibling report.
func Text(raw json.RawMessage) string {
	var run struct {
		Status     string `json:"status"`
		Summary    string `json:"summary"`
		Definition struct {
			Graph struct {
				Nodes []struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"nodes"`
				Edges []struct {
					From string `json:"from"`
				} `json:"edges"`
			} `json:"graph"`
		} `json:"definition"`
		Nodes map[string]struct {
			Status string `json:"status"`
			Output string `json:"output"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &run) != nil {
		return ""
	}
	if run.Status == "failed" || run.Status == "stopped" {
		return run.Summary
	}
	if run.Status != "succeeded" {
		return ""
	}
	for _, node := range run.Definition.Graph.Nodes {
		state := run.Nodes[node.ID]
		if node.Type == "end" && state.Status == "done" {
			if text := extract(state.Output); text != "" {
				return text
			}
		}
	}
	outgoing := map[string]bool{}
	for _, edge := range run.Definition.Graph.Edges {
		outgoing[edge.From] = true
	}
	// Definition order is stable, unlike iteration over the runtime node map.
	var results []string
	for _, node := range run.Definition.Graph.Nodes {
		state := run.Nodes[node.ID]
		if !outgoing[node.ID] && state.Status == "done" {
			if text := extract(state.Output); text != "" {
				results = append(results, text)
			}
		}
	}
	if len(results) > 0 {
		return strings.Join(results, "\n\n")
	}
	return run.Summary
}

// ParseResearchJSON shares the report decoder with workflow verdicts. It checks
// JSON framing only, not report validity, source contents or factual truth.
func ParseResearchJSON(raw string) (json.RawMessage, error) { return unwrapJSON(raw) }

// unwrapJSON accepts a whole JSON document, a whole JSON code fence, and at
// most three nested string/output wrappers. It never extracts a JSON fragment
// from prose, accepts trailing commentary, or tries to repair malformed JSON.
func unwrapJSON(raw string) (json.RawMessage, error) {
	for layer := 0; layer <= 3; layer++ {
		text := strings.TrimSpace(raw)
		if strings.HasPrefix(text, "```") {
			line, rest, ok := strings.Cut(text, "\n")
			if !ok || (strings.TrimSpace(line) != "```json" && strings.TrimSpace(line) != "```") || !strings.HasSuffix(rest, "\n```") {
				return nil, fmt.Errorf("invalid JSON fence")
			}
			text = strings.TrimSpace(strings.TrimSuffix(rest, "\n```"))
		}
		var value any
		if json.Unmarshal([]byte(text), &value) != nil {
			return nil, fmt.Errorf("invalid JSON document")
		}
		switch v := value.(type) {
		case string:
			raw = v
		case map[string]any:
			if _, ok := v["answer"]; ok {
				return json.RawMessage(text), nil
			}
			if _, ok := v["sections"]; ok {
				return json.RawMessage(text), nil
			}
			if _, ok := v["sources"]; ok {
				return json.RawMessage(text), nil
			}
			if output, ok := v["output"]; ok {
				if s, ok := output.(string); ok {
					raw = s
				} else {
					b, _ := json.Marshal(output)
					raw = string(b)
				}
				continue
			}
			return json.RawMessage(text), nil
		default:
			return nil, fmt.Errorf("report must be a JSON object")
		}
	}
	return nil, fmt.Errorf("nested report limit exceeded")
}

func extract(raw string) string {
	if raw == "" {
		return ""
	}
	decoded, err := unwrapJSON(raw)
	if err == nil {
		var truncated struct {
			Truncated bool   `json:"truncated"`
			Text      string `json:"text"`
		}
		if json.Unmarshal(decoded, &truncated) == nil && truncated.Truncated {
			return "저장 한도를 넘어 결과가 잘렸습니다. 전체 보고서를 확인할 수 없습니다.\n\n" + truncated.Text
		}
		var report Research
		if json.Unmarshal(decoded, &report) == nil && report.hasBody() {
			return report.markdown()
		}
	}
	// Preserve ordinary workflow text and non-report JSON without claiming it
	// passed research validation. Plain-string output remains readable.
	var plain string
	if json.Unmarshal([]byte(raw), &plain) == nil {
		return plain
	}
	var wrapper struct {
		Output  string `json:"output"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal([]byte(raw), &wrapper) == nil {
		if wrapper.Output != "" {
			return wrapper.Output
		}
		if wrapper.Summary != "" {
			return wrapper.Summary
		}
	}
	return raw
}

func (r Research) markdown() string {
	var blocks []string
	if r.Title != "" {
		blocks = append(blocks, "# "+r.Title)
	}
	if r.Type != "" {
		blocks = append(blocks, "유형: "+r.Type)
	}
	if text := rawText(r.Confidence); text != "" {
		blocks = append(blocks, "보고서에 기재된 신뢰도: "+text)
	}
	if r.Answer != "" {
		blocks = append(blocks, r.Answer)
	}
	if text := bulletList(r.Summary); text != "" {
		blocks = append(blocks, "요약\n"+text)
	}
	for _, section := range r.Sections {
		text := section.Body
		if section.Heading != "" {
			text = "## " + section.Heading + "\n" + text
		}
		if text != "" {
			blocks = append(blocks, text)
		}
	}
	if text := rawText(r.Table); text != "" {
		blocks = append(blocks, text)
	}
	if text := sourcesMarkdown(r.Sources); text != "" {
		blocks = append(blocks, text)
	}
	if text := bulletList(append(append([]string{}, r.Limitations...), r.Gaps...)); text != "" {
		blocks = append(blocks, "확인하지 못한 점\n"+text)
	}
	if text := councilText(r.Council); text != "" {
		blocks = append(blocks, "검토 의견\n"+text)
	}
	return strings.Join(blocks, "\n\n")
}

func sourcesMarkdown(sources []Source) string {
	if len(sources) == 0 {
		return ""
	}
	lines := []string{"출처"}
	for i, source := range sources {
		n := source.N
		if n == 0 {
			n = i + 1
		}
		title := strings.ReplaceAll(strings.ReplaceAll(source.Title, "[", "\\["), "]", "\\]")
		line := "- [" + strconv.Itoa(n) + "] " + title
		if u, err := url.Parse(source.URL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil {
			line = "- [" + strconv.Itoa(n) + "] [" + title + "](<" + strings.ReplaceAll(source.URL, ">", "%3E") + ">)"
		} else if source.URL != "" {
			line += ": " + source.URL
		}
		if source.Kind != "" {
			line += " · " + source.Kind
		}
		if source.Date != "" {
			line += " · " + source.Date
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func bulletList(items []string) string {
	var lines []string
	for _, item := range items {
		if item != "" {
			lines = append(lines, "- "+item)
		}
	}
	return strings.Join(lines, "\n")
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(value, "", "  ")
	return string(b)
}

func councilText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return rawText(raw)
	}
	labels := map[string]string{"agree": "일치한 의견", "disagree": "갈린 의견", "verdict": "최종 판정"}
	keys := []string{"agree", "disagree", "verdict"}
	var rest []string
	for key := range fields {
		if _, known := labels[key]; !known {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	var blocks []string
	for _, key := range keys {
		value, ok := fields[key]
		if !ok {
			continue
		}
		label := labels[key]
		if label == "" {
			label = key
		}
		var items []string
		text := rawText(value)
		if json.Unmarshal(value, &items) == nil {
			text = bulletList(items)
		}
		if key == "disagree" {
			text = disagreements(value)
		}
		if text != "" {
			blocks = append(blocks, label+"\n"+text)
		}
	}
	return strings.Join(blocks, "\n\n")
}

func disagreements(raw json.RawMessage) string {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return rawText(raw)
	}
	labels := map[string]string{"point": "쟁점", "a": "의견 A", "b": "의견 B", "judge": "원문 기준 판정"}
	var blocks []string
	for _, item := range items {
		keys := []string{"point", "a", "b", "judge"}
		var rest []string
		for key := range item {
			if _, known := labels[key]; !known {
				rest = append(rest, key)
			}
		}
		sort.Strings(rest)
		keys = append(keys, rest...)
		var lines []string
		for _, key := range keys {
			if value, ok := item[key]; ok {
				label := labels[key]
				if label == "" {
					label = key
				}
				lines = append(lines, "- "+label+": "+rawText(value))
			}
		}
		if len(lines) > 0 {
			blocks = append(blocks, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(blocks, "\n\n")
}
