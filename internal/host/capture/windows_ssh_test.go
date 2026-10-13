package capture

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func TestAgentsUseTheAppsSSHAddress(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agents.json")
	t.Setenv("ARKISM_AGENT_CONNECTION", file)
	t.Setenv("ARKISM_SSH_HOST", "from-env")
	winSSHFlag = ""

	if h, p := winServerSSH(); h != "from-env" || p != 0 {
		t.Fatalf("no app file: got %s:%d", h, p)
	}
	if err := os.WriteFile(file, []byte(`{"mode":"server","ssh_host":"alex@example.com","ssh_port":2222}`), 0o600); err != nil {
		t.Fatal(err)
	}
	argv, err := winSSHCommand("codex", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(argv, "-p"); i < 0 || argv[i+1] != "2222" || argv[len(argv)-2] != "alex@example.com" {
		t.Fatalf("argv %v", argv)
	}
	key := filepath.Join(t.TempDir(), "id_ed25519")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	_ = os.WriteFile(file, []byte(`{"mode":"server","ssh_host":"h","ssh_key":`+strconv.Quote(key)+`}`), 0o600)
	if argv, _ := winSSHCommand("codex", "mcp"); !slices.Contains(argv, key) || !slices.Contains(argv, "IdentitiesOnly=yes") {
		t.Fatalf("key not passed: %v", argv)
	}
	_ = os.WriteFile(file, []byte(`{"mode":"server","ssh_host":"h","ssh_key":"/no/such"}`), 0o600)
	if argv, _ := winSSHCommand("codex", "mcp"); slices.Contains(argv, "-i") {
		t.Fatalf("missing key must be dropped: %v", argv)
	}
	for _, bad := range []string{`{"mode":"server","ssh_host":"-oProxyCommand=x"}`, `{"mode":"server","ssh_host":"a b"}`, `{"mode":"local","ssh_host":"x"}`} {
		_ = os.WriteFile(file, []byte(bad), 0o600)
		if h, _ := winServerSSH(); h != "from-env" {
			t.Fatalf("%s: got %s", bad, h)
		}
	}
	winSSHFlag = "flag-host"
	defer func() { winSSHFlag = "" }()
	if h, _ := winServerSSH(); h != "flag-host" {
		t.Fatalf("flag must win, got %s", h)
	}
}
