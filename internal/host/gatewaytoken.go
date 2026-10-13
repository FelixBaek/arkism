package host

// The gateway's local token: one random value, made once and kept 0600 next to the other host
// tokens (crew-runner.token). The gateway requires it on every call; the installer puts it in
// Claude Code's ANTHROPIC_CUSTOM_HEADERS. It is a plain file, like the runner token, because a
// service started by systemd cannot rely on an unlocked desktop keyring, and the same value has to
// sit in settings.json anyway.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// GatewayTokenHeader carries the local token. It never leaves the gateway.
const GatewayTokenHeader = "x-arkism-gateway-token"

// GatewayTokenFile holds the token; ARKISM_GATEWAY_TOKEN, when set, is used instead and never written.
var GatewayTokenFile = Env("ARKISM_GATEWAY_TOKEN_FILE", filepath.Join(AgentsState, "gateway.token"))

var gatewayTokenRe = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidGatewayToken reports whether s has the shape EnsureGatewayToken writes.
func ValidGatewayToken(s string) bool { return gatewayTokenRe.MatchString(s) }

// ReadGatewayToken reads a token file: a regular file, not a link, owner-only on Unix.
func ReadGatewayToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("gateway token %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("gateway token %s must be private (0600)", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if !ValidGatewayToken(token) {
		return "", fmt.Errorf("gateway token %s is malformed", path)
	}
	return token, nil
}

// EnsureGatewayToken returns the token at path, making it first when there is none. Two makers
// racing both end with the one that was linked in first; a reader never sees a half-written file.
func EnsureGatewayToken(path string) (string, error) {
	if token, err := ReadGatewayToken(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return token, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gateway-token-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(hex.EncodeToString(raw) + "\n")
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	return ReadGatewayToken(path)
}
