package capture

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// OpenCode and Cursor get the Arkism MCP server (as "arkism") where they are installed. Neither
// has command hooks the capture hook can answer: OpenCode's hooks are JavaScript plugins, and
// Cursor's own hooks (~/.cursor/hooks.json) use other event names and outputs. Cursor already runs
// the Claude Code hooks in ~/.claude/settings.json by default ("Third-party hooks").

// instOpenCodeDir is OpenCode's global config folder: $XDG_CONFIG_HOME/opencode or ~/.config/opencode
// on every system (it uses xdg-basedir, Windows included).
func instOpenCodeDir(userRoot string) string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && userRoot == host.Home {
		return filepath.Join(xdg, "opencode")
	}
	return filepath.Join(userRoot, ".config", "opencode")
}

// instMoreTarget is one more agent's MCP file and how our entry goes in and comes out.
type instMoreTarget struct {
	id, home, file string
	set            func(data host.Obj, client string)
	drop           func(data host.Obj) host.Obj
}

func instMoreTargets(userRoot string) []instMoreTarget {
	oc := instOpenCodeDir(userRoot)
	return []instMoreTarget{
		{id: "opencode", home: oc, file: filepath.Join(oc, "opencode.json"),
			set: func(data host.Obj, client string) {
				servers := instChild(data, "mcp")
				servers.Set("arkism", host.Obj{{Key: "type", Value: "local"},
					{Key: "command", Value: []any{client, "windows", "mcp", "opencode"}}, {Key: "enabled", Value: true}})
				data.Set("mcp", servers)
			},
			drop: func(data host.Obj) host.Obj {
				if _, has := data.Get("mcp"); !has {
					return data
				}
				servers := instChild(data, "mcp")
				if entry, ok := servers.Get("arkism"); ok && instOursOpenCode(entry) {
					servers.Del("arkism")
					data.Set("mcp", servers)
				}
				return data
			}},
		{id: "cursor", home: filepath.Join(userRoot, ".cursor"), file: filepath.Join(userRoot, ".cursor", "mcp.json"),
			set: func(data host.Obj, client string) {
				servers := instChild(data, "mcpServers")
				servers.Set("arkism", host.Obj{{Key: "type", Value: "stdio"}, {Key: "command", Value: client},
					{Key: "args", Value: []any{"windows", "mcp", "cursor"}}})
				data.Set("mcpServers", servers)
			},
			drop: func(data host.Obj) host.Obj {
				if _, has := data.Get("mcpServers"); !has {
					return data
				}
				servers := instChild(data, "mcpServers")
				if entry, ok := servers.Get("arkism"); ok && instOursCommand(entry) {
					servers.Del("arkism")
					data.Set("mcpServers", servers)
				}
				return data
			}},
	}
}

// instOursOpenCode reports whether an OpenCode entry starts the installed client (command is a list).
func instOursOpenCode(entry any) bool {
	o, ok := entry.(host.Obj)
	if !ok {
		return false
	}
	command, _ := o.Get("command")
	list, _ := command.([]any)
	return len(list) > 0 && instClientIn(str(list[0]))
}

// instInstallMore writes the Arkism MCP entry for OpenCode and Cursor where their folder exists,
// plus OpenCode's global AGENTS.md guidance. A file it cannot read as plain JSON (OpenCode allows
// comments) is left alone and named in skipped instead of stopping the whole install.
func instInstallMore(s *instSaver, userRoot, client string) (skipped []any, err error) {
	for _, t := range instMoreTargets(userRoot) {
		if !isDir(t.home) {
			continue
		}
		data, ok := func() (data host.Obj, ok bool) {
			defer func() {
				if recover() != nil {
					ok = false
				}
			}()
			data, err := instJSON(t.file)
			if err != nil {
				return nil, false
			}
			t.set(data, client)
			return data, true
		}()
		if !ok {
			skipped = append(skipped, t.file)
			continue
		}
		if err := s.json(t.file, data); err != nil {
			return nil, err
		}
		if t.id == "opencode" {
			guide := filepath.Join(t.home, "AGENTS.md")
			before, err := instReadOr(guide, instGuideHeader+"\n")
			if err != nil {
				return nil, err
			}
			if err := s.text(guide, instNoHookGuidance(before)); err != nil {
				return nil, err
			}
		}
	}
	return skipped, nil
}

// instNoHookGuidance is the guidance for an agent without session hooks: it looks memories up itself.
func instNoHookGuidance(text string) string {
	return strings.ReplaceAll(instGuidance(text, "arkism"), "세션 훅이 넣은 기억으로 부족할 때만", "새 대화를 시작할 때 관련 기억을 먼저 조회하고, 추가 기억이 필요할 때")
}

// instUninstallMore takes out what instInstallMore wrote.
func instUninstallMore(userRoot string, editJSON func(string, func(host.Obj) host.Obj) error,
	editText func(string, func(string) (string, error)) error) []func() error {
	var steps []func() error
	for _, t := range instMoreTargets(userRoot) {
		t := t
		steps = append(steps, func() error {
			if _, err := instJSON(t.file); err != nil && exists(t.file) {
				return nil // not plain JSON: the installer skipped it too
			}
			return editJSON(t.file, t.drop)
		})
	}
	guide := filepath.Join(instOpenCodeDir(userRoot), "AGENTS.md")
	steps = append(steps, func() error {
		return editText(guide, func(text string) (string, error) { return instGuidanceRemove(text, "arkism"), nil })
	})
	return steps
}
