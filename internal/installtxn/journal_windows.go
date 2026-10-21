package installtxn

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type journal struct {
	Format               int
	Live, SID, ID, Phase string
	HadOld               bool
	Old, New             Manifest
	Metadata             []byte
}

func readJSON(path string, value any) error {
	if err := ordinary(path, false); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 32<<20 {
		return errors.New("install_journal_too_large")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return errors.New("install_journal_trailing_data")
	}
	return nil
}
func (j journal) validate(l layout) error {
	if j.Format != 1 || j.Live != l.live || j.SID != l.owner || len(j.ID) != 32 || len(j.Metadata) > 1<<20 {
		return errors.New("install_journal_owner_invalid")
	}
	for _, c := range j.ID {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return errors.New("install_journal_id_invalid")
		}
	}
	if j.Phase != "staging" && j.Phase != "prepared" && j.Phase != "publishing" && j.Phase != "committed" {
		return errors.New("install_journal_phase_invalid")
	}
	if err := j.New.Validate(); err != nil {
		return err
	}
	if j.HadOld && j.Phase != "staging" {
		return j.Old.validate(true)
	}
	return nil
}
func save(l layout, j journal) error {
	if err := j.validate(l); err != nil {
		return err
	}
	temporary := filepath.Join(l.pending, "journal.new")
	path := filepath.Join(l.pending, "journal.json")
	if err := writeNewJSON(temporary, j); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func move(from, to string) error {
	if err := ordinary(from, true); err != nil {
		return err
	}
	if _, err := os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		return errors.New("install_move_target_not_fresh")
	}
	a, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	b, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(a, b, windows.MOVEFILE_WRITE_THROUGH)
}
func archive(l layout, j journal) (string, error) {
	// Only fixed slots are accepted. Retain all payloads and checkpoints; never
	// recursively delete a legacy directory merely because it was moved here.
	entries, err := os.ReadDir(l.pending)
	if err != nil {
		return "", err
	}
	allowed := map[string]bool{"incoming": true, "previous": true, "failed": true, "journal.json": true, "journal.new": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return "", errors.New("install_pending_has_foreign_entry")
		}
	}
	path := filepath.Join(l.versions, "version-"+j.ID)
	if err = move(l.pending, path); err != nil {
		return "", err
	}
	return path, nil
}
