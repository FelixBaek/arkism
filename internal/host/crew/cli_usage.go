package crew

import "github.com/FelixBaek/arkism/internal/host"

// cliUsage is what one `claude -p --output-format json` call used (host.CLIUsage).
func cliUsage(res map[string]any, requested string) host.Obj { return host.CLIUsage(res, requested) }

// withUsage appends a "usage" field to payload when the call reported any.
func withUsage(payload, usage host.Obj) host.Obj {
	if usage == nil {
		return payload
	}
	return append(payload, host.KV{Key: "usage", Value: usage})
}
