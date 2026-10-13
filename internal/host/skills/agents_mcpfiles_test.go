package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mcpHome points every agent path and the stash at a temp home; the Claude and Codex readers are stubbed.
func mcpHome(t *testing.T, claude map[string]any) string {
	t.Helper()
	home := t.TempDir()
	h, st, cs, xs := agentsHome, agentsState, claudeServers, codexServers
	agentsHome, agentsState = home, filepath.Join(home, "state")
	claudeServers = func() map[string]any { return claude }
	codexServers = func() map[string]any { return map[string]any{} }
	t.Cleanup(func() { agentsHome, agentsState, claudeServers, codexServers = h, st, cs, xs })
	fakeAgentRun(t)
	return home
}

func mcpRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mcpSwitch(agent, name string, on bool, stash map[string]any) error {
	return applyRequest(map[string]any{"agent": agent, "kind": "mcp", "name": name, "enabled": on}, stash)
}

const geminiSettings = `{
  "theme": "GitHub",
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@github/mcp"]
    },
    "wake": {
      "command": "wake-mcp",
      "args": [
        "--db",
        "x"
      ],
      "env": {
        "Z": "1",
        "A": "2"
      }
    }
  },
  "selectedAuthType": "oauth-personal"
}
`

const zcodeConfig = `{
  "model": "glm-4.6",
  "mcp": {
    "enabled": true,
    "servers": {
      "github": {"type": "stdio", "command": "npx", "args": ["-y", "@github/mcp"]},
      "wake": {
        "type": "stdio",
        "command": "wake-mcp",
        "args": [
          "--db",
          "x"
        ]
      }
    }
  }
}
`

const grokConfig = `model = "grok-4"

[mcp_servers.github]
command = "npx"
args = ["-y", "@github/mcp"]

# keep this note
[ui]
theme = "dark"

[mcp_servers.wake]
command = "wake-mcp" # local
args = ["--db", "x"]

[mcp_servers.wake.env]
A = "2"
`

func TestFileAgentsSwitchOffIntoStashAndBackExactly(t *testing.T) {
	for _, c := range []struct{ agent, file, text string }{
		{"gemini", ".gemini/settings.json", geminiSettings},
		{"zcode", ".zcode/cli/config.json", zcodeConfig},
		{"grok", ".grok/config.toml", grokConfig},
	} {
		t.Run(c.agent, func(t *testing.T) {
			home := mcpHome(t, map[string]any{})
			p := filepath.Join(home, c.file)
			write(t, p, c.text)
			stash := map[string]any{}
			if err := mcpSwitch(c.agent, "wake", false, stash); err != nil {
				t.Fatal(err)
			}
			off := mcpRead(t, p)
			if strings.Contains(off, "wake-mcp") || !strings.Contains(off, "@github/mcp") {
				t.Fatal(off)
			}
			saved := asMap(orMap(loadStash()[c.agent])["wake"])
			if saved["command"] != "wake-mcp" || !strings.Contains(str(saved["_raw"]), "wake-mcp") {
				t.Fatal(saved)
			}
			// Everything else is byte-for-byte what it was: only the entry's own text is gone.
			if c.agent != "grok" && off != strings.Replace(c.text, c.text[strings.LastIndex(c.text[:strings.Index(c.text, `"wake"`)], ","):strings.Index(c.text, `"wake"`)]+wakeJSON(c.text), "", 1) {
				t.Fatalf("%q", off)
			}
			if c.agent == "grok" && off != strings.Replace(grokConfig, "[mcp_servers.wake]\ncommand = \"wake-mcp\" # local\nargs = [\"--db\", \"x\"]\n\n[mcp_servers.wake.env]\nA = \"2\"\n", "", 1) {
				t.Fatalf("%q", off)
			}
			if err := mcpSwitch(c.agent, "wake", false, loadStash()); err != nil { // already off: nothing to do
				t.Fatal(err)
			}
			if err := mcpSwitch(c.agent, "wake", true, loadStash()); err != nil {
				t.Fatal(err)
			}
			if got := mcpRead(t, p); got != c.text {
				t.Fatalf("restored %q", got)
			}
			if _, left := orMap(loadStash()[c.agent])["wake"]; left {
				t.Fatal("still stashed")
			}
		})
	}
}

// wakeJSON is the "wake" member's text through the end of its value.
func wakeJSON(text string) string {
	i := strings.Index(text, `"wake"`)
	end, _ := jsEnd(text, jsWS(text, strings.Index(text[i:], ":")+i+1))
	return text[i:end]
}

func TestFileAgentsRemoveAMiddleEntryAndKeepTheRest(t *testing.T) {
	home := mcpHome(t, map[string]any{})
	p := filepath.Join(home, ".gemini/settings.json")
	write(t, p, geminiSettings)
	if err := mcpSwitch("gemini", "github", false, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(geminiSettings, "\"github\": {\n      \"command\": \"npx\",\n      \"args\": [\"-y\", \"@github/mcp\"]\n    },\n    ", "", 1)
	if got := mcpRead(t, p); got != want {
		t.Fatalf("%q", got)
	}
	p = filepath.Join(home, ".grok/config.toml")
	write(t, p, grokConfig)
	if err := mcpSwitch("grok", "github", false, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := mcpRead(t, p); got != strings.Replace(grokConfig, "[mcp_servers.github]\ncommand = \"npx\"\nargs = [\"-y\", \"@github/mcp\"]\n\n", "", 1) {
		t.Fatalf("%q", got)
	}
}

func TestFileAgentsTakeAnotherAgentsDefinition(t *testing.T) {
	claude := map[string]any{
		"cf":   map[string]any{"type": "http", "url": "https://mcp.cloudflare.com/mcp"},
		"wake": map[string]any{"type": "stdio", "command": "wake-mcp", "args": []any{"--db", "x"}, "env": map[string]any{"A": "1"}},
	}
	for _, c := range []struct{ agent, file, text, want string }{
		{"gemini", ".gemini/settings.json", "{\n  \"theme\": \"x\"\n}\n",
			"{\n  \"theme\": \"x\",\n  \"mcpServers\": {\n    \"cf\": {\n      \"httpUrl\": \"https://mcp.cloudflare.com/mcp\"\n    }\n  }\n}\n"},
		{"gemini", ".gemini/settings.json", "",
			"{\n  \"mcpServers\": {\n    \"cf\": {\n      \"httpUrl\": \"https://mcp.cloudflare.com/mcp\"\n    }\n  }\n}\n"},
		{"zcode", ".zcode/cli/config.json", "{\"mcp\": {\"servers\": {}}}",
			"{\"mcp\": {\"servers\": {\n  \"cf\": {\n    \"type\": \"http\",\n    \"url\": \"https://mcp.cloudflare.com/mcp\"\n  }\n}}}"},
		{"grok", ".grok/config.toml", "model = \"grok-4\"\n",
			"model = \"grok-4\"\n\n[mcp_servers.cf]\nurl = \"https://mcp.cloudflare.com/mcp\"\n"},
	} {
		t.Run(c.agent, func(t *testing.T) {
			home := mcpHome(t, claude)
			p := filepath.Join(home, c.file)
			os.MkdirAll(filepath.Join(home, strings.Split(c.file, "/")[0]), 0o755)
			if c.text != "" {
				write(t, p, c.text)
			}
			if err := mcpSwitch(c.agent, "cf", true, map[string]any{}); err != nil {
				t.Fatal(err)
			}
			if got := mcpRead(t, p); got != c.want {
				t.Fatalf("%q", got)
			}
		})
	}
	home := mcpHome(t, claude)
	write(t, filepath.Join(home, ".grok/config.toml"), "")
	if err := mcpSwitch("grok", "wake", true, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := mcpRead(t, filepath.Join(home, ".grok/config.toml")); got != "[mcp_servers.wake]\ncommand = \"wake-mcp\"\nargs = [\"--db\", \"x\"]\nenv = { A = \"1\" }\n" {
		t.Fatalf("%q", got)
	}
	if srv := fileAgentServers()["grok"]["wake"]; canon(t, srv) != `{"_enabled":true,"args":["--db","x"],"command":"wake-mcp","env":{"A":"1"},"type":"stdio"}` {
		t.Fatal(canon(t, srv))
	}
}

func TestAbsentAgentFoldersAreLeftAlone(t *testing.T) {
	for _, a := range mcpFileAgents {
		home := mcpHome(t, map[string]any{"wake": map[string]any{"command": "wake-mcp"}})
		if err := mcpSwitch(a.id, "wake", false, map[string]any{}); err != nil {
			t.Fatal(a.id, err)
		}
		if err := mcpSwitch(a.id, "wake", true, map[string]any{}); err == nil {
			t.Fatal(a.id, "switched on without the agent")
		}
		if exists(filepath.Join(home, a.home)) {
			t.Fatal(a.id, "folder created")
		}
	}
}

func TestMalformedFilesAreRefusedUntouched(t *testing.T) {
	for _, c := range []struct{ agent, file, text string }{
		{"gemini", ".gemini/settings.json", "{\n  \"mcpServers\": {\"wake\": {\"command\": \"wake-mcp\"},}\n}\n"},                 // trailing comma
		{"gemini", ".gemini/settings.json", "{\"mcpServers\": {\"wake\": {\"command\": \"a\"}, \"wake\": {\"command\": \"b\"}}}"}, // repeated name
		{"zcode", ".zcode/cli/config.json", "{\"mcp\": {\"servers\": [\"wake\"]}}"},
		{"zcode", ".zcode/cli/config.json", "[1, 2]"},
		{"grok", ".grok/config.toml", "mcp_servers = { wake = { command = \"wake-mcp\" } }\n"},
		{"grok", ".grok/config.toml", "mcp_servers.wake.command = \"wake-mcp\"\n"},
		{"grok", ".grok/config.toml", "[mcp_servers.wake\ncommand = \"wake-mcp\"\n"},
		{"grok", ".grok/config.toml", "[[mcp_servers]]\nname = \"wake\"\n"},
	} {
		home := mcpHome(t, map[string]any{"other": map[string]any{"command": "x"}})
		p := filepath.Join(home, c.file)
		write(t, p, c.text)
		for _, on := range []bool{false, true} {
			name := "wake"
			if on {
				name = "other"
			}
			err := mcpSwitch(c.agent, name, on, map[string]any{})
			if err == nil {
				t.Fatal(c.agent, c.text, on, "accepted")
			}
			if got := mcpRead(t, p); got != c.text {
				t.Fatal(c.agent, "file changed", got)
			}
			if len(orMap(loadStash()[c.agent])) != 0 {
				t.Fatal(c.agent, "stashed")
			}
		}
	}
}

func TestServerListReportsTheFileAgents(t *testing.T) {
	old := claudeNeedsLogin
	claudeNeedsLogin = func(string, map[string]any) bool { return false }
	t.Cleanup(func() { claudeNeedsLogin = old })
	home := mcpHome(t, map[string]any{})
	write(t, filepath.Join(home, ".gemini/settings.json"), geminiSettings)
	write(t, filepath.Join(home, ".grok/config.toml"), grokConfig+"\n[mcp_servers.off]\ncommand = \"x\"\nenabled = false\n")
	others := fileAgentServers()
	e := serverEntry("wake", asMap(others["gemini"]["wake"]), map[string]any{}, map[string]any{}, others)
	if got := canon(t, objGet(e, "agents")); got != `{"claude_code":"off","codex":"off","cursor":"off","gemini":"on","grok":"on","opencode":"off","zcode":"off"}` {
		t.Fatal(got)
	}
	if objGet(e, "target") != "wake-mcp x" || objGet(e, "transport") != "stdio" {
		t.Fatal(e)
	}
	e = serverEntry("off", asMap(others["grok"]["off"]), map[string]any{}, map[string]any{}, others)
	if got := canon(t, objGet(e, "agents")); got != `{"claude_code":"off","codex":"off","cursor":"off","gemini":"off","grok":"off","opencode":"off","zcode":"off"}` {
		t.Fatal(got)
	}
}

func TestAddServerWritesGeminiZcodeAndGrok(t *testing.T) {
	home := mcpHome(t, map[string]any{})
	for _, d := range []string{".gemini", ".zcode/cli", ".grok"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"theme": "dark"}`+"\n"), 0o644)
	for _, agent := range []string{"gemini", "zcode", "grok"} {
		if err := addServer(map[string]any{"agent": agent, "name": "docs", "url": "https://docs.example/mcp"}); err != nil {
			t.Fatal(agent, err)
		}
		if err := addServer(map[string]any{"agent": agent, "name": "docs", "url": "https://docs.example/mcp"}); err == nil {
			t.Fatal(agent, "added twice")
		}
		if err := addServer(map[string]any{"agent": agent, "name": "fs", "command": []any{"npx", "-y", "fs-mcp"}}); err != nil {
			t.Fatal(agent, err)
		}
	}
	gem := mcpRead(t, filepath.Join(home, ".gemini", "settings.json"))
	if !strings.Contains(gem, `"theme": "dark"`) || !strings.Contains(gem, `"docs"`) || !strings.Contains(gem, `"npx"`) {
		t.Fatal(gem)
	}
	z := mcpRead(t, filepath.Join(home, ".zcode", "cli", "config.json"))
	g := mcpRead(t, filepath.Join(home, ".grok", "config.toml"))
	if !strings.Contains(z, `"docs"`) || !strings.Contains(g, "[mcp_servers.docs]") || !strings.Contains(g, "[mcp_servers.fs]") {
		t.Fatal(z, g)
	}
	if err := addServer(map[string]any{"agent": "gemini", "name": "x", "url": "https://x"}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(home, ".grok"))
	if err := addServer(map[string]any{"agent": "grok", "name": "y", "url": "https://y"}); err == nil {
		t.Fatal("absent agent written")
	}
}
