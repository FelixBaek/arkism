package installtxn

import (
	"bytes"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallNativeProcessPeer(t *testing.T) {
	marker := os.Getenv("ARKISM_TXN_PROCESS_TEST_MARKER")
	if marker == "" {
		return
	}
	root := filepath.Dir(marker)
	if mustRead(t, filepath.Join(root, "TEST-OWNER")) != "arkism-installtxn-process-test" {
		t.Fatal("unowned peer root")
	}
	mustWrite(t, marker, "ready")
	for {
		time.Sleep(time.Second)
	}
}

type peer struct {
	command *exec.Cmd
	output  *bytes.Buffer
	handle  windows.Handle
}

func startPeer(t *testing.T, root string) peer { return startPeerAt(t, root, false) }
func startPeerAt(t *testing.T, root string, extended bool) peer {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "TEST-OWNER"), "arkism-installtxn-process-test")
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "Arkism.exe")
	if err = os.WriteFile(exe, image, 0600); err != nil {
		t.Fatal(err)
	}
	// The process reports its long image path even when TEMP uses an 8.3 short name.
	ownedPath, err := longPath(exe)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "ready.txt")
	launch := exe
	if extended {
		launch = `\\?\` + exe
	}
	command := exec.Command(launch, "-test.run=^TestInstallNativeProcessPeer$")
	command.Env = append(os.Environ(), "ARKISM_TXN_PROCESS_TEST_MARKER="+marker)
	output := new(bytes.Buffer)
	command.Stdout = output
	command.Stderr = output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	handle, err := inspectProcess(uint32(command.Process.Pid), imageTargets{names: map[string]bool{"arkism.exe": true}, paths: map[string]bool{strings.ToLower(ownedPath): true}})
	if err != nil || handle == 0 {
		command.Process.Kill()
		command.Wait()
		t.Fatalf("owned process identity could not be retained: %v", err)
	}
	t.Cleanup(func() {
		fmt.Println("Only owned peer termination image: " + exe)
		command.Process.Kill()
		command.Wait()
		windows.CloseHandle(handle)
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && string(data) == "ready" {
			return peer{command, output, handle}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned peer did not become ready")
	return peer{}
}
func TestInstallNativeStopWaitsForOwnedImagesAndPreservesSameNamedForeign(t *testing.T) {
	root := t.TempDir()
	fmt.Println("Only native process test cleanup root: " + root)
	own := startPeer(t, filepath.Join(root, "installed Arkism"))
	foreign := startPeer(t, filepath.Join(root, "unrelated app"))
	if err := StopPayload(filepath.Join(root, "installed Arkism"), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// A live process can be queried with signal 0 only on some systems. Open a
	// retained Windows handle here; it cannot be confused by PID reuse.
	if err := assertPeerExit(own.handle, true); err != nil {
		t.Fatal(err)
	}
	if err := assertPeerExit(foreign.handle, false); err != nil {
		t.Fatal(err)
	}
}
func TestInstallNativeStopAcceptsAlreadyIdleAndRejectsZeroBudget(t *testing.T) {
	f := newFixture(t, true)
	if err := StopPayload(f.live, 0); err == nil {
		t.Fatal("accepted unbounded stop")
	}
	if err := StopPayload(f.live, time.Second); err != nil {
		t.Fatal(err)
	}
	assertOld(t, f, true)
}

func assertPeerExit(handle windows.Handle, exited bool) error {
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return err
	}
	if (state == windows.WAIT_OBJECT_0) != exited {
		return fmt.Errorf("peer exit mismatch: %d", state)
	}
	return nil
}

func TestInstallNativeStopIsUsedBeforeWholeDirectoryPublish(t *testing.T) {
	f := newFixture(t, true)
	own := startPeer(t, f.live)
	foreign := startPeer(t, filepath.Join(f.root, "unrelated app"))
	request := f.request()
	request.Stop = func(path string) error { return StopPayload(path, 5*time.Second) }
	result, err := new(Engine).Install(request)
	if err != nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	if err = assertPeerExit(own.handle, true); err != nil {
		t.Fatal(err)
	}
	if err = assertPeerExit(foreign.handle, false); err != nil {
		t.Fatal(err)
	}
	if err = verify(f.live, f.expected); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, filepath.Join(result.Archive, "previous", "unrelated-kept.txt")) != "preserve prior unknown bytes" {
		t.Fatal("old user-added file lost")
	}
}

func TestInstallNativeStopNormalizesExtendedImagePath(t *testing.T) {
	root := t.TempDir()
	fmt.Println("Only extended-path process test cleanup root: " + root)
	live := filepath.Join(root, "installed Arkism")
	owned := startPeerAt(t, live, true)
	if err := StopPayload(live, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := assertPeerExit(owned.handle, true); err != nil {
		t.Fatal(err)
	}
}
