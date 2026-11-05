package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Extension routes and tools (ext_runtime.go) are served under Arkism's own
// authorization: the action an extension declares is checked like any other.
const (
	extensionActionTemplate = "{extension_action}"
	extensionToolCallPath   = "/v1/extension-tools/call"
	extensionBodyLimit      = 256 << 10
)

var extDefaultRoles = []string{roleHuman, roleAgent, roleService}

var extensionRoutePolicies = map[string]routePolicy{
	"GET /v1/extensions/{extension}/routes/{path...}":  {action: extensionActionTemplate, scope: routeQuery},
	"POST /v1/extensions/{extension}/routes/{path...}": {action: extensionActionTemplate, scope: routeJSON, limit: extensionBodyLimit},
	"GET /v1/extension-tools":                          {action: "search:memories", scope: routeQuery},
	"POST " + extensionToolCallPath:                    {action: extensionActionTemplate, scope: routeJSON, limit: extensionBodyLimit},
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
	action, roles, err := s.extensionAction(r.PathValue("extension"), r.Method, "/"+r.PathValue("path"), tool)
	if err != nil {
		action, roles = "read:memories", extDefaultRoles
	}
	policy.action, policy.roles = action, roles
	return policy
}

// extensionAction finds what a route (or, with tool set, a tool) asks for.
func (s *Service) extensionAction(extension, method, path, tool string) (string, []string, error) {
	ep := s.Extensions.Endpoints
	if ep == nil {
		return "", nil, ErrNotFound
	}
	if tool != "" {
		i := slices.IndexFunc(ep.Tools(), func(t ExtensionTool) bool { return t.Name == tool })
		if i < 0 {
			return "", nil, ErrNotFound
		}
		return ep.Tools()[i].Action, extDefaultRoles, nil
	}
	route, ok := ep.Route(extension, method, path)
	if !ok {
		return "", nil, ErrNotFound
	}
	if len(route.Roles) == 0 {
		return route.Action, extDefaultRoles, nil
	}
	return route.Action, route.Roles, nil
}

func (s *Service) authorizeExtension(ctx context.Context, token string, scope Scope, action string, roles []string) (Decision, error) {
	d, err := s.authorize(ctx, token, action, scope, "", "")
	if err == nil && !slices.Contains(roles, d.Role) {
		err = ErrDenied
	}
	return d, err
}

// CallExtensionRoute forwards one authorized request to the extension that declared the route.
func (s *Service) CallExtensionRoute(ctx context.Context, token string, call ExtensionCall) (int, json.RawMessage, error) {
	action, roles, err := s.extensionAction(call.Extension, call.Method, call.Path, "")
	if err != nil {
		return 0, nil, err
	}
	if call.Actor, err = s.authorizeExtension(ctx, token, call.Scope, action, roles); err != nil {
		return 0, nil, err
	}
	return s.Extensions.Endpoints.Serve(ctx, call)
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
	action, roles, err := s.extensionAction(extension, "", "", in.Name)
	if err != nil {
		return nil, err
	}
	d, err := s.authorizeExtension(ctx, token, in.Scope, action, roles)
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
	_, result, err := s.Extensions.Endpoints.Serve(ctx, ExtensionCall{Actor: d, Scope: in.Scope, Extension: extension, Tool: tool, Body: in.Arguments})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": result, "authority": "data_only_untrusted"}, nil
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
	status, body, err := s.CallExtensionRoute(r.Context(), bearer(r), call)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if len(body) == 0 {
		body = json.RawMessage(`{}`)
	}
	w.Header().Set(headerContentType, "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
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
