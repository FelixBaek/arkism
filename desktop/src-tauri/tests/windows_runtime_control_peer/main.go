//go:build windows

// This console fixture runs the real control gate without gateway/model/DB work.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

// The hosted build binds this fixture to its exact checkout using -X.
var sourceSHA string

const prefix = "ARKISM_WINDOWS_RUNTIME_CONTROL_"

type owner struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

func ordinary(path string, directory bool) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || attrs.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	return info.IsDir() == directory && (directory || info.Mode().IsRegular())
}

func ordinaryChain(path string) bool {
	for {
		if !ordinary(path, true) {
			return false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return true
		}
		path = parent
	}
}

func hostedRoot() (string, error) {
	sha := os.Getenv("GITHUB_SHA")
	if runtime.GOOS != "windows" || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Getenv("RUNNER_OS") != "Windows" || os.Getenv(prefix+"NATIVE") != "1" || len(sha) != 40 || strings.IndexFunc(sha, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) >= 0 || sha != sourceSHA || sha != os.Getenv(prefix+"SOURCE_SHA") {
		return "", errors.New("fixture_guard")
	}
	root, temp := os.Getenv(prefix+"ROOT"), os.Getenv("RUNNER_TEMP")
	rel, err := filepath.Rel(temp, root)
	if err != nil || !filepath.IsAbs(root) || !filepath.IsAbs(temp) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || !ordinaryChain(root) || !ordinary(filepath.Join(root, "owner.json"), false) {
		return "", errors.New("fixture_owner")
	}
	file, err := os.Open(filepath.Join(root, "owner.json"))
	if err != nil {
		return "", errors.New("fixture_owner")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 513))
	if errors.Join(readErr, file.Close()) != nil || len(raw) > 512 {
		return "", errors.New("fixture_owner")
	}
	var row owner
	if json.Unmarshal(raw, &row) != nil || row.Kind != "arkism_runtime_control_v1" || row.Source != sha {
		return "", errors.New("fixture_owner")
	}
	return root, nil
}

func marker(root, name string) error {
	file, err := os.OpenFile(filepath.Join(root, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("fixture_marker")
	}
	_, writeErr := file.WriteString("ok")
	return errors.Join(writeErr, file.Close())
}

func waitRelease(root, name string) bool {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ordinary(filepath.Join(root, name), false) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func run() error {
	root, err := hostedRoot()
	if err != nil {
		return err
	}
	if os.Getenv(prefix+"PEER_MODE") == "silent" {
		if !waitRelease(root, "silent_release") {
			return errors.New("fixture_silent_timeout")
		}
		return nil
	}
	if os.Getenv(prefix+"PEER_MODE") != "protocol" {
		return errors.New("fixture_mode")
	}
	control, err := desktopcontrol.FromEnvironment("gateway", os.Stdin, os.Stdout)
	if err != nil || control == nil {
		return errors.New("fixture_configuration")
	}
	release, err := control.Admission().Enter(desktopcontrol.Handler)
	if err != nil {
		return errors.New("fixture_activity")
	}
	go func() {
		if !waitRelease(root, "release_activity") {
			return
		}
		release()
		_ = marker(root, "activity_released")
	}()
	if control.Start(func(ctx context.Context) error {
		if ctx.Err() != nil {
			return errors.New("fixture_stop_context")
		}
		return marker(root, "go_stop")
	}) != nil {
		return errors.New("fixture_start")
	}
	select {
	case <-control.Done():
		return control.Err()
	case <-time.After(30 * time.Second):
		return errors.New("fixture_deadline")
	}
}

func main() {
	if run() != nil {
		// No raw error, argv, environment, nonce or filesystem path is emitted.
		_, _ = os.Stderr.WriteString("runtime_control_fixture_failed\n")
		os.Exit(2)
	}
}
