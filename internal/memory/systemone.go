package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// configured judges, with no fallback generation.
const systemOneRequestMax = 8 << 20
const systemOneResponseMax = 16 << 20

var systemOneID = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)

type SystemOneInput struct {
	Scope   Scope           `json:"scope"`
	Request json.RawMessage `json:"request"`
}

type SystemOneModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SystemOneModelList struct {
	Items     []SystemOneModel `json:"items"`
	Authority string           `json:"authority"`
}

type SystemOneResult struct {
	Status    int             `json:"status"`
	Result    json.RawMessage `json:"result"`
	Authority string          `json:"authority"`
}

func systemOneIdentity(j *JevJudge) (string, string) {
	return j.provider(), strings.TrimPrefix(j.model(), j.provider()+"/")
}

func (s *Service) systemOneJudges() []*JevJudge {
	judges := []*JevJudge{}
	seen := map[string]bool{}
	visited := map[*FallbackJudge]bool{}
	var add func(SkillJudge)
	add = func(judge SkillJudge) {
		switch j := judge.(type) {
		case *JevJudge:
			if j == nil || strings.TrimSpace(j.APIKey) == "" {
				return
			}
			provider, model := systemOneIdentity(j)
			id := provider + "/" + model
			if provider == "group" || !validRoutingModelID(id) || !safeTransfer(id) || strings.Contains(id, j.APIKey) || seen[id] {
				return
			}
			seen[id] = true
			judges = append(judges, j)
		case *FallbackJudge:
			if j != nil && !visited[j] {
				visited[j] = true
				add(j.Primary)
				add(j.Secondary)
			}
		}
	}
	if j, ok := s.ModelPicker.(*JevJudge); ok {
		add(j)
	}
	add(s.Router)
	return judges
}

func (s *Service) SystemOneModels(ctx context.Context, token string, scope Scope) (SystemOneModelList, error) {
	if _, err := s.authorizeAt(ctx, token, "route:models", scope, routingID(scope), "", "internal"); err != nil {
		return SystemOneModelList{}, err
	}
	out := SystemOneModelList{Items: []SystemOneModel{}, Authority: AuthorityDataOnly}
	for _, j := range s.systemOneJudges() {
		provider, model := systemOneIdentity(j)
		out.Items = append(out.Items, SystemOneModel{ID: provider + "/" + model, Name: model})
	}
	return out, nil
}

// Reuse the duplicate-key checker; decode numbers without rounding integer usage.
func systemOneJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !uniqueJSONValue(d) {
		return nil, ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, ErrInvalid
	}
	return value, nil
}

func systemOneFields(value any, required, optional []string) (map[string]any, bool) {
	obj, ok := value.(map[string]any)
	if !ok || obj == nil {
		return nil, false
	}
	for _, key := range required {
		if obj[key] == nil {
			return nil, false
		}
	}
	for key := range obj {
		if !slices.Contains(required, key) && !slices.Contains(optional, key) {
			return nil, false
		}
	}
	return obj, true
}

func systemOneStructured(value any) bool {
	switch value.(type) {
	case string, map[string]any, []any:
		return true
	}
	return false
}

func systemOneRequest(raw []byte) (map[string]any, map[string]any, error) {
	if len(raw) > systemOneRequestMax {
		return nil, nil, ErrInvalid
	}
	value, err := systemOneJSON(raw)
	root, ok := systemOneFields(value, []string{"model", "state", "questions"}, nil)
	if err != nil || !ok || !systemOneStructured(root["state"]) {
		return nil, nil, ErrInvalid
	}
	model, ok := root["model"].(string)
	if !ok || !validRoutingModelID(model) {
		return nil, nil, ErrInvalid
	}
	questions, ok := root["questions"].(map[string]any)
	if !ok || len(questions) < 1 || len(questions) > 64 {
		return nil, nil, ErrInvalid
	}
	for _, value := range questions {
		if !systemOneQuestion(value) {
			return nil, nil, ErrInvalid
		}
	}
	return root, questions, nil
}

func systemOneQuestion(value any) bool {
	q, ok := systemOneFields(value, []string{"type"}, []string{"instructions", "criteria"})
	if !ok || q["instructions"] != nil && !systemOneStructured(q["instructions"]) {
		return false
	}
	switch q["type"] {
	case "choice":
		criteria, ok := q["criteria"].(map[string]any)
		if !ok || len(criteria) < 1 || len(criteria) > 255 {
			return false
		}
		return systemOneDescriptions(criteria)
	case "score":
		criteria, ok := q["criteria"].([]any)
		if !ok || len(criteria) < 2 || len(criteria) > 10 {
			return false
		}
		for _, description := range criteria {
			if !systemOneStructured(description) {
				return false
			}
		}
	case "noul":
		if criteria, exists := q["criteria"]; exists && criteria != nil {
			obj, ok := systemOneFields(criteria, nil, []string{"true", "false"})
			return ok && systemOneDescriptions(obj)
		}
	default:
		return false
	}
	return true
}

func systemOneDescriptions(obj map[string]any) bool {
	for _, description := range obj {
		if description != nil && !systemOneStructured(description) {
			return false
		}
	}
	return true
}

func systemOneNumber(value any) (float64, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	x, err := n.Float64()
	return x, err == nil && !math.IsNaN(x) && !math.IsInf(x, 0)
}

func systemOneTokens(value any) (int64, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	x, err := n.Int64()
	return x, err == nil && x >= 0
}

func systemOneHasKey(value any, key string) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, key)
	case map[string]any:
		for name, item := range v {
			if strings.Contains(name, key) || systemOneHasKey(item, key) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if systemOneHasKey(item, key) {
				return true
			}
		}
	}
	return false
}

func systemOneResponse(raw []byte, j *JevJudge, questions map[string]any) (json.RawMessage, ProviderUsage, error) {
	value, err := systemOneJSON(raw)
	if err != nil || strings.Contains(string(raw), j.APIKey) || containsSecret(structuredText(value)) || systemOneHasKey(value, j.APIKey) {
		return nil, ProviderUsage{}, ErrUnavailable
	}
	if j.Envelope {
		var ok bool
		if value, ok = systemOneEnvelope(value); !ok {
			return nil, ProviderUsage{}, ErrUnavailable
		}
	}
	root, ok := systemOneFields(value, []string{"model", "answers", "usage"}, nil)
	if !ok {
		return nil, ProviderUsage{}, ErrUnavailable
	}
	model, ok := root["model"].(string)
	if !ok || !providerModelRe.MatchString(model) || !safeTransfer(model) {
		return nil, ProviderUsage{}, ErrUnavailable
	}
	answers, ok := root["answers"].(map[string]any)
	if !ok || len(answers) != len(questions) {
		return nil, ProviderUsage{}, ErrUnavailable
	}
	for id, value := range answers {
		q, exists := questions[id].(map[string]any)
		if !exists || !systemOneAnswer(q, value) {
			return nil, ProviderUsage{}, ErrUnavailable
		}
	}
	input, output, ok := systemOneUsage(root["usage"])
	if !ok {
		return nil, ProviderUsage{}, ErrUnavailable
	}
	provider, configured := systemOneIdentity(j)
	return json.RawMessage(structuredText(root)), ProviderUsage{Provider: provider, Model: configured, Requests: 1, InputTokens: input, OutputTokens: output, TotalTokens: input + output}, nil
}

func systemOneEnvelope(value any) (any, bool) {
	wrapped, ok := value.(map[string]any)
	if !ok || wrapped["success"] != true {
		return nil, false
	}
	if errors, exists := wrapped["errors"]; exists && structuredText(errors) != "[]" {
		return nil, false
	}
	return wrapped["result"], true
}

func systemOneAnswer(q map[string]any, value any) bool {
	required := []string{"type", "noul"}
	if q["type"] == "choice" {
		required = []string{"type", "choice", "confidence", "probabilities"}
	} else if q["type"] == "score" {
		required = []string{"type", "score", "confidence", "probabilities", "legend"}
	}
	a, ok := systemOneFields(value, required, nil)
	if !ok || a["type"] != q["type"] {
		return false
	}
	if q["type"] == "noul" {
		n, ok := systemOneNumber(a["noul"])
		return ok && probability(n)
	}
	confidence, ok := systemOneNumber(a["confidence"])
	if !ok || !probability(confidence) {
		return false
	}
	var keys []string
	if q["type"] == "choice" {
		keys, ok = systemOneChoiceKeys(q, a)
	} else {
		keys, ok = systemOneScoreKeys(q, a)
	}
	if !ok {
		return false
	}
	probabilities, ok := systemOneProbabilities(a, keys)
	if !ok {
		return false
	}
	if q["type"] == "choice" {
		selected := probabilities[a["choice"].(string)]
		for _, p := range probabilities {
			if selected+1e-9 < p {
				return false
			}
		}
		return true
	}
	weighted := 0.0
	for i, key := range keys {
		weighted += float64(i) * probabilities[key]
	}
	score, _ := systemOneNumber(a["score"])
	// Allow the same two-decimal rounding as distribution(), plus score rounding.
	tolerance := 0.005 * float64(1+len(keys)*(len(keys)-1)/2)
	return !(math.Abs(score-weighted) > tolerance+1e-9)
}

func systemOneChoiceKeys(q, a map[string]any) ([]string, bool) {
	criteria := q["criteria"].(map[string]any)
	choice, ok := a["choice"].(string)
	if _, exists := criteria[choice]; !ok || !exists {
		return nil, false
	}
	keys := []string{}
	for key := range criteria {
		keys = append(keys, key)
	}
	return keys, true
}

func systemOneScoreKeys(q, a map[string]any) ([]string, bool) {
	criteria := q["criteria"].([]any)
	score, ok := systemOneNumber(a["score"])
	legend, legendOK := a["legend"].(map[string]any)
	if !ok || score < 0 || score > float64(len(criteria)-1) || !legendOK || len(legend) != len(criteria) {
		return nil, false
	}
	keys := []string{}
	for i, description := range criteria {
		key := strconv.Itoa(i)
		if structuredText(legend[key]) != structuredText(description) {
			return nil, false
		}
		keys = append(keys, key)
	}
	return keys, true
}

func systemOneProbabilities(a map[string]any, keys []string) (map[string]float64, bool) {
	p, ok := a["probabilities"].(map[string]any)
	if !ok {
		return nil, false
	}
	probabilities := map[string]float64{}
	for key, value := range p {
		n, ok := systemOneNumber(value)
		if !ok {
			return nil, false
		}
		probabilities[key] = n
	}
	sum := 0.0
	for _, n := range probabilities {
		sum += n
	}
	if !distribution(probabilities, keys) || sum <= 0 {
		return nil, false
	}
	return probabilities, true
}

func systemOneUsage(value any) (int64, int64, bool) {
	u, ok := systemOneFields(value, []string{"input_tokens"}, []string{"output_tokens"})
	if !ok {
		return 0, 0, false
	}
	input, ok := systemOneTokens(u["input_tokens"])
	if !ok {
		return 0, 0, false
	}
	output := int64(0)
	if value, exists := u["output_tokens"]; exists {
		output, ok = systemOneTokens(value)
		if !ok {
			return 0, 0, false
		}
	}
	if input > math.MaxInt64-output {
		return 0, 0, false
	}
	return input, output, true
}

func (s *Service) EvaluateSystemOne(ctx context.Context, token string, in SystemOneInput) (SystemOneResult, error) {
	if !validScope(in.Scope) {
		return SystemOneResult{}, ErrInvalid
	}
	payload, questions, err := systemOneRequest(in.Request)
	if err != nil {
		return SystemOneResult{}, err
	}
	judge := s.systemOneJudge(payload["model"])
	if judge == nil {
		return SystemOneResult{}, ErrUnavailable
	}
	provider, model := systemOneIdentity(judge)
	if provider == "cloudflare" {
		for id := range questions {
			if !systemOneID.MatchString(id) {
				return SystemOneResult{}, ErrInvalid
			}
		}
	}
	payload["model"] = model
	content := structuredText(payload)
	d, err := s.authorizeAt(ctx, token, "route:models", in.Scope, routingID(in.Scope), content, "external:"+provider)
	if err != nil {
		return SystemOneResult{}, err
	}
	if !d.ExternalTransfer || !d.ContentSafe || !safeTransfer(content) || systemOneHasKey(payload, judge.APIKey) {
		return SystemOneResult{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, d)
	if _, err := s.authorizeAt(ctx, token, "write:"+colRoutingCalls, in.Scope, "", "", "internal"); err != nil {
		return SystemOneResult{}, err
	}
	endpoint, err := systemOneEndpoint(judge, provider)
	if err != nil {
		return SystemOneResult{}, err
	}
	op, cancel := context.WithTimeout(ctx, jevTimeout)
	defer cancel()
	reserved, err := s.beginProviderCall(op, in.Scope, "systemone", provider, model)
	if err != nil {
		return SystemOneResult{}, err
	}
	started := time.Now()
	body, status, callErr := postJSON(op, providerClient(nil, jevTimeout), endpoint, judge.APIKey, payload, systemOneRequestMax, systemOneResponseMax)
	latency := time.Since(started).Milliseconds()
	out, usage, reason, callErr := systemOneOutcome(body, status, callErr, judge, questions)
	success := callErr == nil && status == http.StatusOK
	if success {
		reason = ""
	}
	// Record even after caller cancellation, without retaining request content.
	recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), jevTimeout)
	defer recordCancel()
	finishErr := s.finishProviderCall(recordCtx, reserved, usage, success)
	_, recordErr := s.RecordRoutingCall(recordCtx, token, RoutingCallInput{Scope: in.Scope, Call: RoutingCall{
		Model: provider + "/" + model, Decision: "fixed", OK: success, Error: reason,
		LatencyMS: latency, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		// Arkism's own judge call, not router traffic: the routing screens leave it out.
		Origin: &RoutingOrigin{Kind: "system"},
	}})
	if callErr != nil || finishErr != nil || recordErr != nil {
		return SystemOneResult{}, ErrUnavailable
	}
	return out, nil
}

func (s *Service) systemOneJudge(model any) *JevJudge {
	for _, j := range s.systemOneJudges() {
		provider, name := systemOneIdentity(j)
		if model == provider+"/"+name {
			return j
		}
	}
	return nil
}

func systemOneEndpoint(judge *JevJudge, provider string) (string, error) {
	endpoint := judge.Endpoint
	if endpoint == "" {
		if provider != "typesafe" {
			return "", ErrUnavailable
		}
		endpoint = "https://api.typesafe.ai/v1/systemone"
	}
	if validateEndpoint(endpoint) != nil {
		return "", ErrInvalid
	}
	return endpoint, nil
}

func systemOneOutcome(body []byte, status int, callErr error, judge *JevJudge, questions map[string]any) (SystemOneResult, ProviderUsage, string, error) {
	provider, model := systemOneIdentity(judge)
	usage := ProviderUsage{Provider: provider, Model: model, Requests: 1}
	out := SystemOneResult{Status: status, Authority: AuthorityDataOnly}
	reason := "provider_unavailable"
	if status != 0 && status != http.StatusOK {
		reason = "upstream_http_" + strconv.Itoa(status)
		out.Result = json.RawMessage(structuredText(map[string]any{"error": map[string]string{"type": "api_error", "message": reason}}))
		callErr = nil
	} else if callErr == nil {
		out.Result, usage, callErr = systemOneResponse(body, judge, questions)
		if callErr != nil {
			usage = ProviderUsage{Provider: provider, Model: model, Requests: 1}
			reason = "invalid_provider_response"
		}
	}
	return out, usage, reason, callErr
}

func systemOneRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/routing/systemone", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.SystemOneModels(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/routing/systemone", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, systemOneRequestMax+1024))
		value, jsonErr := systemOneJSON(raw)
		root, valid := systemOneFields(value, []string{"scope", "request"}, nil)
		scope, scopeValid := systemOneFields(root["scope"], nil, []string{"workspace_id", "project_id", "user_id", "agent_id"})
		for _, value := range scope {
			_, isString := value.(string)
			scopeValid = scopeValid && isString
		}
		var in SystemOneInput
		if err != nil || jsonErr != nil || !valid || !scopeValid || decodeStrict(raw, &in) != nil {
			respond(w, nil, ErrInvalid)
			return
		}
		out, err := s.EvaluateSystemOne(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}
