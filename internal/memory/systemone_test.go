package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const systemOneTestRequest = `{"model":"typesafe/jev-latest","state":{"message":"example"},"questions":{"pick":{"type":"choice","instructions":{"task":["choose"]},"criteria":{"a":null,"b":{"label":"second"}}},"grade":{"type":"score","instructions":["grade"],"criteria":["low",{"label":"high"}]},"yes":{"type":"noul","instructions":"yes?","criteria":{"true":["yes"],"false":{"label":"no"}}}}}`
const systemOneTestResponse = `{"model":"jev-1.13.0","answers":{"pick":{"type":"choice","choice":"a","confidence":0.9,"probabilities":{"a":0.9,"b":0.1}},"grade":{"type":"score","score":0.25,"confidence":0.75,"legend":{"0":"low","1":{"label":"high"}},"probabilities":{"0":0.75,"1":0.25}},"yes":{"type":"noul","noul":0.75}},"usage":{"input_tokens":23,"output_tokens":7}}`

type systemOneAuth struct {
	Authorizer
	requests []Authorization
	edit     func(Authorization, *Decision)
}

func (a *systemOneAuth) Authorize(ctx context.Context, token string, req Authorization) (Decision, error) {
	a.requests = append(a.requests, req)
	d, err := a.Authorizer.Authorize(ctx, token, req)
	if a.edit != nil {
		a.edit(req, &d)
	}
	return d, err
}

type systemOneFailRepo struct {
	Repository
	failInsert  string
	failReplace bool
}

func (r systemOneFailRepo) Insert(ctx context.Context, col string, rec Record) error {
	if col == r.failInsert {
		return ErrUnavailable
	}
	return r.Repository.Insert(ctx, col, rec)
}

func (r systemOneFailRepo) Replace(ctx context.Context, col string, rec Record, rev int64) error {
	if col == colProviderUsage && r.failReplace {
		return ErrUnavailable
	}
	return r.Repository.Replace(ctx, col, rec, rev)
}

func systemOneTestInput(scope Scope) SystemOneInput {
	return SystemOneInput{Scope: scope, Request: json.RawMessage(systemOneTestRequest)}
}

// exact payload authorization, configured selection,
// bare provider model, scoped metadata, and absence of persisted context/secrets.
func TestSystemOneHTTPAndRecords(t *testing.T) {
	f := newFixture(t)
	a := &systemOneAuth{Authorizer: f.auth}
	f.svc.Auth = a
	var calls atomic.Int32
	var sent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		sent = string(raw)
		if r.Header.Get("Authorization") != "Bearer provider-only-credential" || r.Header.Get("X-Context-User-Token") != "" || r.URL.RawQuery != "" {
			t.Error("provider credential isolation failed")
		}
		io.WriteString(w, systemOneTestResponse)
	}))
	defer upstream.Close()
	f.svc.ModelPicker = &JevJudge{Model: "typesafe/jev-latest", APIKey: "provider-only-credential", Endpoint: upstream.URL}
	mux := http.NewServeMux()
	routingRoutes(mux, f.svc)
	get := httptest.NewRequest("GET", "/v1/routing/systemone?workspace_id=ws-a&project_id=proj-a", nil)
	get.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, get)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"typesafe/jev-latest"`) || calls.Load() != 0 {
		t.Fatalf("GET: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
	}
	if len(a.requests) != 1 || a.requests[0].Content != "" || a.requests[0].Context.Destination != "internal" {
		t.Fatal("GET transferred content")
	}
	for _, scope := range []Scope{scopeA, scopeB} {
		body := structuredText(systemOneTestInput(scope))
		req := httptest.NewRequest("POST", "/v1/routing/systemone", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var result SystemOneResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Status != 200 || result.Authority != AuthorityDataOnly {
			t.Fatalf("POST: %d %s", w.Code, w.Body.String())
		}
		if string(result.Result) != structuredText(mustSystemOneJSON(t, systemOneTestResponse)) {
			t.Fatalf("bare provider result changed: %s", result.Result)
		}
		payload := mustSystemOneJSON(t, systemOneTestRequest).(map[string]any)
		payload["model"] = "jev-latest"
		if sent != structuredText(payload) {
			t.Fatal("structured request was changed")
		}
		var route Authorization
		for _, auth := range a.requests {
			if auth.Scope == scope && auth.Action == "route:models" && auth.Content != "" {
				route = auth
			}
		}
		if route.Content != sent || route.Context.Destination != "external:typesafe" {
			t.Fatal("authorization did not cover exact provider payload/destination")
		}
		costs, _ := f.repo.ListRecords(testCtx, scope, colProviderUsage, 10)
		runs, _ := f.repo.ListRecords(testCtx, scope, colRoutingCalls, 10)
		if len(costs) != 1 || costs[0].Status != JobStatusDone || costs[0].Cost.Model != "jev-latest" || costs[0].Cost.InputTokens != 23 || costs[0].Cost.OutputTokens != 7 || costs[0].Cost.Requests != 1 || len(runs) != 1 {
			t.Fatalf("metadata scope=%v costs=%v runs=%v", scope, costs, runs)
		}
		c := runs[0].RoutingCall
		if c.Decision != "fixed" || !c.OK || c.Model != "typesafe/jev-latest" || c.InputTokens != 23 || c.OutputTokens != 7 || c.LatencyMS < 0 {
			t.Fatalf("routing metadata: %+v", c)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
	stored := structuredText(f.repo.store)
	for _, private := range []string{"provider-only-credential", "instructions", "questions", "message", "example", f.human} {
		if strings.Contains(stored, private) {
			t.Fatalf("persisted private data: %s", private)
		}
	}
}

func mustSystemOneJSON(t *testing.T, raw string) any {
	t.Helper()
	value, err := systemOneJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSystemOneConfiguredModels(t *testing.T) {
	f := newFixture(t)
	primary := &JevJudge{Provider: "cloudflare", Model: "clef-flash", APIKey: "cloud-credential"}
	secondary := &JevJudge{Model: "jev-latest", APIKey: "jev-credential"}
	fallback := &FallbackJudge{Primary: primary, Secondary: secondary}
	f.svc.Router = fallback
	f.svc.ModelPicker = primary
	list, err := f.svc.SystemOneModels(testCtx, f.human, scopeA)
	if err != nil || len(list.Items) != 2 || list.Items[0].ID != "cloudflare/clef-flash" || list.Items[1].ID != "typesafe/jev-latest" {
		t.Fatalf("configured models: %+v %v", list, err)
	}
	secondary.APIKey = ""
	list, err = f.svc.SystemOneModels(testCtx, f.human, scopeA)
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("empty key listed: %+v %v", list, err)
	}
	fallback.Secondary = fallback // cycles and typed nils must not panic.
	list, err = f.svc.SystemOneModels(testCtx, f.human, scopeA)
	if err != nil || len(list.Items) != 1 {
		t.Fatal("fallback cycle")
	}
	f.svc.ModelPicker = (*JevJudge)(nil)
	f.svc.Router = (*FallbackJudge)(nil)
	list, err = f.svc.SystemOneModels(testCtx, f.human, scopeA)
	if err != nil || list.Items == nil || len(list.Items) != 0 {
		t.Fatalf("nil judge: %+v %v", list, err)
	}
}

func TestSystemOneRequestValidation(t *testing.T) {
	base := systemOneTestRequest
	for name, raw := range map[string]string{
		"unknown root":         strings.Replace(base, `"state":`, `"stream":false,"state":`, 1),
		"duplicate root":       strings.Replace(base, `"state":`, `"model":"typesafe/jev-latest","state":`, 1),
		"duplicate structured": strings.Replace(base, `"message":"example"`, `"message":"example","message":"other"`, 1),
		"alias duplicate":      strings.Replace(base, `"message":"example"`, `"message":"example","\u006dessage":"other"`, 1),
		"question type":        strings.Replace(base, `"type":"choice"`, `"type":3`, 1),
		"question option":      strings.Replace(base, `"type":"choice"`, `"type":"choice","other":true`, 1),
		"state null":           strings.Replace(base, `{"message":"example"}`, `null`, 1),
		"instructions boolean": strings.Replace(base, `{"task":["choose"]}`, `true`, 1),
		"noul criterion":       strings.Replace(base, `"true":["yes"]`, `"maybe":["yes"]`, 1),
		"score minimum":        strings.Replace(base, `["low",{"label":"high"}]`, `["low"]`, 1),
		"score null":           strings.Replace(base, `["low",{"label":"high"}]`, `["low",null]`, 1),
		"choice boolean":       strings.Replace(base, `"a":null`, `"a":true`, 1),
		"trailing JSON":        base + `{}`,
		"array root":           `[]`,
		"empty questions":      `{"model":"typesafe/jev-latest","state":"ok","questions":{}}`,
		"empty choice":         `{"model":"typesafe/jev-latest","state":"ok","questions":{"q":{"type":"choice","instructions":"choose","criteria":{}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := systemOneRequest([]byte(raw)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for _, raw := range []string{
		strings.Replace(base, `"pick":`, `"한글 질문 이름":`, 1),
		strings.Replace(base, `"true":["yes"]`, `"true":null`, 1),
		`{"model":"typesafe/jev-latest","state":"ok","questions":{"q":{"type":"noul"}}}`,
		`{"model":"typesafe/jev-latest","state":"ok","questions":{"q":{"type":"noul","instructions":null,"criteria":null}}}`,
		`{"model":"typesafe/jev-latest","state":[],"questions":{"q":{"type":"noul","instructions":{}}}}`,
		`{"model":"typesafe/jev-latest","state":"ok","questions":{"q":{"type":"choice","instructions":"choose","criteria":{"single":null}}}}`,
	} {
		if _, _, err := systemOneRequest([]byte(raw)); err != nil {
			t.Fatalf("valid input rejected: %v", err)
		}
	}
	for _, tc := range []struct {
		kind  string
		count int
	}{{"questions", 65}, {"choice", 256}, {"score", 11}} {
		root := mustSystemOneJSON(t, base).(map[string]any)
		switch tc.kind {
		case "questions":
			questions := map[string]any{}
			for i := 0; i < tc.count; i++ {
				questions[ID()] = map[string]any{"type": "noul", "instructions": "yes?"}
			}
			root["questions"] = questions
		case "choice":
			criteria := map[string]any{}
			for i := 0; i < tc.count; i++ {
				criteria[ID()] = nil
			}
			root["questions"].(map[string]any)["pick"].(map[string]any)["criteria"] = criteria
		case "score":
			criteria := []any{}
			for i := 0; i < tc.count; i++ {
				criteria = append(criteria, "level")
			}
			root["questions"].(map[string]any)["grade"].(map[string]any)["criteria"] = criteria
		}
		if _, _, err := systemOneRequest([]byte(structuredText(root))); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s max not enforced", tc.kind)
		}
	}
}

func TestSystemOneResponseValidation(t *testing.T) {
	_, questions, _ := systemOneRequest([]byte(systemOneTestRequest))
	j := &JevJudge{Model: "jev-latest", APIKey: "provider-only-credential"}
	for name, raw := range map[string]string{
		"unknown root":                   strings.Replace(systemOneTestResponse, `"answers":`, `"debug":"private","answers":`, 1),
		"duplicate root":                 strings.Replace(systemOneTestResponse, `"answers":`, `"model":"jev-1.13.0","answers":`, 1),
		"duplicate answer":               strings.Replace(systemOneTestResponse, `"noul":0.75`, `"noul":0.75,"noul":0.5`, 1),
		"unknown answer":                 strings.Replace(systemOneTestResponse, `"noul":0.75`, `"noul":0.75,"debug":"private"`, 1),
		"question id":                    strings.Replace(systemOneTestResponse, `"yes":`, `"other":`, 1),
		"type":                           strings.Replace(systemOneTestResponse, `"type":"choice"`, `"type":"noul"`, 1),
		"choice":                         strings.Replace(systemOneTestResponse, `"choice":"a"`, `"choice":"c"`, 1),
		"choice contradicts probability": strings.Replace(systemOneTestResponse, `"choice":"a"`, `"choice":"b"`, 1),
		"score contradicts probability":  strings.Replace(systemOneTestResponse, `"score":0.25`, `"score":0.99`, 1),
		"confidence":                     strings.Replace(systemOneTestResponse, `"confidence":0.9`, `"confidence":1.1`, 1),
		"probability":                    strings.Replace(systemOneTestResponse, `"a":0.9`, `"a":1.2`, 1),
		"distribution":                   strings.Replace(systemOneTestResponse, `"a":0.9`, `"a":0.3`, 1),
		"probability null":               strings.Replace(systemOneTestResponse, `"a":0.9`, `"a":null`, 1),
		"score":                          strings.Replace(systemOneTestResponse, `"score":0.25`, `"score":1.25`, 1),
		"legend":                         strings.Replace(systemOneTestResponse, `"0":"low"`, `"0":"other"`, 1),
		"legend missing":                 strings.Replace(systemOneTestResponse, `"0":"low",`, ``, 1),
		"noul":                           strings.Replace(systemOneTestResponse, `"noul":0.75`, `"noul":-0.1`, 1),
		"unknown usage":                  strings.Replace(systemOneTestResponse, `"input_tokens":23`, `"debug":"private","input_tokens":23`, 1),
		"missing input":                  strings.Replace(systemOneTestResponse, `"input_tokens":23,`, ``, 1),
		"negative usage":                 strings.Replace(systemOneTestResponse, `"input_tokens":23`, `"input_tokens":-1`, 1),
		"fraction usage":                 strings.Replace(systemOneTestResponse, `"output_tokens":7`, `"output_tokens":0.5`, 1),
		"null usage":                     strings.Replace(systemOneTestResponse, `"output_tokens":7`, `"output_tokens":null`, 1),
		"overflow usage":                 strings.Replace(systemOneTestResponse, `"input_tokens":23`, `"input_tokens":9223372036854775807`, 1),
		"secret model":                   strings.Replace(systemOneTestResponse, `jev-1.13.0`, `provider-only-credential`, 1),
		"escaped secret":                 strings.Replace(systemOneTestResponse, `jev-1.13.0`, `\u0070rovider-only-credential`, 1),
		"unsafe model":                   strings.Replace(systemOneTestResponse, `jev-1.13.0`, `jev\nprivate`, 1),
		"truncated":                      systemOneTestResponse[:len(systemOneTestResponse)-1],
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := systemOneResponse([]byte(raw), j, questions); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	raw := strings.Replace(systemOneTestResponse, `,"output_tokens":7`, ``, 1)
	result, usage, err := systemOneResponse([]byte(raw), j, questions)
	if err != nil || strings.Contains(string(result), "output_tokens") || usage.InputTokens != 23 || usage.OutputTokens != 0 {
		t.Fatalf("missing output: %s %+v %v", result, usage, err)
	}
	j.Envelope = true
	wrapped := `{"success":true,"errors":[],"messages":[],"result":` + raw + `}`
	result, _, err = systemOneResponse([]byte(wrapped), j, questions)
	if err != nil || strings.Contains(string(result), "result") {
		t.Fatalf("envelope: %s %v", result, err)
	}
	for _, bad := range []string{strings.Replace(wrapped, `"success":true`, `"success":false`, 1), strings.Replace(wrapped, `"errors":[]`, `"errors":[{"message":"bad"}]`, 1), strings.Replace(wrapped, `"messages":[]`, `"messages":["provider-only-credential"]`, 1)} {
		if _, _, err := systemOneResponse([]byte(bad), j, questions); err == nil {
			t.Fatal("envelope refusal/secret accepted")
		}
	}
}

func TestSystemOneRoundedAnswers(t *testing.T) {
	_, questions, _ := systemOneRequest([]byte(systemOneTestRequest))
	j := &JevJudge{APIKey: "provider-only-credential"}
	tie := strings.Replace(systemOneTestResponse, `"a":0.9,"b":0.1`, `"a":0.5,"b":0.5`, 1)
	for _, raw := range []string{tie, strings.Replace(tie, `"choice":"a"`, `"choice":"b"`, 1),
		strings.Replace(systemOneTestResponse, `"score":0.25`, `"score":0.255`, 1)} {
		if _, _, err := systemOneResponse([]byte(raw), j, questions); err != nil {
			t.Fatalf("valid tied/rounded answer: %v", err)
		}
	}
}

func TestSystemOneNoGenerationBeforeGates(t *testing.T) {
	var calls atomic.Int32
	original := http.DefaultTransport
	http.DefaultTransport = systemOneTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(systemOneTestResponse))}, nil
	})
	defer func() { http.DefaultTransport = original }()
	for _, name := range []string{"nil key", "unknown model", "group", "token", "permission", "external", "unsafe", "scope", "secret", "escaped secret", "provider key in data", "record permission", "reserve failure", "endpoint", "missing configured endpoint", "safeTransfer limit", "cloudflare question name"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			j := &JevJudge{Model: "jev-latest", APIKey: "provider-only-credential", Endpoint: "https://provider.invalid/systemone"}
			f.svc.ModelPicker = j
			in, token := systemOneTestInput(scopeA), f.human
			a := &systemOneAuth{Authorizer: f.auth}
			f.svc.Auth = a
			switch name {
			case "nil key":
				j.APIKey = ""
			case "unknown model":
				in.Request = json.RawMessage(strings.Replace(string(in.Request), "typesafe/jev-latest", "typesafe/other", 1))
			case "group":
				in.Request = json.RawMessage(strings.Replace(string(in.Request), "typesafe/jev-latest", "group/fast", 1))
			case "token":
				token = ""
			case "permission":
				a.edit = func(req Authorization, d *Decision) { d.Allowed = false }
			case "external":
				a.edit = func(req Authorization, d *Decision) { d.ExternalTransfer = false }
			case "unsafe":
				a.edit = func(req Authorization, d *Decision) { d.ContentSafe = false }
			case "scope":
				a.edit = func(req Authorization, d *Decision) { d.Scope = scopeFar }
			case "secret":
				in.Request = json.RawMessage(strings.Replace(string(in.Request), "example", "sk-abcdefghijklmnopqrstuv", 1))
			case "escaped secret":
				in.Request = json.RawMessage(strings.Replace(string(in.Request), "example", `\u0073k-abcdefghijklmnopqrstuv`, 1))
			case "provider key in data":
				in.Request = json.RawMessage(strings.Replace(string(in.Request), "example", j.APIKey, 1))
			case "record permission":
				a.edit = func(req Authorization, d *Decision) {
					if req.Action == "write:"+colRoutingCalls {
						d.Allowed = false
					}
				}
			case "reserve failure":
				f.svc.Repo = systemOneFailRepo{Repository: f.repo, failInsert: colProviderUsage}
			case "endpoint":
				j.Endpoint = "http://example.com/unsafe"
			case "missing configured endpoint":
				j.Provider, j.Endpoint = "custom", ""
				in.Request = json.RawMessage(strings.Replace(string(in.Request), "typesafe/jev-latest", "custom/jev-latest", 1))
			case "cloudflare question name":
				j.Provider = "cloudflare"
				in.Request = json.RawMessage(strings.ReplaceAll(strings.Replace(string(in.Request), "typesafe/jev-latest", "cloudflare/jev-latest", 1), `"yes":`, `"한글 질문":`))
			case "safeTransfer limit":
				in.Request = json.RawMessage(strings.Replace(string(in.Request), "example", strings.Repeat("x", 64000), 1))
			}
			before := calls.Load()
			if _, err := f.svc.EvaluateSystemOne(testCtx, token, in); err == nil {
				t.Fatal("gate accepted")
			}
			if calls.Load() != before {
				t.Fatal("generated before gate")
			}
			rows, _ := f.repo.ListRecords(testCtx, scopeA, colProviderUsage, 10)
			if len(rows) != 0 {
				t.Fatal("reserved before validation/authorization")
			}
		})
	}
}

func TestSystemOneFailuresRecordOnce(t *testing.T) {
	for _, name := range []string{"http refusal", "redirect", "truncated", "invalid", "secret", "large response", "timeout", "finish failure", "routing record failure"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch name {
				case "http refusal":
					w.WriteHeader(429)
					io.WriteString(w, `{"error":"provider-only-credential private"}`)
				case "redirect":
					w.Header().Set("Location", "/again")
					w.WriteHeader(307)
				case "truncated":
					w.Header().Set("Content-Length", "9999")
					io.WriteString(w, systemOneTestResponse)
				case "invalid":
					io.WriteString(w, `{}`)
				case "secret":
					io.WriteString(w, strings.Replace(systemOneTestResponse, "jev-1.13.0", "provider-only-credential", 1))
				case "large response":
					io.WriteString(w, strings.Repeat("x", systemOneResponseMax+1))
				case "timeout":
					time.Sleep(80 * time.Millisecond)
					io.WriteString(w, systemOneTestResponse)
				default:
					io.WriteString(w, systemOneTestResponse)
				}
			}))
			defer server.Close()
			f.svc.ModelPicker = &JevJudge{Model: "jev-latest", APIKey: "provider-only-credential", Endpoint: server.URL}
			if name == "finish failure" {
				f.svc.Repo = systemOneFailRepo{Repository: f.repo, failReplace: true}
			}
			if name == "routing record failure" {
				f.svc.Repo = systemOneFailRepo{Repository: f.repo, failInsert: colRoutingCalls}
			}
			ctx := testCtx
			if name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(testCtx, 30*time.Millisecond)
				defer cancel()
			}
			out, err := f.svc.EvaluateSystemOne(ctx, f.human, systemOneTestInput(scopeA))
			if name == "http refusal" || name == "redirect" {
				status := 429
				if name == "redirect" {
					status = 307
				}
				if err != nil || out.Status != status || !strings.Contains(string(out.Result), "upstream_http_") || strings.Contains(string(out.Result), "credential") {
					t.Fatalf("HTTP status result=%+v err=%v", out, err)
				}
			} else if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("failure error=%v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("attempts=%d", calls.Load())
			}
			costs, _ := f.repo.ListRecords(testCtx, scopeA, colProviderUsage, 10)
			runs, _ := f.repo.ListRecords(testCtx, scopeA, colRoutingCalls, 10)
			if len(costs) != 1 || name != "routing record failure" && len(runs) != 1 {
				t.Fatalf("costs=%d runs=%d", len(costs), len(runs))
			}
			if name != "finish failure" && costs[0].Status == JobStatusPending {
				t.Fatal("usage left pending")
			}
			if name != "finish failure" && name != "routing record failure" && (costs[0].Status != JobStatusFailed || runs[0].RoutingCall.OK || runs[0].RoutingCall.Decision != "fixed" || runs[0].RoutingCall.Error == "") {
				t.Fatal("failed call not recorded")
			}
		})
	}
}

func TestSystemOneHTTPInputLimits(t *testing.T) {
	f := newFixture(t)
	mux := http.NewServeMux()
	routingRoutes(mux, f.svc)
	body := structuredText(systemOneTestInput(scopeA))
	for _, raw := range []string{
		strings.Replace(body, `"request":`, `"unknown":1,"request":`, 1),
		strings.Replace(body, `"request":`, `"request":{},"request":`, 1),
		strings.Replace(body, `"workspace_id":`, `"Workspace_ID":`, 1),
		strings.Replace(body, `"project_id":"proj-a"`, `"project_id":"proj-a","user_id":null`, 1),
		strings.Repeat(" ", systemOneRequestMax+1025),
		body + `{}`,
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/routing/systemone", strings.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+f.human)
		mux.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("bad wrapper: %d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		size  int
		valid bool
	}{{systemOneRequestMax, true}, {systemOneRequestMax + 1, false}} {
		raw := systemOneTestRequest + strings.Repeat(" ", tc.size-len(systemOneTestRequest))
		_, _, err := systemOneRequest([]byte(raw))
		if (err == nil) != tc.valid {
			t.Fatalf("request limit %d: %v", tc.size, err)
		}
	}
	get := httptest.NewRequest("GET", "/v1/routing/systemone?workspace_id=ws-a&project_id=proj-a", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, get)
	if w.Code != 403 {
		t.Fatalf("missing bearer GET: %d", w.Code)
	}
}

type systemOneTransport func(*http.Request) (*http.Response, error)

func (f systemOneTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Also verify the complete service path when the sandbox cannot open sockets.
func TestSystemOneOfflineProviderAndRecords(t *testing.T) {
	for _, name := range []string{"success", "inconsistent choice", "inconsistent score", "missing output", "secondary selected", "refusal", "refusal truncated", "redirect", "truncated", "large response", "secret", "timeout", "reserve failure", "finish failure"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			j := &JevJudge{Model: "jev-latest", APIKey: "provider-only-credential", Endpoint: "https://provider.invalid/systemone"}
			f.svc.ModelPicker = j
			if name == "secondary selected" {
				f.svc.ModelPicker = &JevJudge{Provider: "cloudflare", Model: "clef-flash", APIKey: "primary-credential", Endpoint: "https://primary.invalid/systemone"}
				f.svc.Router = &FallbackJudge{Primary: f.svc.ModelPicker.(*JevJudge), Secondary: j}
			}
			if name == "reserve failure" {
				f.svc.Repo = systemOneFailRepo{Repository: f.repo, failInsert: colProviderUsage}
			}
			if name == "finish failure" {
				f.svc.Repo = systemOneFailRepo{Repository: f.repo, failReplace: true}
			}
			a := &systemOneAuth{Authorizer: f.auth}
			f.svc.Auth = a
			calls := 0
			original := http.DefaultTransport
			http.DefaultTransport = systemOneTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "provider.invalid" || r.Header.Get("Authorization") != "Bearer "+j.APIKey || r.Header.Get("X-Context-User-Token") != "" {
					t.Fatal("wrong provider/credential")
				}
				raw, _ := io.ReadAll(r.Body)
				if len(a.requests) < 2 || a.requests[0].Content != string(raw) || a.requests[0].Context.Destination != "external:typesafe" {
					t.Fatal("authorization payload mismatch")
				}
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > jevTimeout {
					t.Fatal("provider timeout missing")
				}
				status, body := 200, systemOneTestResponse
				switch name {
				case "inconsistent choice":
					body = strings.Replace(body, `"choice":"a"`, `"choice":"b"`, 1)
				case "inconsistent score":
					body = strings.Replace(body, `"score":0.25`, `"score":0.99`, 1)
				case "missing output":
					body = strings.Replace(body, `,"output_tokens":7`, ``, 1)
				case "refusal":
					status, body = 429, `{"private":"provider-only-credential"}`
				case "refusal truncated":
					return &http.Response{StatusCode: 429, Header: http.Header{}, Body: systemOneBrokenBody{}}, nil
				case "redirect":
					status, body = 307, ""
				case "truncated":
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: systemOneBrokenBody{}}, nil
				case "large response":
					body = strings.Repeat("x", systemOneResponseMax+1)
				case "secret":
					body = strings.Replace(body, "jev-1.13.0", j.APIKey, 1)
				case "timeout":
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://primary.invalid/again"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			defer func() { http.DefaultTransport = original }()
			ctx := testCtx
			if name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
			}
			out, err := f.svc.EvaluateSystemOne(ctx, f.human, systemOneTestInput(scopeA))
			success := name == "success" || name == "missing output" || name == "secondary selected"
			if success {
				if err != nil || out.Status != 200 || !strings.Contains(string(out.Result), `"model":"jev-1.13.0"`) {
					t.Fatalf("success: %+v %v", out, err)
				}
				if name == "missing output" && strings.Contains(string(out.Result), "output_tokens") {
					t.Fatal("invented output usage")
				}
			} else if name == "refusal" || name == "refusal truncated" || name == "redirect" {
				status := 429
				if name == "redirect" {
					status = 307
				}
				want := `{"error":{"message":"upstream_http_` + strconv.Itoa(status) + `","type":"api_error"}}`
				if err != nil || out.Status != status || string(out.Result) != want {
					t.Fatalf("refusal: %+v %v", out, err)
				}
			} else if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("failure: %v", err)
			}
			costs, _ := f.repo.ListRecords(testCtx, scopeA, colProviderUsage, 10)
			runs, _ := f.repo.ListRecords(testCtx, scopeA, colRoutingCalls, 10)
			if name == "reserve failure" {
				if calls != 0 || len(costs) != 0 || len(runs) != 0 {
					t.Fatal("generation after reservation failure")
				}
				return
			}
			if calls != 1 || len(costs) != 1 || len(runs) != 1 {
				t.Fatalf("calls=%d cost=%d routes=%d", calls, len(costs), len(runs))
			}
			if runs[0].RoutingCall.Decision != "fixed" || runs[0].RoutingCall.Model != "typesafe/jev-latest" || runs[0].RoutingCall.OK != (success || name == "finish failure") {
				t.Fatal("wrong route metadata")
			}
			if name != "finish failure" && ((costs[0].Status == JobStatusDone) != success) {
				t.Fatalf("cost status=%s", costs[0].Status)
			}
			if success && (costs[0].Cost.InputTokens != 23 || runs[0].RoutingCall.InputTokens != 23) {
				t.Fatal("lost measured usage")
			}
			far, _ := f.repo.ListRecords(testCtx, scopeB, colRoutingCalls, 10)
			if len(far) != 0 {
				t.Fatal("scope leak")
			}
			stored := structuredText(f.repo.store)
			if strings.Contains(stored, j.APIKey) || strings.Contains(stored, "instructions") || strings.Contains(stored, "example") {
				t.Fatal("private data persisted")
			}
		})
	}
}

type systemOneBrokenBody struct{}

func (systemOneBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (systemOneBrokenBody) Close() error             { return nil }
