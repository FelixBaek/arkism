package memory

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Callers must authorize external transfer and validate lesson provenance before
// calling a drafter. This interface has no evaluation dataset or answer field.
type SkillDraftLesson struct {
	TaskType           string `json:"task_type"`
	Lesson             string `json:"lesson"`
	VerificationMethod string `json:"verification_method"`
}
type SkillDraftInput struct {
	SkillID          string             `json:"skill_id"`
	Path             string             `json:"path"`
	BaselineContent  string             `json:"baseline_content"`
	BaselineAnalysis *SkillAnalysis     `json:"baseline_analysis"`
	Lessons          []SkillDraftLesson `json:"lessons"`
}
type SkillDraft struct {
	Content  string        `json:"content"`
	Analysis SkillAnalysis `json:"analysis"`
}
type SkillDrafter interface {
	Generator() Generator
	Draft(context.Context, SkillDraftInput) (SkillDraft, ProviderUsage, error)
}

type ChatSkillDrafter struct{ Config LLMConfig }

func (a *ChatSkillDrafter) Generator() Generator {
	return Generator{Model: a.Config.Model, PromptVersion: "skill-draft-v3", PipelineVersion: "arkism-skill-draft-v1"}
}

func invalidSkillDraftInput(in SkillDraftInput) bool {
	if !validRunLabel(in.SkillID, 256) || !strings.HasSuffix(in.Path, "SKILL.md") || strings.TrimSpace(in.Path) != in.Path || strings.IndexFunc(in.Path, unicode.IsControl) >= 0 || len(in.Path) > 4096 || len(in.Lessons) < improvementMinRuns || len(in.Lessons) > 16 || !safeTransfer(structuredText(in)) {
		return true
	}
	if in.BaselineAnalysis == nil {
		if in.BaselineContent != "" {
			return true
		}
	} else if !validAnalysis(in.BaselineAnalysis) || in.BaselineAnalysis.SkillID != in.SkillID || in.BaselineContent == "" || in.BaselineAnalysis.ContentHash != ContentHash(in.BaselineContent) {
		return true
	}
	for _, lesson := range in.Lessons {
		if strings.TrimSpace(lesson.TaskType) == "" || strings.TrimSpace(lesson.Lesson) == "" || strings.TrimSpace(lesson.VerificationMethod) == "" {
			return true
		}
	}
	return false
}

func (a *ChatSkillDrafter) Draft(ctx context.Context, in SkillDraftInput) (SkillDraft, ProviderUsage, error) {
	var zero SkillDraft
	if invalidSkillDraftInput(in) {
		return zero, ProviderUsage{}, ErrInvalid
	}
	g := a.Generator()
	if !validGenerator(&g) || a.Config.APIKey == "" || strings.HasPrefix(strings.ToLower(a.Config.Model), "jev") || validateEndpoint(a.Config.Endpoint) != nil {
		return zero, ProviderUsage{}, ErrUnavailable
	}
	// Exclude baseline evaluation statistics and catalog identity from model data.
	if in.BaselineAnalysis != nil {
		clone := *in.BaselineAnalysis
		clone.EvaluationStats = nil
		clone.SkillID, clone.Version, clone.ContentHash, clone.SourcePath, clone.SourceFile = "", "", "", "", ""
		in.BaselineAnalysis = &clone
	}
	payload := map[string]any{"model": a.Config.Model, "temperature": 0, "max_tokens": 8192, "response_format": map[string]string{"type": "json_object"}, "messages": []map[string]string{
		{"role": "system", "content": skillDraftSystemPrompt(in)},
		{"role": "user", "content": structuredText(in)},
	}}
	body, status, err := postJSON(ctx, providerClient(nil, 60*time.Second), a.Config.Endpoint, a.Config.APIKey, payload, defaultRequestMax, defaultResponseMax)
	if err != nil {
		return zero, ProviderUsage{}, err
	}
	if status != 200 {
		return zero, ProviderUsage{}, dependencyStatusError("skill draft", status)
	}
	var response struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil {
		return zero, ProviderUsage{}, ErrUnavailable
	}
	usage := ProviderUsage{Provider: "skill_llm", Model: a.Config.Model, Requests: 1, InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, TotalTokens: response.Usage.TotalTokens}
	if !validProviderUsage(usage) || usage.TotalTokens == 0 || usage.InputTokens > usage.TotalTokens || usage.OutputTokens > usage.TotalTokens-usage.InputTokens {
		return zero, ProviderUsage{}, ErrUnavailable
	}
	if len(response.Choices) != 1 || response.Choices[0].FinishReason != "stop" {
		return zero, usage, ErrUnavailable
	}
	raw := response.Choices[0].Message.Content
	out, err := decodeSkillDraft(in, raw)
	return out, usage, err
}

func normalizeSkillDraft(in SkillDraftInput, out *SkillDraft) error {
	if strings.TrimSpace(out.Content) == "" || !safeTransfer(structuredText(out)) {
		return ErrDenied
	}
	analysis := &out.Analysis
	analysis.SkillID, analysis.ContentHash = in.SkillID, ContentHash(out.Content)
	analysis.Version, analysis.SourcePath, analysis.SourceFile = analysis.ContentHash[:16], in.Path, "SKILL.md"
	analysis.EvaluationStats = map[string]float64{"runs": 0, "passed": 0, "failed": 0}
	if !validAnalysis(analysis) || !safeTransfer(structuredText(out)) || !draftCapabilitiesWithin(in.BaselineAnalysis, analysis) {
		return ErrDenied
	}
	return nil
}

// This bounds declared capabilities, not arbitrary natural-language behavior.
// Independent evaluation and human approval remain mandatory.
func draftCapabilitiesWithin(base, candidate *SkillAnalysis) bool {
	if base == nil {
		base = &SkillAnalysis{}
	}
	if candidate.Reads && !base.Reads || candidate.Writes && !base.Writes || candidate.ExternalTransfer && !base.ExternalTransfer {
		return false
	}
	for _, pair := range [][2][]string{
		{base.Tools, candidate.Tools}, {base.APIs, candidate.APIs},
		{base.Permissions, candidate.Permissions}, {base.SecretNames, candidate.SecretNames},
		{base.Dependencies, candidate.Dependencies}, {base.IrreversibleActions, candidate.IrreversibleActions},
	} {
		for _, value := range pair[1] {
			if !slices.Contains(pair[0], value) {
				return false
			}
		}
	}
	return true
}

// All schema keys must be present. Null is accepted only for slices/maps (as
// emitted by the schema); null booleans must not silently become false.
func completeDraftJSON(raw string) bool {
	return completeDraftResponseJSON(raw, "content")
}

func completeDraftResponseJSON(raw, bodyKey string) bool {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if !uniqueJSONValue(decoder) {
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	var root, schema map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &root) != nil || len(root) != 2 || root[bodyKey] == nil || root["analysis"] == nil || strings.TrimSpace(string(root[bodyKey])) == "null" {
		return false
	}
	if bodyKey == "edits" && !completeDraftEditsJSON(root[bodyKey]) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(root["analysis"], &fields) != nil {
		return false
	}
	_ = json.Unmarshal([]byte(structuredText(SkillAnalysis{})), &schema)
	if len(fields) != len(schema) {
		return false
	}
	for key, expected := range schema {
		value, ok := fields[key]
		if !ok || string(value) == "null" && string(expected) != "null" {
			return false
		}
	}
	return true
}

func completeDraftEditsJSON(raw json.RawMessage) bool {
	var edits []map[string]json.RawMessage
	if json.Unmarshal(raw, &edits) != nil || len(edits) == 0 || len(edits) > maxSkillDraftEdits {
		return false
	}
	for _, edit := range edits {
		if len(edit) != 3 {
			return false
		}
		for _, key := range []string{"old_text", "new_text", "lesson_index"} {
			if edit[key] == nil || strings.TrimSpace(string(edit[key])) == "null" {
				return false
			}
		}
	}
	return true
}

func uniqueJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !uniqueJSONValue(decoder) {
				return false
			}
		}
	case '[':
		for decoder.More() {
			if !uniqueJSONValue(decoder) {
				return false
			}
		}
	default:
		return false
	}
	_, err = decoder.Token()
	return err == nil
}
