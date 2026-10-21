package installtxn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// StopPayload uses verified process handles through termination and waiting.
// It rescans after termination to catch children created during the first scan.
// It proves the inspected payload is idle at return; it cannot prevent a user
// from launching a non-cooperating legacy executable after that observation.
func StopPayload(live string, budget time.Duration) error {
	if budget <= 0 || budget > time.Minute {
		return errors.New("install_stop_budget_invalid")
	}
	l, err := paths(live)
	if err != nil {
		return err
	}
	exists, err := directoryExists(l.live)
	if err != nil || !exists {
		return err
	}
	release, err := pinAncestors(filepath.Dir(l.live))
	if err != nil {
		return err
	}
	defer release()
	deadline := time.Now().Add(budget)
	inventory, err := executableInventory(l.live, deadline)
	if err != nil {
		return err
	}
	targets, err := processTargets(l.live, inventory)
	if err != nil {
		return err
	}
	for {
		if time.Until(deadline) <= 0 {
			return errors.New("install_stop_deadline")
		}
		handles, err := findPayloadProcesses(targets, deadline)
		if err != nil {
			return err
		}
		if len(handles) == 0 {
			return nil
		}
		err = terminateAndWait(handles, deadline)
		closeProcesses(handles)
		if err != nil {
			return err
		}
	}
}

type imageTargets struct {
	names map[string]bool
	paths map[string]bool
}

func processTargets(live string, manifest Manifest) (imageTargets, error) {
	result := imageTargets{map[string]bool{}, map[string]bool{}}
	for _, file := range manifest.Files {
		name := strings.ToLower(file.Path)
		if !strings.HasSuffix(name, ".exe") || !(name == "arkism.exe" || strings.HasPrefix(name, "runtime/") || strings.HasPrefix(name, "integration/")) {
			continue
		}
		path, err := longPath(filepath.Join(live, filepath.FromSlash(file.Path)))
		if err != nil {
			return result, err
		}
		result.paths[strings.ToLower(path)] = true
		result.names[strings.ToLower(filepath.Base(path))] = true
	}
	return result, nil
}
func longPath(path string) (string, error) {
	path = strings.TrimPrefix(path, `\\?\`)
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 32768)
	size, err := windows.GetLongPathName(p, &buffer[0], uint32(len(buffer)))
	if err != nil {
		return "", err
	}
	if size == 0 || size >= uint32(len(buffer)) {
		return "", errors.New("install_image_path_too_long")
	}
	return strings.TrimPrefix(windows.UTF16ToString(buffer[:size]), `\\?\`), nil
}
func closeProcesses(handles []windows.Handle) {
	for _, h := range handles {
		windows.CloseHandle(h)
	}
}
func findPayloadProcesses(targets imageTargets, deadline time.Time) (handles []windows.Handle, err error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	defer func() {
		if err != nil {
			closeProcesses(handles)
			handles = nil
		}
	}()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if time.Until(deadline) <= 0 {
			return handles, errors.New("install_stop_deadline")
		}
		if !targets.names[strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))] {
			continue
		}
		if len(handles) >= 4096 {
			return handles, errors.New("install_process_count_limit")
		}
		handle, inspectErr := inspectProcess(entry.ProcessID, targets)
		if inspectErr != nil {
			return handles, inspectErr
		}
		if handle != 0 {
			handles = append(handles, handle)
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return handles, nil
	}
	return handles, err
}
func inspectProcess(pid uint32, targets imageTargets) (windows.Handle, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return 0, nil
		}
		return 0, fmt.Errorf("install_process_inspection_failed: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			windows.CloseHandle(handle)
		}
	}()
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err = windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		if stopped, _ := windows.WaitForSingleObject(handle, 0); stopped == windows.WAIT_OBJECT_0 {
			return 0, nil
		}
		return 0, err
	}
	image := strings.TrimPrefix(windows.UTF16ToString(buffer[:size]), `\\?\`)
	if len(filepath.VolumeName(image)) != 2 {
		return 0, nil
	}
	path, err := longPath(image)
	if err != nil {
		return 0, err
	}
	if !targets.paths[strings.ToLower(path)] {
		return 0, nil
	}
	if pid == uint32(os.Getpid()) {
		return 0, errors.New("install_helper_inside_live_payload")
	}
	keep = true
	return handle, nil
}
func terminateAndWait(handles []windows.Handle, deadline time.Time) error {
	for _, handle := range handles {
		if err := windows.TerminateProcess(handle, 1); err != nil {
			state, waitErr := windows.WaitForSingleObject(handle, 0)
			if waitErr != nil || state != windows.WAIT_OBJECT_0 {
				return fmt.Errorf("install_process_terminate_failed: %w", err)
			}
		}
	}
	for _, handle := range handles {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("install_stop_deadline")
		}
		milliseconds := uint32((remaining + time.Millisecond - 1) / time.Millisecond)
		state, err := windows.WaitForSingleObject(handle, milliseconds)
		if err != nil {
			return err
		}
		if state != windows.WAIT_OBJECT_0 {
			return errors.New("install_process_not_stopped")
		}
	}
	return nil
}

// Process discovery needs names, not a second hash of every database/runtime file.
func executableInventory(root string, deadline time.Time) (Manifest, error) {
	var result Manifest
	count := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if time.Until(deadline) <= 0 {
			return errors.New("install_stop_deadline")
		}
		count++
		if count > 20000 {
			return errors.New("install_process_inventory_limit")
		}
		if err = ordinary(path, entry.IsDir()); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result.Files = append(result.Files, File{Path: filepath.ToSlash(relative)})
		return nil
	})
	return result, err
}
