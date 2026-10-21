package installtxn

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type linkFile struct {
	file *os.File
	data []byte
	path string
}

func metadataTag(handle windows.Handle, directory bool) error {
	var info struct{ Attributes, Tag uint32 }
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return err
	}
	// Cloud placeholders are not namespace redirections. Symlinks and junctions
	// are name-surrogate reparse points and may not redirect a scoped operation.
	if info.Attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 != directory || info.Tag&0x20000000 != 0 {
		return errors.New("install_metadata_path_redirected")
	}
	return nil
}
func pinMetadataDirectory(path string) (func(), error) {
	var handles []windows.Handle
	release := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			windows.CloseHandle(handles[i])
		}
	}
	var chain []string
	for p := path; ; p = filepath.Dir(p) {
		chain = append(chain, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		pointer, err := windows.UTF16PtrFromString(chain[i])
		if err != nil {
			release()
			return nil, err
		}
		handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			release()
			return nil, err
		}
		handles = append(handles, handle)
		if err = metadataTag(handle, true); err != nil {
			release()
			return nil, err
		}
	}
	return release, nil
}
func resolveKnownDirectory(path string) (string, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if size == 0 || size >= uint32(len(buffer)) {
		return "", errors.New("install_known_folder_path_invalid")
	}
	resolved := windows.UTF16ToString(buffer[:size])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
	} else {
		resolved = strings.TrimPrefix(resolved, `\\?\`)
	}
	return resolved, nil
}
func openLink(path string) (*linkFile, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if err = metadataTag(handle, false); err != nil {
		file.Close()
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxShortcutBytes+1))
	if err != nil || len(data) > maxShortcutBytes {
		file.Close()
		return nil, errors.New("install_metadata_link_read_failed")
	}
	return &linkFile{file, data, path}, nil
}
func newLinkFile(path string, data []byte) (*linkFile, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.FILE_SHARE_READ, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_HIDDEN, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return &linkFile{file, append([]byte{}, data...), path}, nil
}
func (file *linkFile) close() {
	if file != nil && file.file != nil {
		file.file.Close()
		file.file = nil
	}
}
func (file *linkFile) rename(target string) error {
	name, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	var header struct {
		Replace uint32
		Root    windows.Handle
		Length  uint32
		Name    [1]uint16
	}
	offset := int(unsafe.Offsetof(header.Name))
	data := make([]byte, offset+len(name)*2)
	binary.LittleEndian.PutUint32(data[int(unsafe.Offsetof(header.Length)):], uint32((len(name)-1)*2))
	for i, c := range name {
		binary.LittleEndian.PutUint16(data[offset+i*2:], c)
	}
	if err = windows.SetFileInformationByHandle(windows.Handle(file.file.Fd()), windows.FileRenameInfo, &data[0], uint32(len(data))); err != nil {
		return err
	}
	file.path = target
	return nil
}
func (file *linkFile) remove() error {
	if file == nil {
		return nil
	}
	fmt.Println("Only verified installer-owned file removal: " + file.path)
	disposition := byte(1)
	if err := windows.SetFileInformationByHandle(windows.Handle(file.file.Fd()), windows.FileDispositionInfo, &disposition, 1); err != nil {
		return err
	}
	file.close()
	return nil
}

type linkSnapshot struct {
	Exists bool
	Data   []byte
}

func snapshotOf(file *linkFile) linkSnapshot {
	if file == nil {
		return linkSnapshot{}
	}
	return linkSnapshot{true, append([]byte{}, file.data...)}
}
func matchesLink(file *linkFile, expected linkSnapshot) bool {
	return (file != nil) == expected.Exists && (file == nil || bytes.Equal(file.data, expected.Data))
}

type linkSlots struct{ target, backup, pending *linkFile }

func (slots *linkSlots) close() { slots.target.close(); slots.backup.close(); slots.pending.close() }
func openLinkSlots(target, id string, index int) (slots linkSlots, err error) {
	defer func() {
		if err != nil {
			slots.close()
		}
	}()
	backup, pending := linkTemporaryPaths(target, id, index)
	if slots.target, err = openLink(target); err != nil {
		return slots, err
	}
	if slots.backup, err = openLink(backup); err != nil {
		return slots, err
	}
	slots.pending, err = openLink(pending)
	return slots, err
}
func linkTemporaryPaths(target, id string, index int) (string, string) {
	prefix := filepath.Join(filepath.Dir(target), fmt.Sprintf(".Arkism-install-%s-%d", id, index))
	return prefix + ".old", prefix + ".new"
}
func (slots *linkSlots) validateRestore(old linkSnapshot, newData []byte) error {
	if slots.pending != nil && !bytes.HasPrefix(newData, slots.pending.data) {
		return errors.New("install_metadata_foreign_new_file")
	}
	if slots.backup != nil {
		if !old.Exists || !bytes.Equal(slots.backup.data, old.Data) {
			return errors.New("install_metadata_foreign_backup")
		}
		if slots.target != nil && !bytes.Equal(slots.target.data, newData) {
			return errors.New("install_metadata_foreign_link")
		}
		return nil
	}
	if matchesLink(slots.target, old) {
		return nil
	}
	if !old.Exists && slots.target != nil && bytes.Equal(slots.target.data, newData) {
		return nil
	}
	return errors.New("install_metadata_link_ownership_changed")
}
func (slots *linkSlots) restore(target string, old linkSnapshot) error {
	if slots.backup != nil {
		if err := slots.target.remove(); err != nil {
			return err
		}
		slots.target = nil
		if err := slots.backup.rename(target); err != nil {
			return err
		}
		slots.target = slots.backup
		slots.backup = nil
	} else if !old.Exists {
		if err := slots.target.remove(); err != nil {
			return err
		}
		slots.target = nil
	}
	if err := slots.pending.remove(); err != nil {
		return err
	}
	slots.pending = nil
	return nil
}

func (file *linkFile) makeVisible() error {
	var basic struct {
		Creation, Access, Write, Change int64
		Attributes                      uint32
	}
	basic.Attributes = windows.FILE_ATTRIBUTE_NORMAL
	return windows.SetFileInformationByHandle(windows.Handle(file.file.Fd()), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic)))
}

func openTemporaryLinkSlots(target, id string, index int) (slots linkSlots, err error) {
	defer func() {
		if err != nil {
			slots.close()
		}
	}()
	backup, pending := linkTemporaryPaths(target, id, index)
	if slots.backup, err = openLink(backup); err != nil {
		return slots, err
	}
	slots.pending, err = openLink(pending)
	return slots, err
}
