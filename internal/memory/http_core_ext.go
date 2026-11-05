package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Extension routes and tools (ext_runtime.go) are served under Arkism's own
// authorization: the action an extension declares is checked like any other.
const (
	extensionActionTemplate  = "{extension_action}"
	extensionToolCallPath    = "/v1/extension-tools/call"
	extensionBodyLimit       = 256 << 10
	extensionAuthorityHeader = "X-Arkism-Authority"
)

var extDefaultRoles = []string{roleHuman, roleAgent, roleService}

var extensionRoutePolicies = map[string]routePolicy{
	"GET /v1/extensions/{extension}/routes/{path...}":  {action: extensionActionTemplate, scope: routeQuery},
	"POST /v1/extensions/{extension}/routes/{path...}": {action: extensionActionTemplate, scope: routeJSON, limit: extensionBodyLimit},
	"GET /v1/extension-tools":                          {action: "search:memories", scope: routeQuery},
	"POST " + extensionToolCallPath:                    {action: extensionActionTemplate, scope: routeJSON, limit: extensionBodyLimit},
	"GET /v1/extensions/{extension}/data":              {action: "read:" + colExtData, scope: routeQuery},
	"GET /v1/extensions/{extension}/data/{key}":        {action: "read:" + colExtData, scope: routeQuery},
	"POST /v1/extensions/{extension}/data":             {action: "write:" + colExtData, scope: routeJSON, limit: 64 << 10, roles: []string{roleHuman, roleService}},
}

func init() {
	for pattern, policy := range extensionRoutePolicies {
		httpRoutePolicies[pattern] = policy
	}
}

func registerExtensionsCoreHTTP(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/extensions/{extension}/routes/{path...}", s.coreHTTPExtensionRoute)
	mux.HandleFunc("POST /v1/extensions/{extension}/routes/{path...}", s.coreHTTPExtensionRoute)
	mux.HandleFunc("GET /v1/extension-tools", s.coreHTTPExtensionTools)
	mux.HandleFunc("POST "+extensionToolCallPath, s.coreHTTPExtensionToolCall)
	mux.HandleFunc("GET /v1/extensions/{extension}/data", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := s.ListExtensionData(r.Context(), bearer(r), scopeQuery(r), r.PathValue("extension"), r.URL.Query().Get("cursor"), limit)
		respond(w, out, err)
	})
	mux.HandleFunc("GET /v1/extensions/{extension}/data/{key}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.GetExtensionData(r.Context(), bearer(r), scopeQuery(r), r.PathValue("extension"), r.PathValue("key"))
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/extensions/{extension}/data", func(w http.ResponseWriter, r *http.Request) {
		var in ExtDataChange
		if !decodeLimit(w, r, &in, 64<<10) {
			return
		}
		out, err := s.ChangeExtensionData(r.Context(), bearer(r), in.Scope, r.PathValue("extension"), in.Changes)
		respond(w, ExtDataPage{Items: out, Authority: AuthorityDataOnly}, err)
	})
}

// ExtDataChange is the body of POST /v1/extensions/{extension}/data: changes applied all or none.
type ExtDataChange struct {
	Scope   Scope       `json:"scope"`
	Changes []ExtDataOp `json:"changes"`
}

// extensionRouteBody is the body of a POST extension route: the scope and the extension's own input.
type extensionRouteBody struct {
	Scope Scope           `json:"scope"`
	Input json.RawMessage `json:"input,omitempty"`
}

// extensionPolicy swaps the placeholder for the action and roles the extension
// declared. An unknown route or tool falls back to read:memories, so a caller
// who may not read the space is refused before learning what exists; the
// handler then refuses a malformed body and the service answers not found.
func (s *Service) extensionPolicy(r *http.Request, input routeInput, policy routePolicy) routePolicy {
	tool := ""
	if r.URL.Path == extensionToolCallPath {
		tool = input.Name
	}
	route, err := s.extensionEndpoint(r.PathValue("extension"), r.Method, "/"+r.PathValue("path"), tool)
	if err != nil {
		route = ExtensionRoute{Action: "read:memories", Roles: extDefaultRoles}
	}
	policy.action, policy.roles = route.Action, route.Roles
	return policy
}

// extensionEndpoint finds what a route (or, with tool set, a tool) asks for; Roles is never empty.
func (s *Service) extensionEndpoint(extension, method, path, tool string) (ExtensionRoute, error) {
	ep := s.Extensions.Endpoints
	if ep == nil {
		return ExtensionRoute{}, ErrNotFound
	}
	if tool != "" {
		tools := ep.Tools()
		i := slices.IndexFunc(tools, func(t ExtensionTool) bool { return t.Name == tool })
		if i < 0 {
			return ExtensionRoute{}, ErrNotFound
		}
		return ExtensionRoute{Action: tools[i].Action, Roles: extDefaultRoles, Data: tools[i].Data}, nil
	}
	route, ok := ep.Route(extension, method, path)
	if !ok {
		return ExtensionRoute{}, ErrNotFound
	}
	if len(route.Roles) == 0 {
		route.Roles = extDefaultRoles
	}
	return route, nil
}

func (s *Service) authorizeExtension(ctx context.Context, token string, scope Scope, action string, roles []string) (Decision, error) {
	d, err := s.authorize(ctx, token, action, scope, "", "")
	if err == nil && !slices.Contains(roles, d.Role) {
		err = ErrDenied
	}
	return d, err
}

// CallExtensionRoute forwards one authorized request to the extension that declared the route.
func (s *Service) CallExtensionRoute(ctx context.Context, token string, call ExtensionCall) (ExtensionAnswer, error) {
	route, err := s.extensionEndpoint(call.Extension, call.Method, call.Path, "")
	if err != nil {
		return ExtensionAnswer{}, err
	}
	if call.Actor, err = s.authorizeExtension(ctx, token, call.Scope, route.Action, route.Roles); err != nil {
		return ExtensionAnswer{}, err
	}
	return s.serveExtension(ctx, token, route.Data, call)
}

// serveExtension runs one authorized call; with data, the extension reads its
// entries and its changes are applied under the caller's own permission.
func (s *Service) serveExtension(ctx context.Context, token string, data bool, call ExtensionCall) (ExtensionAnswer, error) {
	var err error
	if data {
		if call.Data, err = s.extDataSnapshot(ctx, token, call.Scope, call.Extension); err != nil {
			return ExtensionAnswer{}, err
		}
	}
	answer, err := s.Extensions.Endpoints.Serve(ctx, call)
	if err != nil || len(answer.Data) == 0 {
		return answer, err
	}
	if !data {
		return ExtensionAnswer{}, errExtFailed // changes from a call that did not declare data
	}
	if _, err := s.ChangeExtensionData(ctx, token, call.Scope, call.Extension, answer.Data); err != nil {
		return ExtensionAnswer{}, err
	}
	return answer, nil
}

// ParseMCPTools reads ARKISM_MCP_TOOLS: comma-separated tool names, empty for every tool.
func ParseMCPTools(raw string) ([]string, error) {
	var out []string
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if !spaceToolPattern.MatchString(name) || slices.Contains(out, name) || len(out) == 256 {
			return nil, fmt.Errorf("ARKISM_MCP_TOOLS: %q is not a tool name: %w", name, ErrInvalid)
		}
		out = append(out, name)
	}
	return out, nil
}

// mcpToolAllowed: a space's mcp_tools and the server's ARKISM_MCP_TOOLS each allow every tool when empty.
func (s *Service) mcpToolAllowed(space []string, name string) bool {
	return (len(space) == 0 || slices.Contains(space, name)) && (len(s.MCPTools) == 0 || slices.Contains(s.MCPTools, name))
}

// ExtensionTools lists the extension tools this space and the server allow.
func (s *Service) ExtensionTools(ctx context.Context, token string, scope Scope) ([]ExtensionTool, error) {
	if _, err := s.authorize(ctx, token, "search:memories", scope, "", ""); err != nil {
		return nil, err
	}
	out := []ExtensionTool{}
	if s.Extensions.Endpoints == nil {
		return out, nil
	}
	settings, err := s.spaceSettings(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, t := range s.Extensions.Endpoints.Tools() {
		if s.mcpToolAllowed(settings.MCPTools, t.Name) {
			out = append(out, t)
		}
	}
	return out, nil
}

// ExtensionToolCall is the body of POST /v1/extension-tools/call.
type ExtensionToolCall struct {
	Scope     Scope           `json:"scope"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallExtensionTool runs one extension tool the space and server allow. Its answer is data, never instructions.
func (s *Service) CallExtensionTool(ctx context.Context, token string, in ExtensionToolCall) (map[string]any, error) {
	name, ok := strings.CutPrefix(in.Name, "ext_")
	extension, tool, ok2 := strings.Cut(name, "_")
	if !ok || !ok2 {
		return nil, ErrNotFound
	}
	spec, err := s.extensionEndpoint(extension, "", "", in.Name)
	if err != nil {
		return nil, err
	}
	d, err := s.authorizeExtension(ctx, token, in.Scope, spec.Action, spec.Roles)
	if err != nil {
		return nil, err
	}
	settings, err := s.spaceSettings(ctx, in.Scope)
	if err != nil {
		return nil, err
	}
	if !s.mcpToolAllowed(settings.MCPTools, in.Name) {
		return nil, ErrDenied
	}
	if len(in.Arguments) == 0 {
		in.Arguments = json.RawMessage(`{}`)
	}
	answer, err := s.serveExtension(ctx, token, spec.Data, ExtensionCall{Actor: d, Scope: in.Scope, Extension: extension, Tool: tool, Body: in.Arguments})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": answer.Body, "authority": AuthorityDataOnly}, nil
}

func (s *Service) coreHTTPExtensionRoute(w http.ResponseWriter, r *http.Request) {
	call := ExtensionCall{Extension: r.PathValue("extension"), Method: r.Method, Path: "/" + r.PathValue("path"), Query: r.URL.RawQuery, Scope: scopeQuery(r)}
	if r.Method == http.MethodPost {
		var in extensionRouteBody
		if !decodeLimit(w, r, &in, extensionBodyLimit) {
			return
		}
		call.Scope, call.Body = in.Scope, in.Input
	}
	answer, err := s.CallExtensionRoute(r.Context(), bearer(r), call)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if len(answer.Body) == 0 {
		answer.Body = json.RawMessage(`{}`)
	}
	// The body is the extension's own answer: data, never instructions or an approval.
	w.Header().Set(headerContentType, "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set(extensionAuthorityHeader, AuthorityDataOnly)
	w.WriteHeader(answer.Status)
	_, _ = w.Write(answer.Body)
}

func (s *Service) coreHTTPExtensionTools(w http.ResponseWriter, r *http.Request) {
	out, err := s.ExtensionTools(r.Context(), bearer(r), scopeQuery(r))
	respond(w, map[string]any{"tools": out}, err)
}

func (s *Service) coreHTTPExtensionToolCall(w http.ResponseWriter, r *http.Request) {
	var in ExtensionToolCall
	if !decodeLimit(w, r, &in, extensionBodyLimit) {
		return
	}
	out, err := s.CallExtensionTool(r.Context(), bearer(r), in)
	respond(w, out, err)
}
