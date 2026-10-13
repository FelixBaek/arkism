//go:build windows

package subscription

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// newSession starts the usage probe in its own process group with no console window, so a
// timeout can end it and everything it started (claude.cmd -> cmd.exe -> claude.exe).
func newSession(cmd *exec.Cmd) error {
	const createNewProcessGroup, createNoWindow = 0x00000200, 0x08000000
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup | createNoWindow
	return nil
}

// killGroup ends the process and its whole tree.
func killGroup(pid int) error {
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	return kill.Run()
}

func openNoFollow(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err != nil {
		return nil, err
	} else if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return nil, errors.New("credential file is a link")
	}
	return os.Open(path)
}
