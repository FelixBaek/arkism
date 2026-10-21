package processlock

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessLockCrashRecovery(t *testing.T) {
	if path := os.Getenv("ARKISM_TEST_PROCESS_LOCK"); path != "" {
		unlock, err := Acquire(path)
		if err != nil {
			os.Exit(2)
		}
		defer unlock()
		fmt.Println("ready")
		for {
			time.Sleep(time.Hour)
		}
	}
	path := filepath.Join(t.TempDir(), "daemon.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessLockCrashRecovery$")
	cmd.Env = append(os.Environ(), "ARKISM_TEST_PROCESS_LOCK="+path)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan bool, 1)
	go func() { s := bufio.NewScanner(pipe); ready <- s.Scan() && s.Text() == "ready" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child failed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child lock timeout")
	}
	if unlock, err := Acquire(path); err == nil {
		unlock()
		t.Fatal("two daemons own one lock")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	unlock, err := Acquire(path)
	if err != nil {
		t.Fatal("crash retained lock", err)
	}
	unlock()
}
