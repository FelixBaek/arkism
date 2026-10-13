//go:build linux

package skills

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// TestMain lets the test binary stand in for an agent CLI: it prints a sign-in address and waits on its loopback.
func TestMain(m *testing.M) {
	if os.Getenv("ARKISM_FAKE_LOGIN_CLI") == "1" {
		os.Exit(fakeLoginCLI())
	}
	os.Exit(m.Run())
}

func fakeLoginCLI() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 4
	}
	code := make(chan string, 1)
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
		code <- r.URL.Query().Get("code")
	}))
	port := ln.Addr().(*net.TCPAddr).Port
	fmt.Printf("Visit this URL to authorize:\n  https://auth.example/authorize?response_type=code&redirect_uri=http%%3A%%2F%%2F127.0.0.1%%3A%d%%2Fcallback&state=s1\n", port)
	if <-code == "c1" {
		return 0
	}
	return 3
}

func runFakeLogin(t *testing.T, callback string) []string {
	t.Helper()
	t.Setenv("ARKISM_FAKE_LOGIN_CLI", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clis, step, req := loginCLIs, loginStep, loginRequest
	t.Cleanup(func() { loginCLIs, loginStep, loginRequest = clis, step, req })
	loginCLIs = map[string]func() string{"codex": func() string { return exe }}
	var steps []string
	loginStep = func(_ string, body host.Obj) error { steps = append(steps, canon(t, body)); return nil }
	loginRequest = func(string) (map[string]any, error) {
		return map[string]any{"status": "waiting", "callback": callback}, nil
	}
	if err := runLogin("r1", "codex", "cloudflare-api"); err != nil {
		steps = append(steps, "raised: "+err.Error())
	}
	return steps
}

func TestRedirectIsReplayedOnTheCLILoopback(t *testing.T) {
	steps := runFakeLogin(t, "/callback?code=c1&state=s1")
	if !strings.Contains(steps[0], `"status":"waiting"`) || !strings.Contains(steps[0], "redirect_uri=http%3A%2F%2F127.0.0.1") || steps[len(steps)-1] != `{"status":"done"}` {
		t.Fatal(steps)
	}
}

func TestAFailedSignInIsReported(t *testing.T) {
	steps := runFakeLogin(t, "/callback?code=wrong&state=s1")
	if !strings.HasPrefix(steps[len(steps)-1], "raised: ") {
		t.Fatal(steps)
	}
}
