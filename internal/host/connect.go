package host

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// connect-info is what the desktop app runs over SSH ("ssh HOST arkism connect-info") to connect
// to this server: the API address as seen from this machine, the owner token and the scope the
// server's tools use. The app then forwards that address through the same SSH login, so the user
// types only the SSH address — no token, no Google login, no workspace.
func init() { Register("connect-info", connectInfoMain) }

type connectInfo struct {
	API         string `json:"api"`
	Token       string `json:"token"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
}

func readConnectInfo() (connectInfo, error) {
	api, err := url.Parse(BaseURL)
	if err != nil || api.Scheme != "http" || api.Port() == "" {
		return connectInfo{}, fmt.Errorf("ARKISM_URL must be http://HOST:PORT, got %q", BaseURL)
	}
	raw, err := os.ReadFile(TokenFile)
	if err != nil {
		return connectInfo{}, fmt.Errorf("read token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return connectInfo{}, fmt.Errorf("token file %s is empty", TokenFile)
	}
	return connectInfo{
		API:         BaseURL,
		Token:       token,
		WorkspaceID: Env("ARKISM_WORKSPACE", "default"),
		ProjectID:   Env("ARKISM_PROJECT", "default"),
	}, nil
}

func connectInfoMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: arkism connect-info")
		return 2
	}
	info, err := readConnectInfo()
	if err != nil {
		fmt.Fprintln(stderr, "arkism connect-info:", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(info); err != nil {
		return 1
	}
	return 0
}
