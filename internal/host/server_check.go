package host

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/FelixBaek/arkism/internal/buildinfo"
)

// The desktop app's server update runs these over SSH with the freshly installed binary:
// `arkism version` names the build, `arkism server-check --want V` waits until the API at
// ARKISM_URL answers /healthz with version V (the restarted service is the new one);
// `--want ""` accepts any healthy answer (after a rollback to a build that reports no version).
func init() {
	Register("version", versionMain)
	Register("server-check", serverCheckMain)
}

func versionMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: arkism version")
		return 2
	}
	fmt.Fprintln(stdout, buildinfo.Version)
	return 0
}

func serverCheckMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("server-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	want := fs.String("want", "", "version the API must report (empty: any healthy API)")
	wait := fs.Duration("wait", 60*time.Second, "how long to keep asking")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: arkism server-check --want VERSION [--wait 60s]")
		return 2
	}
	got, err := waitForVersion(BaseURL, *want, *wait, time.Second)
	_ = json.NewEncoder(stdout).Encode(map[string]any{"want": *want, "got": got.Version, "api_level": got.APILevel, "ok": err == nil})
	if err != nil {
		fmt.Fprintln(stderr, "arkism server-check:", err)
		return 1
	}
	return 0
}

func waitForVersion(base, want string, wait, every time.Duration) (buildinfo.Health, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(wait)
	var last buildinfo.Health
	var err error
	for {
		last, err = readHealth(client, base)
		if err == nil && (want == "" || last.Version == want) {
			return last, nil
		}
		if err == nil {
			err = fmt.Errorf("API reports version %q, want %q", last.Version, want)
		}
		if time.Now().Add(every).After(deadline) {
			return last, err
		}
		time.Sleep(every)
	}
}

func readHealth(client *http.Client, base string) (buildinfo.Health, error) {
	var h buildinfo.Health
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		return h, fmt.Errorf("API not answering")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("API answered %d", resp.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&h)
	return h, err
}
