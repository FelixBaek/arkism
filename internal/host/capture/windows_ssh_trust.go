package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// App-managed agents share the exact approved host key with the desktop. Legacy
// profiles need a reconnect in the app; a missing pin never falls back to TOFU.
func winSSHTrust(selectedHost string) ([]string, error) {
	options := []string{"-o", "StrictHostKeyChecking=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ControlPersist=no", "-o", "ForwardAgent=no", "-o", "PermitLocalCommand=no", "-o", "ClearAllForwardings=yes"}
	if winSSHFlag != "" {
		return options, nil
	} // explicit CLI choice uses strict system known_hosts
	data, err := os.ReadFile(winLocalConnPath())
	if os.IsNotExist(err) {
		return options, nil
	}
	if err != nil {
		return nil, valueErr("cannot read SSH connection profile")
	}
	var c winLocalConn
	if json.Unmarshal(data, &c) != nil || c.Mode != "server" || c.SSHHost != selectedHost {
		return nil, valueErr("invalid SSH connection profile; reconnect in Arkism")
	}
	return winApprovedSSHOptions(options, c)
}

func sshApprovalError() error { return valueErr("SSH host key approval required; reconnect in Arkism") }

func winApprovedSSHOptions(options []string, c winLocalConn) ([]string, error) {
	id, err := sshApprovalPath(c)
	if err != nil {
		return nil, err
	}
	if err := sshOrdinaryPinPath(c.SSHKnownHosts); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(c.SSHKnownHosts)
	if err != nil || len(body) > 32768 {
		return nil, sshApprovalError()
	}
	if err := sshApprovedKey(c, id, body); err != nil {
		return nil, err
	}
	path := filepath.ToSlash(c.SSHKnownHosts)
	if strings.ContainsAny(path, "\"\r\n\x00") || strings.Contains(path, "${") {
		return nil, sshApprovalError()
	}
	path = strings.ReplaceAll(path, "%", "%%")
	return append(options, "-o", "UserKnownHostsFile=\""+path+"\"", "-o", "HostKeyAlias="+c.SSHHostAlias, "-o", "HostKeyAlgorithms="+c.SSHHostAlgorithms, "-o", "GlobalKnownHostsFile=none", "-o", "KnownHostsCommand=none", "-o", "UpdateHostKeys=no", "-o", "VerifyHostKeyDNS=no", "-o", "CheckHostIP=no"), nil
}

func sshApprovalPath(c winLocalConn) (string, error) {
	if !filepath.IsAbs(c.SSHKnownHosts) || !strings.HasPrefix(c.SSHHostAlias, "arkism-") {
		return "", sshApprovalError()
	}
	id := strings.TrimPrefix(c.SSHHostAlias, "arkism-")
	if len(id) != 64 || strings.ToLower(id) != id {
		return "", sshApprovalError()
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", sshApprovalError()
	}
	root := filepath.Join(filepath.Dir(winLocalConnPath()), "ssh-hosts")
	if filepath.Clean(filepath.Dir(c.SSHKnownHosts)) != filepath.Clean(root) {
		return "", sshApprovalError()
	}
	return id, nil
}

func sshOrdinaryPinPath(pin string) error {
	// Refuse redirected stores and files, including a changed parent directory.
	for path := pin; ; path = filepath.Dir(path) {
		st, err := os.Lstat(path)
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return sshApprovalError()
		}
		if path == pin && (!st.Mode().IsRegular() || st.Size() > 32768) {
			return sshApprovalError()
		}
		if path != pin && !st.IsDir() {
			return sshApprovalError()
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	return nil
}

func sshApprovedKey(c winLocalConn, id string, body []byte) error {
	fields := strings.Fields(string(body))
	if len(fields) != 3 || fields[0] != c.SSHHostAlias {
		return sshApprovalError()
	}
	key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(fields[1] + " " + fields[2]))
	if err != nil || len(rest) != 0 {
		return sshApprovalError()
	}
	algorithms := map[string]string{"ssh-ed25519": "ssh-ed25519", "ssh-rsa": "rsa-sha2-512,rsa-sha2-256", "ecdsa-sha2-nistp256": "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384": "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521": "ecdsa-sha2-nistp521"}
	if algorithms[key.Type()] == "" || c.SSHHostAlgorithms != algorithms[key.Type()] {
		return sshApprovalError()
	}
	sum := sha256.Sum256(key.Marshal())
	if filepath.Base(c.SSHKnownHosts) != fmt.Sprintf("%s.%x.known_hosts", id, sum) {
		return sshApprovalError()
	}
	if string(body) != c.SSHHostAlias+" "+string(ssh.MarshalAuthorizedKey(key)) {
		return sshApprovalError()
	}
	return nil
}
