package capture

// Undoing install-arkism-windows: every hook, MCP entry and guidance section the installer wrote
// is taken out again, and nothing else is touched. Each changed file is backed up first, in the
// same backups/<time>/manifest.json layout the installer uses.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// instGuideHeader is the first line the installer writes into a guide file it creates.
const instGuideHeader = "# Arkism 중앙 연결"

// remove backs a file up and deletes it.
func (s *instSaver) remove(path string) error {
	if !exists(path) {
		return nil
	}
	saved := filepath.Join(s.backup, strconv.Itoa(len(s.manifest)))
	if err := instCopy(path, saved); err != nil {
		return err
	}
	s.manifest.Set(path, saved)
	if err := os.WriteFile(filepath.Join(s.backup, "manifest.json"), instText(instDumps(s.manifest)), 0o666); err != nil {
		return err
	}
	return os.Remove(path)
}

// instOursCommand reports whether an MCP entry starts the installed Arkism client.
func instOursCommand(entry any) bool {
	o, ok := entry.(host.Obj)
	if !ok {
		return false
	}
	command, _ := o.Get("command")
	return instClientIn(str(command))
}

// instClientIn reports whether text runs the installed client (Windows, macOS or Linux).
func instClientIn(text string) bool {
	text = filepath.ToSlash(strings.ToLower(text))
	return strings.Contains(text, "arkism.exe") || strings.Contains(text, instUnixClient)
}

// instGuidanceRemove drops the sections instGuidance adds and keeps every other line.
func instGuidanceRemove(text, server string) string {
	for _, kv := range instSections(server) {
		header := regexp.MustCompile(`^## ` + regexp.QuoteMeta(kv.Key) + `[ \t]*$`)
		var out strings.Builder
		skipping := false
		for _, line := range strings.SplitAfter(text, "\n") {
			body := strings.TrimSuffix(line, "\n")
			if skipping && instHeadingStop.MatchString(body) {
				skipping = false
			}
			if !skipping && strings.HasSuffix(line, "\n") && header.MatchString(body) {
				skipping = true
				continue
			}
			if !skipping {
				out.WriteString(line)
			}
		}
		text = out.String()
	}
	if t := pyRstrip(text); t != "" {
		return t + "\n"
	}
	return ""
}

// instCodexRemove takes out the [mcp_servers.skill-router] table the installer wrote; a table of
// that name that starts something else is left alone.
func instCodexRemove(text string) (string, error) {
	sections, named, err := instServers(text)
	if err != nil {
		return "", err
	}
	if !named["skill-router"] {
		return text, nil
	}
	span, ok := sections["skill-router"]
	if !ok {
		return "", errInstTOML
	}
	if !instClientIn(text[span[0]:span[1]]) {
		return text, nil
	}
	text = pyRstrip(text[:span[0]]) + "\n" + strings.TrimLeft(text[span[1]:], "\n")
	if t := pyRstrip(text); t != "" {
		return t + "\n", nil
	}
	return "", nil
}

// instDropOurHooks removes the installer's handlers from every event of a hooks object.
func instDropOurHooks(hooks *host.Obj) {
	events := make([]string, 0, len(*hooks))
	for _, kv := range *hooks {
		events = append(events, kv.Key)
	}
	for _, event := range events {
		instMergeHooks(hooks, event, nil)
	}
}

func instUninstall(userRoot, roaming, local string) (summary host.Obj, err error) {
	defer recovered(&err)
	if userRoot == "" {
		userRoot = host.Home
	}
	if roaming == "" {
		roaming = instRoaming(userRoot)
	}
	if local == "" && runtime.GOOS == "windows" {
		local = os.Getenv("LOCALAPPDATA")
	}
	folder := filepath.Join(userRoot, ".config", "arkism")
	now := time.Now().UTC()
	backup := filepath.Join(folder, "backups", now.Format("20060102T150405")+fmt.Sprintf("%06dZ", now.Nanosecond()/1000)+"-remove")
	if err := os.MkdirAll(backup, 0o777); err != nil {
		return nil, err
	}
	s := &instSaver{backup: backup, manifest: host.Obj{}}

	// JSON files: only write the ones that change.
	editJSON := func(path string, edit func(data host.Obj) host.Obj) error {
		if !exists(path) {
			return nil
		}
		data, err := instJSON(path)
		if err != nil {
			return err
		}
		before := instDumps(data)
		data = edit(data)
		if instDumps(data) == before {
			return nil
		}
		return s.json(path, data)
	}
	editText := func(path string, edit func(string) (string, error)) error {
		if !exists(path) {
			return nil
		}
		before, err := instRead(path)
		if err != nil {
			return err
		}
		after, err := edit(before)
		if err != nil || after == before {
			return err
		}
		if strings.TrimSpace(after) == "" || strings.TrimSpace(after) == instGuideHeader {
			return s.remove(path)
		}
		return s.text(path, after)
	}
	dropHooks := func(data host.Obj) host.Obj {
		if _, has := data.Get("hooks"); !has {
			return data
		}
		hooks := instChild(data, "hooks")
		instDropOurHooks(&hooks)
		if len(hooks) == 0 {
			data.Del("hooks")
		} else {
			data.Set("hooks", hooks)
		}
		return data
	}
	dropServer := func(key, name string) func(host.Obj) host.Obj {
		return func(data host.Obj) host.Obj {
			if _, has := data.Get(key); !has {
				return data
			}
			servers := instChild(data, key)
			if entry, ok := servers.Get(name); ok && instOursCommand(entry) {
				servers.Del(name)
				data.Set(key, servers)
			}
			return data
		}
	}

	codex := filepath.Join(userRoot, instCodex)
	steps := []func() error{
		func() error { return editJSON(filepath.Join(codex, "hooks.json"), dropHooks) },
		func() error { return editText(filepath.Join(codex, "config.toml"), instCodexRemove) },
		func() error { return editJSON(filepath.Join(userRoot, ".claude", "settings.json"), dropHooks) },
		func() error {
			return editJSON(filepath.Join(userRoot, ".claude.json"), dropServer("mcpServers", "skill-router"))
		},
		func() error {
			return editJSON(filepath.Join(userRoot, instGemini, "settings.json"), dropServer("mcpServers", "skill-router"))
		},
		func() error { return editText(filepath.Join(userRoot, ".grok", "config.toml"), instCodexRemove) },
		func() error {
			return editJSON(filepath.Join(userRoot, ".zcode", "cli", "config.json"), func(data host.Obj) host.Obj {
				if _, has := data.Get("mcp"); has {
					mcp := instChild(data, "mcp")
					if _, has := mcp.Get("servers"); has {
						servers := instChild(mcp, "servers")
						if entry, ok := servers.Get("arkism"); ok && instOursCommand(entry) {
							servers.Del("arkism")
							mcp.Set("servers", servers)
							data.Set("mcp", mcp)
						}
					}
				}
				if _, has := data.Get("hooks"); has {
					hooks := instChild(data, "hooks")
					if _, has := hooks.Get("events"); has {
						events := instChild(hooks, "events")
						instDropOurHooks(&events)
						hooks.Set("events", events)
						data.Set("hooks", hooks)
					}
				}
				return data
			})
		},
	}
	steps = append(steps, instUninstallMore(userRoot, editJSON, editText)...)
	desktops := []string{}
	if roaming != "" {
		desktops = append(desktops, filepath.Join(roaming, "Claude", "claude_desktop_config.json"))
	}
	if local != "" {
		found, _ := filepath.Glob(filepath.Join(globEscape(filepath.Join(local, "Packages")), "Claude*", "LocalCache", "Roaming", "Claude", "claude_desktop_config.json"))
		desktops = append(desktops, found...)
	}
	for _, path := range desktops {
		path := path
		steps = append(steps, func() error { return editJSON(path, dropServer("mcpServers", "arkism")) })
	}
	// Guidance: the same files the installer may have written, including an AGENTS.override.md.
	guides := [][2]string{{filepath.Join(codex, "AGENTS.md"), "skill-router"}, {filepath.Join(codex, "AGENTS.override.md"), "skill-router"},
		{filepath.Join(userRoot, ".claude", "CLAUDE.md"), "skill-router"}, {filepath.Join(userRoot, ".zcode", "AGENTS.md"), "arkism"},
		{filepath.Join(userRoot, instGemini, "GEMINI.md"), "skill-router"}, {filepath.Join(userRoot, ".dsh", "AGENTS.md"), "arkism"}}
	for _, g := range guides {
		g := g
		steps = append(steps, func() error {
			return editText(g[0], func(text string) (string, error) { return instGuidanceRemove(text, g[1]), nil })
		})
	}
	profiles, _ := filepath.Glob(filepath.Join(globEscape(filepath.Join(userRoot, ".dsh", "profiles")), "*", "cordis.patch.yml"))
	for _, path := range profiles {
		path := path
		steps = append(steps, func() error {
			return editText(path, func(text string) (string, error) {
				out := pyRstrip(instDeepseekBlock.ReplaceAllString(text, ""))
				if out == "" {
					return "[]\n", nil
				}
				return out + "\n", nil
			})
		})
	}
	steps = append(steps, func() error { return s.remove(filepath.Join(folder, "hook-codex.cmd")) })
	// The PC's scheduled tasks (tools/install_arkism_*_task.ps1) run files in this folder; without
	// them Windows shows a "script not found" box every few minutes.
	var tasks []string
	steps = append(steps, func() error { tasks = instRemoveTasks(); return nil })
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	files := make([]any, len(s.manifest))
	for i, kv := range s.manifest {
		files[i] = kv.Key
	}
	if len(files) == 0 {
		os.Remove(backup)
	}
	return host.Obj{{Key: "removed", Value: true}, {Key: "backup", Value: backup}, {Key: "files", Value: files},
		{Key: "scheduled_tasks", Value: stringsAnyCapture(tasks)}}, nil
}

// instTaskNames are the scheduled tasks the PC scripts register.
var instTaskNames = []string{"Arkism-Work-Drain-claude_code", "Arkism-Work-Drain-codex", "Arkism-Work-Drain-zcode", "Arkism-Zcode-Export"}

// instRemoveTasks deletes those tasks; tests replace it. Off Windows there are none.
var instRemoveTasks = func() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var removed []string
	for _, name := range instTaskNames {
		cmd := exec.Command("schtasks", "/Delete", "/TN", name, "/F")
		detach(cmd)
		if cmd.Run() == nil {
			removed = append(removed, name)
		}
	}
	return removed
}

func stringsAnyCapture(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func uninstMain(_ []string, _ io.Reader, stdout, stderr io.Writer) int {
	summary, err := instUninstall("", "", "")
	if err != nil {
		fmt.Fprintln(stderr, "arkism uninstall-arkism:", err)
		return 1
	}
	fmt.Fprintln(stdout, host.MustDumps(summary, host.DumpOpts{NoASCII: true}))
	return 0
}

func init() {
	host.Register("uninstall-arkism", uninstMain)
	host.Register("uninstall-arkism-windows", uninstMain)
}
