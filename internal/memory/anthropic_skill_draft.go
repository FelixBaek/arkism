package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Optional paid role adapter. Only GenerateSkill can provide its authorized
// scope/event identity; direct Draft calls fail closed.
type AnthropicSkillDrafter struct {
	Service                       *Service
	APIKey, WorkspaceID, Endpoint string
	BudgetScope                   Scope
	Client                        *http.Client
}

func (a *AnthropicSkillDrafter) Generator() Generator {
	return Generator{Model: "claude-sonnet-5-5", PromptVersion: "anthropic-skill-draft-v2", PipelineVersion: "arkism-skill-draft-v1"}
}
func (a *AnthropicSkillDrafter) Draft(context.Context, SkillDraftInput) (SkillDraft, ProviderUsage, error) {
	return SkillDraft{}, ProviderUsage{}, ErrDenied
}

// draftSchema is the strict structured-output schema: every field required.
func draftSchema(t reflect.Type) map[string]any { return jsonSchemaOf(t, true) }

// jsonSchemaOf describes a Go type as JSON Schema. strict requires every field
// (structured model output); otherwise omitempty fields are optional.
func jsonSchemaOf(t reflect.Type, strict bool) map[string]any {
	return jsonSchemaWalk(t, strict, map[reflect.Type]bool{})
}

// jsonSchemaWalk is jsonSchemaOf with the structs being described; a recursive type
// (a tag test's and/or) is described once and its inner repeats as a plain object.
func jsonSchemaWalk(t reflect.Type, strict bool, open map[reflect.Type]bool) map[string]any {
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return jsonSchemaWalk(t.Elem(), strict, open)
	case reflect.Struct:
		if open[t] {
			return map[string]any{"type": "object"}
		}
		open[t] = true
		defer delete(open, t)
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "" || tag[0] == "-" {
				continue
			}
			properties[tag[0]] = jsonSchemaWalk(f.Type, strict, open)
			if strict || !slices.Contains(tag[1:], "omitempty") {
				required = append(required, tag[0])
			}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": jsonSchemaWalk(t.Elem(), strict, open)}
	case reflect.Map:
		if !strict {
			return map[string]any{"type": "object", "additionalProperties": jsonSchemaWalk(t.Elem(), strict, open)}
		}
		return map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Int32:
		return map[string]any{"type": "integer"}
	case reflect.Float64, reflect.Float32:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{"type": "string"}
	}
}
func (a *AnthropicSkillDrafter) DraftAuthorized(ctx context.Context, token string, scope Scope, operation string, lessonRefs []Ref, in SkillDraftInput) (SkillDraft, ProviderUsage, error) {
	var zero SkillDraft
	usage := ProviderUsage{}
	if a.Service == nil || a.APIKey == "" || a.WorkspaceID == "" || !safeTransfer(structuredText(in)) {
		return zero, usage, ErrUnavailable
	}
	if in.BaselineAnalysis != nil {
		clone := *in.BaselineAnalysis
		clone.EvaluationStats = nil
		clone.SkillID, clone.Version, clone.ContentHash, clone.SourcePath, clone.SourceFile = "", "", "", "", ""
		in.BaselineAnalysis = &clone
	}
	if err := a.authorizeLessonTransfer(ctx, token, scope, lessonRefs); err != nil {
		return zero, usage, err
	}
	data := structuredText(map[string]any{"authority": "data_only_untrusted", "data": in})
	if _, e := a.Service.authorizeArchiveModelContent(ctx, token, scope, "", "private", data); e != nil {
		return zero, usage, e
	}
	endpoint, e := a.draftEndpoint()
	if e != nil {
		return zero, usage, e
	}
	client := providerClient(a.Client, 120*time.Second)
	sent := false
	header := http.Header{"X-Api-Key": {a.APIKey}, "Anthropic-Version": {"2023-06-01"}, "Anthropic-Workspace-Id": {a.WorkspaceID}}
	request := func(method, path string, payload []byte) ([]byte, error) {
		sent = true
		raw, status, e := sendBody(ctx, client, method, strings.TrimRight(endpoint, "/")+path, header, payload, defaultResponseMax)
		if e != nil || status != http.StatusOK {
			return nil, ErrUnavailable
		}
		return raw, nil
	}
	models, e := request("GET", "/models", nil)
	if e != nil {
		return zero, usage, e
	}
	if !draftModelListed(models, a.Generator().Model) {
		return zero, usage, ErrUnavailable
	}
	payload := map[string]any{"model": a.Generator().Model, "max_tokens": 16384, "thinking": map[string]string{"type": "adaptive"}, "output_config": map[string]any{"effort": "high", "format": map[string]any{"type": "json_schema", "schema": draftSchema(reflect.TypeOf(skillDraftShape(in)))}}, "system": skillDraftSystemPrompt(in), "messages": []map[string]string{{"role": "user", "content": data}}}
	body, e := json.Marshal(payload)
	if e != nil {
		return zero, usage, ErrInvalid
	}
	bound := int64(len(body) + 1024)
	if bound > 100000 {
		return zero, usage, ErrInvalid
	}
	reserved, e := a.Service.ReserveModelBudget(ctx, token, ModelBudgetReserveInput{Scope: a.BudgetScope, OperationID: "skill-draft-" + ContentHash(operation+"\n"+data), Model: a.Generator().Model, Role: "skill_drafter", MaxInputTokens: bound, MaxOutputTokens: 16384})
	if e != nil {
		return zero, usage, e
	}
	if !reserved.DispatchAllowed {
		return zero, usage, ErrConflict
	}
	// Every failed dispatch is settled so the reservation never stays pending;
	// "unknown" keeps the allowance held because the provider may have billed it.
	release := func(e error) (SkillDraft, ProviderUsage, error) {
		status := "unknown"
		if !sent {
			status = "not_sent"
		}
		if _, se := a.Service.SettleModelBudget(context.WithoutCancel(ctx), token, ModelBudgetSettleInput{Scope: a.BudgetScope, ReservationID: reserved.ReservationID, Status: status}); se != nil {
			return zero, usage, errors.Join(e, se)
		}
		return zero, usage, e
	}
	sent = false
	raw, e := request("POST", "/messages", body)
	if e != nil {
		return release(e)
	}
	got, stop, text, ok := parseDraftResponse(raw, a.Generator().Model)
	if !ok {
		return release(ErrUnavailable)
	}
	usage = got
	if _, e = a.Service.SettleModelBudget(ctx, token, ModelBudgetSettleInput{Scope: a.BudgetScope, ReservationID: reserved.ReservationID, Status: "completed", InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}); e != nil {
		return zero, usage, e
	}
	if stop != "end_turn" {
		return zero, usage, ErrUnavailable
	}
	out, e := decodeSkillDraft(in, text)
	return out, usage, e
}

func (a *AnthropicSkillDrafter) authorizeLessonTransfer(ctx context.Context, token string, scope Scope, lessonRefs []Ref) error {
	for _, ref := range lessonRefs {
		row, err := a.Service.Get(ctx, token, ref.Collection, scope, ref.ID)
		if err != nil {
			return err
		}
		if row.Revision != ref.Revision {
			return ErrConflict
		}
		if _, err = a.Service.authorizeTransfer(ctx, token, "transfer:model_input", scope, row.ID, row.Sensitivity, "external:anthropic", recordContent(row), row.Sources); err != nil {
			return err
		}
	}
	return nil
}

func (a *AnthropicSkillDrafter) draftEndpoint() (string, error) {
	endpoint := a.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com/v1"
	}
	if validateEndpoint(endpoint) != nil {
		return "", ErrUnavailable
	}
	return endpoint, nil
}

func draftModelListed(models []byte, model string) bool {
	var discovered struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(models, &discovered) != nil {
		return false
	}
	found := false
	for _, m := range discovered.Data {
		found = found || m.ID == model
	}
	return found
}

// parseDraftResponse returns the billed usage, the stop reason and the joined text blocks
// of a messages-protocol answer; model "" accepts any answering model.
func parseDraftResponse(raw []byte, model string) (ProviderUsage, string, string, bool) {
	var response struct {
		Model   string `json:"model"`
		Stop    string `json:"stop_reason"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			Input      *int64 `json:"input_tokens"`
			Output     *int64 `json:"output_tokens"`
			CacheRead  int64  `json:"cache_read_input_tokens"`
			CacheWrite int64  `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &response) != nil || (model != "" && response.Model != model) || response.Usage.Input == nil || response.Usage.Output == nil || *response.Usage.Input < 0 || *response.Usage.Output < 0 || response.Usage.CacheRead != 0 || response.Usage.CacheWrite != 0 {
		return ProviderUsage{}, "", "", false
	}
	usage := ProviderUsage{Provider: "skill_llm", Model: model, Requests: 1, InputTokens: *response.Usage.Input, OutputTokens: *response.Usage.Output, TotalTokens: *response.Usage.Input + *response.Usage.Output}
	text := ""
	for _, block := range response.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return usage, response.Stop, text, true
}
