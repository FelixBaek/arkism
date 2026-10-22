package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
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

func draftSchema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			properties[name] = draftSchema(f.Type)
			required = append(required, name)
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": draftSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int:
		return map[string]any{"type": "integer"}
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
	request := func(method, path string, payload []byte) ([]byte, error) {
		req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(payload))
		if e != nil {
			return nil, e
		}
		req.Header.Set("x-api-key", a.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-workspace-id", a.WorkspaceID)
		req.Header.Set("Content-Type", "application/json")
		sent = true
		response, e := client.Do(req)
		if e != nil {
			return nil, ErrUnavailable
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(response.Body, defaultResponseMax+1))
		if e != nil || len(raw) > defaultResponseMax || response.StatusCode != 200 {
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

// parseDraftResponse returns the billed usage, the stop reason and the joined text blocks.
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
	if json.Unmarshal(raw, &response) != nil || response.Model != model || response.Usage.Input == nil || response.Usage.Output == nil || *response.Usage.Input < 0 || *response.Usage.Output < 0 || response.Usage.CacheRead != 0 || response.Usage.CacheWrite != 0 {
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
