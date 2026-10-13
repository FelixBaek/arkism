package skills

import "github.com/FelixBaek/arkism/internal/host"

// OpenCode and Cursor keep their MCP servers in a JSON file like Gemini CLI and ZCode do, so they
// are file-edited agents too (mcpFileAgents). Here: their two entry formats, and their ids on the
// lists that name the agents the skill router and the on/off switches know.

func init() {
	for _, a := range mcpFileAgents {
		if _, ok := switches[a.id]; !ok {
			switches[a.id] = a.switchMCP
		}
	}
	for _, id := range []string{"opencode", "cursor"} {
		if !containsStr(routerRuntimes, id) {
			routerRuntimes = append(routerRuntimes, id)
		}
		if !containsStr(pcOrigins, id) {
			pcOrigins = append(pcOrigins, id)
		}
	}
}

// OpenCode (~/.config/opencode/opencode.json, "mcp"): {"type": "local", "command": [program, args…],
// "environment", "enabled"} or {"type": "remote", "url", "headers", "enabled"}.
func fromOpenCode(o map[string]any) map[string]any {
	d := map[string]any{"_enabled": getOr(o, "enabled", true)}
	if o["type"] == "remote" || !truthy(o["command"]) && truthy(o["url"]) {
		d["type"], d["url"] = "http", o["url"]
		if truthy(o["headers"]) {
			d["headers"] = o["headers"]
		}
		return d
	}
	d["type"] = "stdio"
	if cmd := asList(o["command"]); len(cmd) > 0 {
		d["command"], d["args"] = cmd[0], append([]any{}, cmd[1:]...)
	}
	if truthy(o["environment"]) {
		d["env"] = o["environment"]
	}
	if truthy(o["cwd"]) {
		d["cwd"] = o["cwd"]
	}
	return d
}

func toOpenCode(d map[string]any) host.Obj {
	if isStdio(d) {
		cmd := append([]any{d["command"]}, asList(d["args"])...)
		o := host.Obj{{Key: "type", Value: "local"}, {Key: "command", Value: cmd}}
		if truthy(d["env"]) {
			o.Set("environment", d["env"])
		}
		setIf(&o, d, "cwd")
		o.Set("enabled", true)
		return o
	}
	o := host.Obj{{Key: "type", Value: "remote"}, {Key: "url", Value: d["url"]}}
	setIf(&o, d, "headers")
	o.Set("enabled", true)
	return o
}

// Cursor (~/.cursor/mcp.json, "mcpServers"): Claude's shape — {"type": "stdio", "command", "args",
// "env"} or {"url", "headers"} for a remote server.
func fromCursor(c map[string]any) map[string]any {
	d := copyExcept(c)
	if !truthy(d["type"]) {
		d["type"] = "stdio"
		if !truthy(d["command"]) && truthy(d["url"]) {
			d["type"] = "http"
		}
	}
	return d
}

func toCursor(d map[string]any) host.Obj {
	if isStdio(d) {
		o := host.Obj{{Key: "type", Value: "stdio"}, {Key: "command", Value: d["command"]}}
		setIf(&o, d, "args", "env", "cwd")
		return o
	}
	o := host.Obj{{Key: "url", Value: d["url"]}}
	setIf(&o, d, "headers")
	return o
}
