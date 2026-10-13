//go:build linux

package crew

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary runs as "crew run-node --json"; a SIGTERM to it must take the CLI's whole group down.
func TestSigtermTakesTheCLIAndItsChildrenDown(t *testing.T) {
	tmp := t.TempDir()
	pidfile := filepath.Join(tmp, "child.pid")
	cli := filepath.Join(tmp, "claude")
	_ = os.WriteFile(cli, []byte("#!/bin/sh\nsleep 30 &\necho $! > "+pidfile+"\nwait\n"), 0o755)
	cmd := exec.Command(testBinary(t), "crew", "run-node", "--json")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + tmp, "ARKISM_CLAUDE_BIN=" + cli}
	cmd.Stdin = strings.NewReader(`{"model": "opus", "task": "y", "cwd": "/tmp", "access": "read"}`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	grandchild := 0
	for i := 0; i < 100 && grandchild == 0; i++ {
		if raw, err := os.ReadFile(pidfile); err == nil {
			grandchild, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if grandchild == 0 {
		_ = cmd.Process.Kill()
		t.Fatal("the CLI never started")
	}
	_ = cmd.Process.Signal(sigTerm)
	_ = cmd.Wait()
	time.Sleep(200 * time.Millisecond)
	state := "gone"
	if raw, err := os.ReadFile("/proc/" + strconv.Itoa(grandchild) + "/stat"); err == nil {
		state = strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))[0]
	}
	if state != "gone" && state != "Z" {
		t.Fatalf("the CLI's child kept running (%s)", state)
	}
}
