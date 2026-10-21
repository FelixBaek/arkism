package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
)

// Every production registration goes through this narrow interface. Tests may
// still use a plain ServeMux to exercise an individual transport adapter.
type routeRegistrar interface {
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
}

type routeScope uint8

const (
	routePublic routeScope = iota
	routeQuery
	routeJSON
	routeMultipart
)

// routePolicy is the coarse route permission. Service methods retain their
// payload, evidence, resource-state and secondary-action checks. A successful
// route check never grants those additional permissions or caches a decision.
type routePolicy struct {
	action string
	scope  routeScope
	limit  int64
	roles  []string
}

type authorizationMux struct {
	mux     *http.ServeMux
	service *Service
}

func newAuthorizationMux(s *Service) *authorizationMux {
	return &authorizationMux{mux: http.NewServeMux(), service: s}
}

func (m *authorizationMux) HandleFunc(pattern string, next func(http.ResponseWriter, *http.Request)) {
	policy, ok := httpRoutePolicies[pattern]
	if !ok || policy.scope != routePublic && policy.action == "" {
		panic("HTTP route has no explicit authorization policy: " + pattern)
	}
	m.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policy.scope == routePublic {
			next(w, r)
			return
		}
		if bearer(r) == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="arkism"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "authentication_required"})
			return
		}
		actor, err := m.service.authorizeHTTPRoute(w, r, policy)
		if err != nil {
			respond(w, nil, err)
			return
		}
		bound := &httpActor{principal: actor.PrincipalID, role: actor.Role}
		ctx := context.WithValue(r.Context(), httpActorKey{}, bound)
		// A service may intentionally filter an ordinary per-record denial. An
		// identity change is different: discard even a masked success response.
		response := &routeResponse{headers: make(http.Header)}
		next(response, r.WithContext(ctx))
		if bound.changed.Load() {
			respond(w, nil, ErrDenied)
			return
		}
		response.flush(w)
	}))
}

func (m *authorizationMux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mux.ServeHTTP(w, r) }
func (m *authorizationMux) Handler(r *http.Request) (http.Handler, string)   { return m.mux.Handler(r) }

type httpActorKey struct{}
type httpActor struct {
	principal, role string
	changed         atomic.Bool
}

// Protected handlers return bounded JSON or artifact bytes, never an HTTP
// stream. Keep the response private until all nested policy checks finish.
type routeResponse struct {
	headers http.Header
	body    bytes.Buffer
	status  int
}

func (w *routeResponse) Header() http.Header { return w.headers }
func (w *routeResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *routeResponse) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(p)
}
func (w *routeResponse) flush(dst http.ResponseWriter) {
	for name, values := range w.headers {
		dst.Header()[name] = values
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	dst.WriteHeader(w.status)
	_, _ = dst.Write(w.body.Bytes())
}

type httpPreflightKey struct{}

func (s *Service) authorizeHTTPRoute(w http.ResponseWriter, r *http.Request, policy routePolicy) (Decision, error) {
	scope, input, err := policy.requestScope(w, r)
	if err != nil {
		return Decision{}, err
	}
	actions, err := policy.requestActions(r, input)
	if err != nil {
		return Decision{}, err
	}
	// Code graph records belong to a project even when a caller supplies a user
	// scope. Match the service's normalization before asking for permission.
	if strings.HasPrefix(r.URL.Path, "/v1/code_graph/") {
		scope = projectScope(scope)
	}
	// Legacy source deletion enters the privacy-retention saga, which also
	// permits an explicitly granted service. Other generic deletes stay human.
	if policy.action == "delete:{collection}" && r.PathValue("collection") == "sources" {
		policy.roles = []string{roleHuman, roleService}
	}
	var actor Decision
	for _, action := range actions {
		ctx := context.WithValue(r.Context(), httpPreflightKey{}, true)
		d, err := s.authorize(ctx, bearer(r), action, scope, r.PathValue("id"), "")
		if err != nil {
			return Decision{}, err
		}
		if !decisionRoles[d.Role] || len(policy.roles) > 0 && !slices.Contains(policy.roles, d.Role) {
			return Decision{}, ErrDenied
		}
		if actor.PrincipalID != "" && (d.PrincipalID != actor.PrincipalID || d.Role != actor.Role) {
			return Decision{}, ErrDenied
		}
		actor = d
	}
	return actor, nil
}

// Scope is read from the same input as the endpoint, never an alternate query
// parameter on a JSON route. Buffered JSON is replayed byte-for-byte so existing
// strict decoding, trailing-data and per-route limits remain effective.
type routeInput struct {
	Scope  Scope `json:"scope"`
	Source struct {
		Scope Scope `json:"scope"`
	} `json:"source"`
	Purpose            string `json:"purpose"`
	Kind               string `json:"kind"`
	Memories           []Ref  `json:"memories"`
	TargetType         string `json:"target_type"`
	RetentionPolicyRef string `json:"retention_policy_ref"`
	Skill              struct {
		Scope Scope `json:"scope"`
	} `json:"skill"`
}

func (policy routePolicy) requestScope(w http.ResponseWriter, r *http.Request) (Scope, routeInput, error) {
	if policy.scope == routeQuery {
		scope := scopeQuery(r)
		if !validScope(scope) {
			return Scope{}, routeInput{}, ErrInvalid
		}
		return scope, routeInput{}, nil
	}
	if policy.scope == routeMultipart {
		return readMultipartRouteScope(w, r)
	}
	if policy.scope != routeJSON || policy.limit < 1 {
		return Scope{}, routeInput{}, ErrDenied
	}
	r.Body = http.MaxBytesReader(w, r.Body, policy.limit)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return Scope{}, routeInput{}, errBlobTooLarge
		}
		return Scope{}, routeInput{}, ErrInvalid
	}
	original := r.Body
	r.Body = &replayedBody{Reader: bytes.NewReader(data), original: original}
	var input routeInput
	if json.Unmarshal(data, &input) != nil {
		return Scope{}, routeInput{}, ErrInvalid
	}
	if r.URL.Path == "/v1/skills/propose" {
		input.Scope = input.Skill.Scope
	}
	if !validScope(input.Scope) {
		return Scope{}, routeInput{}, ErrInvalid
	}
	if containsSecret(structuredText(input.Scope)) {
		return Scope{}, routeInput{}, ErrSecretDetected
	}
	return input.Scope, input, nil
}

type replayedBody struct {
	io.Reader
	original io.Closer
}

func (b *replayedBody) Close() error { return b.original.Close() }

func readMultipartRouteScope(w http.ResponseWriter, r *http.Request) (Scope, routeInput, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFileRequestBytes)
	original := r.Body
	var prefix bytes.Buffer
	r.Body = &replayedBody{Reader: io.TeeReader(original, &prefix), original: original}
	reader, err := r.MultipartReader()
	if err != nil {
		return Scope{}, routeInput{}, ErrInvalid
	}
	part, err := reader.NextRawPart()
	if err != nil || part.FormName() != "metadata" {
		return Scope{}, routeInput{}, ErrInvalid
	}
	metadata, err := filePartBytes(part, maxFileMetadataBytes)
	if err != nil {
		return Scope{}, routeInput{}, err
	}
	var input routeInput
	if json.Unmarshal(metadata, &input) != nil || !validScope(input.Source.Scope) {
		return Scope{}, routeInput{}, ErrInvalid
	}
	if containsSecret(structuredText(input.Source.Scope)) {
		return Scope{}, routeInput{}, ErrSecretDetected
	}
	// The multipart reader may have read ahead into the first file buffer. Keep
	// that prefix too, without reading or buffering the remaining file payload.
	r.Body = &replayedBody{Reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), original), original: original}
	r.MultipartForm = nil // MultipartReader marks the request as already consumed.
	return input.Source.Scope, input, nil
}

func (policy routePolicy) requestActions(r *http.Request, input routeInput) ([]string, error) {
	action := policy.action
	if action == "{twin_action}" {
		action, err := twinClaimPermission(splitStatuses(r.URL.Query().Get("status")))
		if err != nil {
			return nil, err
		}
		return []string{action}, nil
	}
	if action == "transition:{conflict_collection}" {
		return conflictRouteActions(input.Memories)
	}
	collection, err := routeCollection(r, input, action)
	if err != nil {
		return nil, err
	}
	for _, template := range []string{"{collection}", "{input_collection}", "{digest_collection}", "{privacy_target}"} {
		action = strings.ReplaceAll(action, template, collection)
	}
	if !permissionPattern.MatchString(action) {
		return nil, ErrDenied
	}
	return []string{action}, nil
}

func routeCollection(r *http.Request, input routeInput, action string) (string, error) {
	if strings.Contains(action, "{digest_collection}") {
		kind := input.Kind
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			kind = r.URL.Query().Get("kind")
		}
		collection, ok := digestCollection(kind)
		if !ok {
			return "", ErrInvalid
		}
		return collection, nil
	}
	if strings.Contains(action, "{privacy_target}") {
		if input.TargetType == "work_checkpoint" {
			return colWorkCheckpoints, nil
		}
		return "sources", nil
	}
	collection := ""
	if strings.Contains(action, "{collection}") {
		collection = r.PathValue("collection")
		if !knownCollection(collection) {
			return "", ErrInvalid
		}
	}
	if strings.Contains(action, "{input_collection}") {
		var ok bool
		collection, ok = searchCollection(input.Purpose)
		if !ok {
			return "", ErrInvalid
		}
	}
	return collection, nil
}

func conflictRouteActions(refs []Ref) ([]string, error) {
	if len(refs) != 2 {
		return nil, ErrInvalid
	}
	var actions []string
	for _, ref := range refs {
		if ref.Collection != "memories" && ref.Collection != "memory_candidates" {
			return nil, ErrInvalid
		}
		action := "transition:" + ref.Collection
		if !slices.Contains(actions, action) {
			actions = append(actions, action)
		}
	}
	return actions, nil
}
