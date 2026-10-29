package capture

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func approvedSSHProfile(t *testing.T, file string) winLocalConn {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	root := filepath.Join(filepath.Dir(file), "ssh-hosts")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, fmt.Sprintf("%s.%x.known_hosts", id, sha256.Sum256(key.Marshal())))
	c := winLocalConn{Mode: "server", SSHHost: "alex@example.com", SSHPort: 2222, SSHKnownHosts: path, SSHHostAlias: "arkism-" + id, SSHHostAlgorithms: "ssh-ed25519"}
	if err := os.WriteFile(path, append([]byte(c.SSHHostAlias+" "), ssh.MarshalAuthorizedKey(key)...), 0600); err != nil {
		t.Fatal(err)
	}
	writeSSHProfile(t, file, c)
	return c
}
func writeSSHProfile(t *testing.T, file string, c winLocalConn) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestAgentsUseTheAppsApprovedSSHKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), "with spaces % store", "agents.json")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_AGENT_CONNECTION", file)
	t.Setenv("ARKISM_SSH_HOST", "from-env")
	winSSHFlag = ""
	if h, p := winServerSSH(); h != "from-env" || p != 0 {
		t.Fatalf("got %s:%d", h, p)
	}
	c := approvedSSHProfile(t, file)
	c.SSHKey = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(c.SSHKey, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSSHProfile(t, file, c)
	argv, err := winSSHCommand("codex", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(argv, "-p"); i < 0 || argv[i+1] != "2222" || argv[len(argv)-2] != c.SSHHost {
		t.Fatalf("argv %v", argv)
	}
	for _, required := range []string{c.SSHKey, "IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "HostKeyAlias=" + c.SSHHostAlias, "GlobalKnownHostsFile=none", "KnownHostsCommand=none", "ControlPath=none"} {
		if !slices.Contains(argv, required) {
			t.Errorf("missing %s", required)
		}
	}
	if !slices.Contains(argv, "UserKnownHostsFile="+sshConfigPath(filepath.ToSlash(c.SSHKnownHosts))) {
		t.Fatal("pin path was not quoted/escaped")
	}
	if got := sshConfigPath("C:/Users/a b/100%/kh"); got != `"C:/Users/a b/100%%/kh"` {
		t.Fatalf("spaced path %s", got)
	}
	if got := sshConfigPath("C:/Users/ab/kh"); got != "C:/Users/ab/kh" {
		t.Fatalf("plain path %s", got)
	}
	if strings.Contains(strings.Join(argv, " "), "accept-new") {
		t.Fatal("TOFU enabled")
	}
	// Modifying the key cannot turn the profile into a new approval.
	if err := os.WriteFile(c.SSHKnownHosts, []byte("attacker key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := winSSHCommand("codex", "mcp"); err == nil {
		t.Fatal("modified pin accepted")
	}
}
func TestAgentsRejectLegacyOrRedirectedApprovals(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agents.json")
	t.Setenv("ARKISM_AGENT_CONNECTION", file)
	winSSHFlag = ""
	c := approvedSSHProfile(t, file)
	for _, mutate := range []func(*winLocalConn){
		func(c *winLocalConn) { c.SSHKnownHosts = "" },
		func(c *winLocalConn) { c.SSHHostAlgorithms = "+ssh-dss" },
		func(c *winLocalConn) { c.SSHHostAlias = "*" },
		func(c *winLocalConn) { c.SSHHost = "-oProxyCommand=x" },
		func(c *winLocalConn) { c.SSHKnownHosts = filepath.Join(filepath.Dir(file), "outside.known_hosts") },
	} {
		bad := c
		mutate(&bad)
		writeSSHProfile(t, file, bad)
		if _, err := winSSHCommand("codex", "mcp"); err == nil {
			t.Fatal("invalid approval accepted")
		}
	}
	winSSHFlag = "flag-host"
	defer func() { winSSHFlag = "" }()
	argv, err := winSSHCommand("codex", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if argv[len(argv)-2] != "flag-host" || !slices.Contains(argv, "StrictHostKeyChecking=yes") {
		t.Fatalf("unsafe explicit SSH: %v", argv)
	}
}
