package host

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConnectInfoPrintsAddressTokenAndScope(t *testing.T) {
	file := filepath.Join(t.TempDir(), "owner.token")
	if err := os.WriteFile(file, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldURL, oldFile := BaseURL, TokenFile
	defer func() { BaseURL, TokenFile = oldURL, oldFile }()
	BaseURL, TokenFile = "http://127.0.0.1:8091", file
	t.Setenv("ARKISM_WORKSPACE", "ws")
	t.Setenv("ARKISM_PROJECT", "")

	var out, errOut bytes.Buffer
	if code := connectInfoMain(nil, nil, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var got connectInfo
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != (connectInfo{API: "http://127.0.0.1:8091", Token: "secret", WorkspaceID: "ws", ProjectID: ""}) {
		t.Fatalf("got %+v", got)
	}

	BaseURL = "https://example.com"
	if code := connectInfoMain(nil, nil, &out, &errOut); code != 1 {
		t.Fatalf("an address without a port must be refused, exit %d", code)
	}
}
