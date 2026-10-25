package subscription

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

var (
	errTimeout  = errors.New("process timed out")
	errTooLarge = errors.New("process output too large")
)

// runBounded runs cmd with stdin closed, stopping it at timeout or once its captured output
// passes limit bytes. With group it runs in its own process group (session), and the whole
// group is killed on return so no descendant outlives the call.
func runBounded(cmd *exec.Cmd, group, captureErr bool, limit int, timeout time.Duration) ([]byte, []byte, int, error) {
	if group {
		if err := newSession(cmd); err != nil {
			return nil, nil, 0, err
		}
	}
	readers := []io.Reader{}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, 0, err
	}
	readers = append(readers, outPipe)
	if captureErr {
		errPipe, err := cmd.StderrPipe()
		if err != nil {
			return nil, nil, 0, err
		}
		readers = append(readers, errPipe)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, 0, err
	}
	kill := func() {
		if !group || killGroup(cmd.Process.Pid) != nil {
			cmd.Process.Kill()
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	total, over := 0, make(chan struct{})
	var overOnce sync.Once
	bufs := make([]bytes.Buffer, len(readers))
	for i, r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk := make([]byte, 65536)
			for {
				n, err := r.Read(chunk)
				mu.Lock()
				total += n
				bufs[i].Write(chunk[:n])
				exceeded := total > limit
				mu.Unlock()
				if exceeded {
					overOnce.Do(func() { close(over) })
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	fail := func(err error) ([]byte, []byte, int, error) {
		kill()
		cmd.Wait() // closes the pipes, which releases the readers
		<-done
		return nil, nil, 0, err
	}
	select {
	case <-done:
	case <-over:
		return fail(errTooLarge)
	case <-timer.C:
		return fail(errTimeout)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-timer.C:
		kill()
		<-waited
		return nil, nil, 0, errTimeout
	}
	if group {
		_ = killGroup(cmd.Process.Pid) // descendants left behind
	}
	var errOut []byte
	if captureErr {
		errOut = bufs[1].Bytes()
	}
	return bufs[0].Bytes(), errOut, cmd.ProcessState.ExitCode(), nil
}

// resolve is Python's Path.resolve(): absolute, with the links of the existing part resolved.
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs
	}
	return filepath.Join(resolve(parent), filepath.Base(abs))
}
