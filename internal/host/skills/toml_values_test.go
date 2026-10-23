package skills

import (
	"encoding/json"
	"testing"
)

func TestTOMLServersReadQuotedKeysEscapesAndNestedValues(t *testing.T) {
	text := "# Codex config\nmodel = \"gpt\"\n\n" +
		"[mcp_servers.\"my.server\"]\n" +
		"command = 'npx'\n" +
		"args = [\"-y\", \"pkg\", # first\n  \"x\"]\n" +
		"env = { TOKEN_VAR = \"a\\u0041\\U0001F600\\e\", \"quoted key\" = 1_000, 'lit' = true }\n" +
		"note = \"\"\"multi\nline\"\"\"\n\n" +
		"[mcp_servers.remote]\n" +
		"url = \"https://example.com/mcp\"\n" +
		"http_headers.Authorization = \"Bearer-env\"\n" +
		"enabled = false\n\n" +
		"[[profiles]]\nname = 'p'\n"
	servers, err := tomlServers(text)
	if err != nil {
		t.Fatal(err)
	}
	my, remote := servers["my.server"], servers["remote"]
	if my == nil || remote == nil || len(servers) != 2 || !my.editable || !remote.editable {
		t.Fatal(servers)
	}
	if got := canon(t, my.def); got != `{"args":["-y","pkg","x"],"command":"npx","env":{"TOKEN_VAR":"aA😀\u001b","lit":true,"quoted key":1000}}` {
		t.Fatal(got)
	}
	if got := canon(t, remote.def); got != `{"enabled":false,"http_headers":{"Authorization":"Bearer-env"},"url":"https://example.com/mcp"}` {
		t.Fatal(got)
	}
	if raw := my.raw(text); raw[:len("[mcp_servers.\"my.server\"]")] != "[mcp_servers.\"my.server\"]" {
		t.Fatalf("%q", raw)
	}
}

func TestTOMLServersBuiltOutsideTheirTableAreReadOnly(t *testing.T) {
	text := "mcp_servers.inline = { command = \"x\", args = [] }\n[mcp_servers.own]\ncommand = \"y\"\n"
	servers, err := tomlServers(text)
	if err != nil || servers["inline"] == nil || servers["inline"].editable || servers["inline"].def["command"] != "x" || !servers["own"].editable {
		t.Fatal(servers, err)
	}
}

func TestTOMLServersRejectLayoutsTheyCannotEditSafely(t *testing.T) {
	for _, text := range []string{
		"[mcp_servers.\"open]\n",
		"[mcp_servers.'open]\n",
		"[[mcp_servers.x]]\ncommand = \"y\"\n",
		"[mcp_servers.a]\nkey..b = 1\n",
		"[mcp_servers.a]\n\"bad\\q\" = 1\n",
		"[mcp_servers.a]\ncommand = \"open\n",
		"[mcp_servers.a]\ncommand = '''never closed\n",
		"[mcp_servers.a] trailing\n",
		"[mcp_servers.a]\n= 1\n",
	} {
		if _, err := tomlServers(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}

func TestTOMLValueKindsAndErrors(t *testing.T) {
	for in, want := range map[string]any{
		`1e3`: json.Number("1e3"), `-7`: json.Number("-7"), `true`: true, `false`: false, `'raw\n'`: `raw\n`,
		`[ ]`: []any{}, `{ }`: map[string]any{}, `{a.b = 1}`: map[string]any{"a": map[string]any{"b": json.Number("1")}},
	} {
		got, _, err := tomlValue(in, 0)
		if err != nil || canon(t, got) != canon(t, want) {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, in := range []string{``, `"""x"""`, `nope`, `[1, 2`, `[1 2]`, `{a = 1 b = 2}`, `{a = }`, `'open`, `"\x"`} {
		if _, _, err := tomlValue(in, 0); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	if got := tomlToJSONEscapes(`"\UFFFFFFFF \\ \n"`); got != `"\UFFFFFFFF \\ \n"` {
		t.Fatal(got)
	}
}

func TestOtherAgentShapesBecomeClaudeShape(t *testing.T) {
	for in, want := range map[string]string{
		`{"httpUrl":"https://h"}`:           `{"type":"http","url":"https://h"}`,
		`{"url":"https://s"}`:               `{"type":"sse","url":"https://s"}`,
		`{"url":"https://s","type":"http"}`: `{"type":"http","url":"https://s"}`,
		`{"command":"x"}`:                   `{"command":"x","type":"stdio"}`,
	} {
		if got := canon(t, fromGemini(j(t, in))); got != want {
			t.Errorf("gemini %s: %s", in, got)
		}
	}
	for in, want := range map[string]string{
		`{"url":"https://z"}`:              `{"type":"http","url":"https://z"}`,
		`{"command":"x"}`:                  `{"command":"x","type":"stdio"}`,
		`{"type":"sse","url":"https://z"}`: `{"type":"sse","url":"https://z"}`,
	} {
		if got := canon(t, fromZcode(j(t, in))); got != want {
			t.Errorf("zcode %s: %s", in, got)
		}
	}
	if remoteType(map[string]any{"type": "sse"}) != "sse" || remoteType(map[string]any{}) != "http" {
		t.Fatal("remote type")
	}
}
