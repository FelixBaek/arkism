package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintenanceOptions(t *testing.T) {
	good := [][]string{{"status"}, {"advance", "--expected-revision", "2"}, {"hold", "--expected-revision=2"}, {"release", "--expected-revision", "3"}, {"cancel", "--expected-revision", "3"}, {"begin-privacy", "--request", "request.json", "--allow-global-downtime"}, {"begin-supersede", "--request", "request.json", "--memory-id", "m", "--allow-global-downtime", "--timeout", "1m"}}
	for _, args := range good {
		if _, _, e := parseOptions(args); e != nil {
			t.Fatal(args, e)
		}
	}
	bad := [][]string{{}, {"unlock"}, {"status", "--token-file", "secret"}, {"advance"}, {"cancel"}, {"advance", "--expected-revision", "0"}, {"begin-privacy", "--request", "x"}, {"begin-privacy", "--request", "x", "--allow-global-downtime=false"}, {"begin-supersede", "--request", "x", "--allow-global-downtime"}, {"status", "--expected-revision", "1"}, {"hold", "--expected-revision", "1", "--expected-revision", "2"}, {"status", "--timeout", "16m"}, {"status", "--token", "SECRET"}}
	for _, args := range bad {
		if _, _, e := parseOptions(args); e == nil {
			t.Fatal("accepted", args)
		}
	}
	if o, help, e := parseOptions([]string{"status"}); e != nil || help || o.timeout != 30*time.Second {
		t.Fatal(o, help, e)
	}
}
func TestMaintenanceSecretFileAndStrictRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if e := os.WriteFile(path, []byte("private-test-token\n"), 0600); e != nil {
		t.Fatal(e)
	}
	empty := func(string) string { return "" }
	if token, e := loadToken(options{tokenFile: path}, empty); e != nil || token != "private-test-token" {
		t.Fatal("private token rejected")
	}
	if _, e := loadToken(options{tokenFile: path}, func(string) string { return "another-token" }); e == nil {
		t.Fatal("ambiguous token sources")
	}
	if e := os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := loadToken(options{tokenFile: path}, empty); e == nil {
		t.Fatal("public token file")
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := loadToken(options{tokenFile: link}, empty); e == nil {
		t.Fatal("symlink token")
	}
	var req struct {
		Scope string `json:"scope"`
	}
	for _, body := range []string{`{"scope":"x","extra":true}`, `{"scope":"x"} {}`, `not JSON`} {
		if e := os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		if e := decodeRequest(path, &req); e == nil {
			t.Fatal("non-strict request accepted")
		}
	}
}
